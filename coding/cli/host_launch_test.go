package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// hostLaunchRecord is one line of the host-launch-go fixture's report.
type hostLaunchRecord struct {
	Event       string   `json:"event"`
	Error       string   `json:"error"`
	Argv        []string `json:"argv"`
	Mode        string   `json:"mode"`
	ContextMode string   `json:"contextMode"`
	StdinIsTTY  bool     `json:"stdinIsTTY"`
	StdoutIsTTY bool     `json:"stdoutIsTTY"`
}

func readHostLaunchReport(t *testing.T, path string) []hostLaunchRecord {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("the fixture wrote no report: %v", err)
	}
	defer func() { _ = file.Close() }()
	var records []hostLaunchRecord
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record hostLaunchRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("report line %q: %v", scanner.Text(), err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return records
}

func hostLaunchEvents(records []hostLaunchRecord) []string {
	events := make([]string, len(records))
	for i, record := range records {
		events[i] = record.Event
		if record.Error != "" {
			events[i] += ":" + record.Error
		}
	}
	return events
}

// hostLaunchEnv is an isolated environment for one pig run of the host-launch-go fixture.
func hostLaunchEnv(t *testing.T, report string, extra ...string) []string {
	t.Helper()
	home := t.TempDir()
	return append(append(os.Environ(), "HOME="+home, "PIG_HOME="+filepath.Join(home, ".pig"), "PI_HOME="+filepath.Join(home, ".pi"),
		"PIG_CODING_AGENT_DIR="+filepath.Join(home, "agent"), "PI_CODING_AGENT_DIR="+filepath.Join(home, "agent"), "PIG_TEST_FAUX=1", "HOST_LAUNCH_REPORT="+report), extra...)
}

// runHostLaunch runs pig with args and the fixture's environment, stdin empty, and returns stdout and the exit status.
func runHostLaunch(t *testing.T, binary string, env, args []string) (string, int) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), binary, args...)
	cmd.Dir = t.TempDir()
	cmd.Env = env
	cmd.Stdin = strings.NewReader("")
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run pig: %v\nstderr:\n%s", err, stderr.String())
	}
	return stdout.String(), code
}

// A Pi extension reads how Pi was started from process.argv, process.stdin.isTTY and process.stdout.isTTY, because it runs in Pi's process. A Go extension's own os.Args is its helper's, so ctx.HostLaunch() returns pig's (D236): every token after the executable, in order and unchanged, the run mode ctx.mode reports, and the terminal state of pig's stdin and stdout.
func TestGoExtensionReadsHowPigWasStarted(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the pig binary")
	}
	t.Parallel()
	binary := buildPigBinaryForSignalTest(t)
	fixture := buildGoSDKFixture(t, "host-launch-go")
	for _, mode := range []string{"print", "json", "rpc"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			report := filepath.Join(t.TempDir(), "report.jsonl")
			args := []string{"--no-approve", "--models", "a,b", "-e", fixture, "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-session", "--append-system-prompt", "two words, ü"}
			if mode == "print" {
				args = append(args, "--print")
			} else {
				args = append(args, "--mode", mode)
			}
			if _, code := runHostLaunch(t, binary, hostLaunchEnv(t, report), args); code != 0 {
				t.Fatalf("pig exited %d", code)
			}
			records := readHostLaunchReport(t, report)
			if len(records) == 0 || records[0].Event != "hostLaunch" || records[0].Error != "" {
				t.Fatalf("report %v, want the launch first", hostLaunchEvents(records))
			}
			launch := records[0]
			if !slices.Equal(launch.Argv, args) {
				t.Fatalf("argv = %q, want %q", launch.Argv, args)
			}
			if launch.Mode != mode || launch.ContextMode != mode {
				t.Fatalf("mode = %q, ctx.Mode() = %q, want %q", launch.Mode, launch.ContextMode, mode)
			}
			if launch.StdinIsTTY || launch.StdoutIsTTY {
				t.Fatalf("stdinIsTTY = %v, stdoutIsTTY = %v, want both false for pipes", launch.StdinIsTTY, launch.StdoutIsTTY)
			}
		})
	}
}

// A Pi extension stops a misconfigured startup with process.exit(code) in its session_start handler: the process ends with that status before the prompt on the command line reaches a provider, in every mode. ctx.ShutdownWithCode is that call for a Go extension (D236). The fixture's before_agent_start handler shows whether a prompt reached the agent; the control run without the request shows that the prompt does reach it.
func TestShutdownWithCodeEndsStartupBeforeThePrompt(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the pig binary")
	}
	t.Parallel()
	binary := buildPigBinaryForSignalTest(t)
	fixture := buildGoSDKFixture(t, "host-launch-go")
	base := []string{"-e", fixture, "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-session", "--model", "test-faux/faux-1"}
	for _, tc := range []struct {
		name     string
		args     []string
		exit     string
		wantCode int
		want     []string
	}{
		{"print", []string{"-p", "hello"}, "1", 1, []string{"hostLaunch", "shutdownWithCode", "session_shutdown"}},
		{"json", []string{"--mode", "json", "hello"}, "2", 2, []string{"hostLaunch", "shutdownWithCode", "session_shutdown"}},
		{"rpc", []string{"--mode", "rpc"}, "3", 3, []string{"hostLaunch", "shutdownWithCode", "session_shutdown"}},
		{"print zero", []string{"-p", "hello"}, "0", 0, []string{"hostLaunch", "shutdownWithCode", "session_shutdown"}},
		{"print out of range", []string{"-p", "hello"}, "256", 1, []string{"hostLaunch", "shutdownWithCode:shutdownWithCode: exit code 256 is outside 0..255", "before_agent_start", "session_shutdown"}},
		{"print control", []string{"-p", "hello"}, "", 1, []string{"hostLaunch", "before_agent_start", "session_shutdown"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			report := filepath.Join(t.TempDir(), "report.jsonl")
			var extra []string
			if tc.exit != "" {
				extra = append(extra, "HOST_LAUNCH_EXIT="+tc.exit)
			}
			stdout, code := runHostLaunch(t, binary, hostLaunchEnv(t, report, extra...), append(slices.Clone(base), tc.args...))
			if code != tc.wantCode {
				t.Fatalf("exit status %d, want %d (stdout %q)", code, tc.wantCode, stdout)
			}
			if got := hostLaunchEvents(readHostLaunchReport(t, report)); !slices.Equal(got, tc.want) {
				t.Fatalf("report %q, want %q", got, tc.want)
			}
			if tc.name == "print" && stdout != "" {
				t.Fatalf("stdout %q, want no result after the exit request", stdout)
			}
		})
	}
}
