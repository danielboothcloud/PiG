//go:build darwin || linux

package cli

import (
	"bytes"
	"io"
	"os/exec"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/internal/testbudget"
)

// pig divergence (D238): in interactive mode on a terminal, the fixture relaunches in session_start of a --continue session. The first TUI stops, and the same pig process starts the relaunched TUI on the same terminal with the scope, both extensions and the continued session; a prompt typed there lands in that session, and /quit ends the process with status 0.
func TestRelaunchRestartsTheInteractiveTUIOnTheSameTerminal(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the pig binary")
	}
	t.Parallel()
	r := newRelaunchRig(t)
	r.createSession(t)
	cmd := exec.Command(r.binary, append([]string{"--continue", "-e", r.first}, r.base()...)...)
	cmd.Dir = r.cwd
	cmd.Env = append(r.relaunchEnv(t, "session_start", nil, ""), "TERM=xterm-256color")
	master, slave := openModePTY(t)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = slave.Close()
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(done) }()
	output := &editorProbeOutput{}
	go func() { _, _ = io.Copy(output, master) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
		_ = master.Close()
	})
	tail := func() string {
		all := output.since(0)
		return string(all[max(0, len(all)-3000):])
	}
	await := func(stage string, marker string) {
		t.Helper()
		deadline := time.Now().Add(testbudget.Wait(t))
		for time.Now().Before(deadline) {
			if bytes.Contains(output.since(0), []byte(marker)) {
				return
			}
			select {
			case <-done:
				t.Fatalf("pig exited %s; last output: %q", stage, tail())
			case <-time.After(20 * time.Millisecond):
			}
		}
		t.Fatalf("%s: %q never appeared; last output: %q", stage, marker, tail())
	}
	write := func(text string) {
		t.Helper()
		if _, err := master.WriteString(text); err != nil {
			t.Fatal(err)
		}
	}

	r.waitRecord(t, "the relaunched runtime", done, func(record relaunchRecord) bool {
		return record.Stage == "2" && record.Ext == "relaunch-second-go" && record.Event == "session_start"
	})
	// Enter keeps the text while startup runs; the relaunched TUI's extension listing names the second extension once it is ready.
	await("starting the relaunched TUI", "relaunch-second-go")
	write("reply with exactly: second answer")
	time.Sleep(200 * time.Millisecond)
	write("\r")
	r.waitRecord(t, "the relaunched runtime's answer", done, func(record relaunchRecord) bool { return record.Stage == "2" && record.Event == "agent_end" })
	write("/quit")
	time.Sleep(200 * time.Millisecond)
	write("\r")
	select {
	case <-done:
	case <-time.After(testbudget.Wait(t)):
		t.Fatalf("/quit did not exit; last output: %q", tail())
	}
	if waitErr != nil {
		t.Fatalf("pig ended with %v, want exit status 0", waitErr)
	}
	// The first TUI restored the terminal before the image was replaced: bracketed paste is switched on, off, and on again by the relaunched TUI, all before the relaunched TUI lists the second extension.
	screen := output.since(0)
	listed := bytes.Index(screen, []byte("relaunch-second-go"))
	on, off := []byte("\x1b[?2004h"), []byte("\x1b[?2004l")
	first := bytes.Index(screen, on)
	restored := first + bytes.Index(screen[first:], off)
	again := restored + bytes.Index(screen[restored:], on)
	if first < 0 || restored < first || again < restored || again > listed {
		t.Fatalf("bracketed paste on %d, off %d, on again %d, second extension listed at %d: want the first TUI restored before the relaunched one started", first, restored, again, listed)
	}
	records := r.records(t)
	file := r.assertRelaunched(t, records, cmd.Process.Pid, "tui")
	if i := slices.IndexFunc(records, func(record relaunchRecord) bool { return record.Event == "before_agent_start" }); i < 0 || records[i].Stage != "2" {
		t.Fatalf("report %v, want the only prompt in the relaunched runtime", records)
	}
	headers, users := sessionContent(t, file)
	if len(headers) != 1 || headers[0] != records[0].SessionID || len(users) != 2 || !bytes.Contains([]byte(users[1]), []byte("second answer")) {
		t.Fatalf("session headers %q users %q, want the continued session with the typed prompt", headers, users)
	}
	r.assertNoPigLeft(t)
}
