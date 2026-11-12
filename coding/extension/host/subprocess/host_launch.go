package subprocess

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"

	"golang.org/x/term"
)

// pig divergence (D236): a Pi extension runs in Pi's own Node process, so it reads the command line from process.argv, whether stdin and stdout are terminals from process.stdin.isTTY and process.stdout.isTTY, and ends startup with process.exit(code). A subprocess extension's argv, stdio and exit are its own process's. CallHostLaunch answers the first three from the Host, and CallShutdownWithCode is ctx.shutdown() with the exit status that process.exit gives.

// CallHostLaunch (ext→host) reads how pig was started. The call has no arguments and the result is a HostLaunch.
const CallHostLaunch = "hostLaunch"

// CallShutdownWithCode (ext→host) is the shutdown call with the pig process's exit status. The args are ShutdownWithCodeArgs and the result is null.
const CallShutdownWithCode = "shutdownWithCode"

// HostLaunch is the CallHostLaunch result. Argv is the pig process's command line after the executable, in order and unchanged, as process.argv.slice(2) is in Pi; it can hold a secret passed as an argument (--api-key), as Pi's does. Mode is the run mode an extension reads as ctx.mode. StdinIsTTY and StdoutIsTTY report whether pig's stdin and stdout were terminals when it started. Nothing is read from the environment.
type HostLaunch struct {
	Argv        []string `json:"argv"`
	Mode        string   `json:"mode"`
	StdinIsTTY  bool     `json:"stdinIsTTY"`
	StdoutIsTTY bool     `json:"stdoutIsTTY"`
}

// ShutdownWithCodeArgs is the argument of CallShutdownWithCode. Code is the exit status, 0 to 255.
type ShutdownWithCodeArgs struct {
	Code *int `json:"code"`
}

// LaunchTerminals is whether pig's stdin and stdout were terminals when it started.
type LaunchTerminals struct {
	Stdin  bool
	Stdout bool
}

// currentLaunchTerminals reads the terminal state of os.Stdin and os.Stdout now. A caller that replaces os.Stdout (the output guard of print, JSON and RPC mode) reads it first and passes it to SetLaunchTerminals.
func currentLaunchTerminals() LaunchTerminals {
	return LaunchTerminals{Stdin: term.IsTerminal(int(os.Stdin.Fd())), Stdout: term.IsTerminal(int(os.Stdout.Fd()))}
}

// SetLaunchTerminals records whether pig's stdin and stdout were terminals at launch. Call before Load. NewHost reads them when it is created.
func (h *Host) SetLaunchTerminals(terminals LaunchTerminals) {
	h.launchTerminals = terminals
}

// handleHostLaunch serves CallHostLaunch.
func (h *Host) handleHostLaunch() (*CallResultPayload, error) {
	argv := slices.Clone(h.harnessArgs)
	if argv == nil {
		argv = []string{}
	}
	mode := h.mode
	if mode == "" {
		// The SDKs read an omitted ready mode as "print" (runner.ts:229).
		mode = "print"
	}
	result, err := json.Marshal(HostLaunch{Argv: argv, Mode: mode, StdinIsTTY: h.launchTerminals.Stdin, StdoutIsTTY: h.launchTerminals.Stdout}) //portlint:allow jsonescape PiG-only call; every SDK's JSON parser decodes \u003c, \u003e and \u0026 to the same argv text
	if err != nil {
		return nil, err
	}
	return &CallResultPayload{Result: result}, nil
}

// handleShutdownWithCode serves CallShutdownWithCode.
func (b *UIBridge) handleShutdownWithCode(actions *HostCallbacks, args json.RawMessage) (*CallResultPayload, error) {
	var parsed ShutdownWithCodeArgs
	if err := json.Unmarshal(args, &parsed); err != nil {
		return nil, fmt.Errorf("decode %s: %w", CallShutdownWithCode, err)
	}
	if parsed.Code == nil {
		return nil, errors.New(CallShutdownWithCode + " requires code")
	}
	if *parsed.Code < 0 || *parsed.Code > 255 {
		return nil, fmt.Errorf("%s: exit code %d is outside 0..255", CallShutdownWithCode, *parsed.Code)
	}
	if actions == nil || actions.ShutdownWithCode == nil {
		return nil, errors.New(CallShutdownWithCode + " is not available in this host")
	}
	actions.ShutdownWithCode(*parsed.Code)
	return &CallResultPayload{}, nil
}
