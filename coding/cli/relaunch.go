package cli

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/MichaelKinsy/PiG/coding/extension/host/subprocess"
	"github.com/MichaelKinsy/PiG/internal/codingagent"
	"github.com/MichaelKinsy/PiG/internal/linkerexec"
	"github.com/MichaelKinsy/PiG/tui"
)

// pig divergence (D238): a Pi extension restarts Pi with another extension set or model scope by spawning a child Pi on the terminal and exiting with the child's status. A subprocess extension cannot do that, so an accepted relaunch ends the runtime the way the mode's shutdown does and then replaces this process's image with pig started with the requested arguments and environment: the same PID, terminal and standard streams, and one pig process.
//
// Async contract: the relaunch call returns once the host accepted or refused the request. An accepted relaunch is a shutdown request (interactive mode stops when idle, RPC mode at its shutdown check, print and JSON mode abort the run and write no result); session_shutdown runs with reason "quit"; then the process image is replaced where the process would otherwise exit with status 0. A termination signal, a crash or an exit status other than 0 ends the process instead, without a relaunch.

// relaunchPlan is an accepted relaunch: the program, its argv (argv[0] first) and the environment changes applied over this process's environment when it executes.
type relaunchPlan struct {
	path string
	argv []string
	env  map[string]*string
}

var (
	// pendingRelaunch is the accepted relaunch; at most one is accepted per process.
	pendingRelaunch atomic.Pointer[relaunchPlan]
	// pipedStdinConsumed is set once pig read non-blank piped stdin, which a relaunched pig could not read again.
	pipedStdinConsumed atomic.Bool
)

// relaunchSelectsSession reports whether args choose the session themselves: a session file or ID, the most recent session, the picker, a fork or no session.
func relaunchSelectsSession(args []string) bool {
	parsed := parseArgs(args)
	return parsed.Session != "" || parsed.SessionIDSet || parsed.Continue || parsed.Resume || parsed.Fork != "" || parsed.NoSession
}

// relaunchArgs returns the relaunched pig's arguments: args, after `--session <sessionFile>` when args select no session and the session file exists. A session whose file is not written yet (Pi writes it with the first assistant reply) or that has none (--no-session) is not carried.
func relaunchArgs(args []string, sessionFile string) []string {
	if sessionFile == "" || relaunchSelectsSession(args) {
		return slices.Clone(args)
	}
	if info, err := os.Stat(sessionFile); err != nil || !info.Mode().IsRegular() {
		return slices.Clone(args)
	}
	return append([]string{"--session", sessionFile}, args...)
}

// relaunchEnviron applies changes over base: a changed variable leaves its place, and the set ones follow in name order.
func relaunchEnviron(base []string, changes map[string]*string) []string {
	env := make([]string, 0, len(base)+len(changes))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if _, changed := changes[key]; changed {
			continue
		}
		env = append(env, entry)
	}
	for _, key := range slices.Sorted(maps.Keys(changes)) {
		if value := changes[key]; value != nil {
			env = append(env, key+"="+*value)
		}
	}
	return env
}

// acceptRelaunch records request as the pending relaunch, or returns why this process cannot restart. The mode checks its own state first and starts its shutdown after a nil return.
func acceptRelaunch(request subprocess.RelaunchRequest) error {
	if !relaunchSupported {
		return errRelaunchUnsupported
	}
	if pipedStdinConsumed.Load() {
		return errors.New(subprocess.CallRelaunch + ": pig already read its piped stdin, which a relaunched pig could not read again")
	}
	if pendingRelaunch.Load() != nil {
		return errors.New(subprocess.CallRelaunch + ": a relaunch is already pending")
	}
	executable, err := linkerexec.Executable()
	if err != nil {
		return fmt.Errorf("%s: locate the pig executable: %w", subprocess.CallRelaunch, err)
	}
	argv0 := executable
	if len(os.Args) > 0 && os.Args[0] != "" {
		argv0 = os.Args[0]
	}
	path, argv, err := linkerexec.Current().Resolve(executable, append([]string{argv0}, relaunchArgs(request.Args, request.SessionFile)...))
	if err != nil {
		return fmt.Errorf("%s: %w", subprocess.CallRelaunch, err)
	}
	if !pendingRelaunch.CompareAndSwap(nil, &relaunchPlan{path: path, argv: argv, env: maps.Clone(request.Env)}) {
		return errors.New(subprocess.CallRelaunch + ": a relaunch is already pending")
	}
	// pig additive (D102): the exit record names the relaunch as this process image's ending.
	codingagent.RecordEnding(codingagent.AgentDir(), "extension requested relaunch")
	return nil
}

// relaunchPending reports whether a relaunch was accepted.
func relaunchPending() bool { return pendingRelaunch.Load() != nil }

// releaseForRelaunch releases what exitProcess releases before the process exits, except the profiles, which every caller has written.
func releaseForRelaunch() {
	stopAutomaticExtensionCacheGC()
	stopModelServices()
	stopStartupExtensions()
	codingagent.RestoreStdout()
	codingagent.EndSessionMarker()
	tui.RestoreTerminalOnExit()
	codingagent.BeforeExit()
}

// relaunchAfterMain runs the accepted relaunch when Main returns normally. Any panic continues.
func relaunchAfterMain() {
	if value := recover(); value != nil {
		panic(value)
	}
	if relaunchPending() {
		releaseForRelaunch()
		runPendingRelaunch()
	}
}

// runPendingRelaunch replaces the process image with the accepted relaunch. It returns when none is pending or a termination signal ended the process; when the replacement fails it reports why and exits 1, because the runtime is gone.
func runPendingRelaunch() {
	plan := pendingRelaunch.Load()
	if plan == nil || receivedTerminationSignal.Load() != 0 {
		return
	}
	err := execImage(plan.path, plan.argv, relaunchEnviron(os.Environ(), plan.env))
	fmt.Fprintf(os.Stderr, "pig: relaunch failed: %v\n", err)
	tui.RestoreTerminalOnExit()
	os.Exit(1)
}
