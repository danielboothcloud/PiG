//go:build darwin || linux

package cli

import (
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/internal/testbudget"
)

// In interactive mode on a terminal, ctx.HostLaunch() reports mode "tui" with stdin and stdout terminals, and ctx.ShutdownWithCode(1) in session_start ends pig with status 1 before the command-line prompt is sent, as process.exit(1) does in a Pi extension (D236). Without the fix the TUI kept running (ctx.Shutdown() has no status) or exited 0.
func TestShutdownWithCodeEndsInteractiveStartup(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the pig binary")
	}
	t.Parallel()
	binary := buildPigBinaryForSignalTest(t)
	fixture := buildGoSDKFixture(t, "host-launch-go")
	report := filepath.Join(t.TempDir(), "report.jsonl")
	args := []string{"--no-approve", "--models", "a,b", "-e", fixture, "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-session", "--model", "test-faux/faux-1", "hello"}
	cmd := exec.Command(binary, args...)
	cmd.Dir = t.TempDir()
	cmd.Env = append(hostLaunchEnv(t, report, "HOST_LAUNCH_EXIT=1"), "TERM=xterm-256color", "PIG_OFFLINE=1")
	master, slave := openModePTY(t)
	t.Cleanup(func() { _ = master.Close() })
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = slave.Close()
	go func() { _, _ = io.Copy(io.Discard, master) }()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	var err error
	select {
	case err = <-exited:
	case <-time.After(testbudget.Wait(t)):
		_ = cmd.Process.Kill()
		<-exited
		t.Fatal("pig did not exit after ctx.ShutdownWithCode(1)")
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("pig ended with %v, want exit status 1", err)
	}
	records := readHostLaunchReport(t, report)
	if got, want := hostLaunchEvents(records), []string{"hostLaunch", "shutdownWithCode", "session_shutdown"}; !slices.Equal(got, want) {
		t.Fatalf("report %q, want %q", got, want)
	}
	launch := records[0]
	if !slices.Equal(launch.Argv, args) || launch.Mode != "tui" || launch.ContextMode != "tui" || !launch.StdinIsTTY || !launch.StdoutIsTTY {
		t.Fatalf("launch = %+v, want argv %q, mode tui and terminal stdin and stdout", launch, args)
	}
}
