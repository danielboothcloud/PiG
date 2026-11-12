//go:build linux || darwin

package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/internal/testbudget"
)

// pig divergence (D237): a Pi extension writes terminal control strings to process.stdout, the terminal its TUI draws on; a Go extension's stdout is the host wire, so it calls ctx.WriteTerminal and the host writes the string between frames. These tests run the real binary with a real Go extension.

const termWriteOSC = "\x1b]777;notify;PiG probe;written between frames\x07"

const termWriteProbeSource = `package termwprobe

import (
	"fmt"
	"os"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"
)

func Extension() *sdk.Extension {
	ext := sdk.New("termwprobe")
	ext.RegisterCommand("termwrite", sdk.CommandOptions{
		Description: "write an OSC 777 notification",
		Handler: func(ctx sdk.Context, _ string) error {
			err := ctx.WriteTerminal("\x1b]777;notify;PiG probe;written between frames\x07")
			result := fmt.Sprintf("termwrite err=%v", err)
			if path := os.Getenv("TERMW_OUT"); path != "" {
				_ = os.WriteFile(path, []byte(result), 0o600)
			}
			ctx.Notify(result, "info")
			return nil
		},
	})
	return ext
}
`

func writeTermWriteProbe(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "termwprobe")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	mod := fmt.Sprintf("module example.com/termwprobe\n\ngo 1.26\n\nrequire github.com/MichaelKinsy/PiG/extensions/sdk v0.3.0\n\nreplace github.com/MichaelKinsy/PiG/extensions/sdk => %s\n", filepath.Join(fixtureSourceRoot, "extensions", "sdk"))
	for name, content := range map[string]string{"go.mod": mod, "extension.go": termWriteProbeSource} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// termWriteEnv isolates one pig run: every agent-directory variable and HOME point into the test's directories.
func termWriteEnv(home, pigHome, agentDir string, extra ...string) []string {
	return append(append(os.Environ(), "HOME="+home, "PIG_HOME="+pigHome, "PI_HOME="+pigHome, "PIG_CODING_AGENT_DIR="+agentDir, "PI_CODING_AGENT_DIR="+agentDir,
		"PIG_TEST_FAUX=1", "PIG_OFFLINE=1", "PIG_TEST_FAUX_SCENARIO=parity-basic"), extra...)
}

// prebuildTermWriteProbe compiles the probe in print mode, so no interactive wait covers the build.
func prebuildTermWriteProbe(t *testing.T, binary string, env []string, probe string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), binary, "-e", probe, "--no-extensions", "--model", "test-faux/faux-1", "--mode", "text", "-p", "What is 20+22?")
	cmd.Dir = t.TempDir()
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the probe in print mode: %v\n%s", err, out)
	}
}

// The OSC 777 string reaches the terminal unchanged, once, outside every frame the TUI draws (each frame is a synchronized update, ESC [?2026h ... ESC [?2026l), and the TUI keeps drawing: the frame after it shows the command's notice and the next typed text, and nothing of the string is printed on screen.
func TestGoExtensionWriteTerminalReachesTheTerminalBetweenFrames(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the pig binary")
	}
	binary := buildPigBinaryForSignalTest(t)
	probe := writeTermWriteProbe(t)
	home := t.TempDir()
	pigHome, agentDir := filepath.Join(home, ".pig"), filepath.Join(home, "agent")
	seedFirstRunDone(t, agentDir)
	env := termWriteEnv(home, pigHome, agentDir)
	prebuildTermWriteProbe(t, binary, env, probe)

	master, slave := openModePTY(t)
	cmd := exec.Command(binary, "--no-extensions", "-e", probe, "--model", "test-faux/faux-1", "--tui-mode", "regular", "--no-session")
	cmd.Dir = t.TempDir()
	cmd.Env = append(slices.Clone(env), "TERM=xterm-256color")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start pig: %v", err)
	}
	_ = slave.Close()
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	output := &editorProbeOutput{}
	go func() { _, _ = io.Copy(output, master) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
		_ = master.Close()
	})
	tail := func() string {
		all := output.since(0)
		return string(all[max(0, len(all)-3000):])
	}
	await := func(stage string, mark int, marker string) {
		t.Helper()
		deadline := time.Now().Add(testbudget.Wait(t))
		for time.Now().Before(deadline) {
			select {
			case <-exited:
				t.Fatalf("pig exited %s; last output: %q", stage, tail())
			default:
			}
			if bytes.Contains(output.since(mark), []byte(marker)) {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("%s: %q never appeared; last output: %q", stage, marker, tail())
	}
	send := func(text string) int {
		mark := output.mark()
		if _, err := master.WriteString(text); err != nil {
			t.Fatal(err)
		}
		return mark
	}

	await("starting", 0, "\x1b[?2004h")
	// Enter keeps the text while startup runs ("Startup is still in progress"); the extension listing ends it.
	await("loading the probe", 0, "termwprobe")
	mark := send("/termwrite\r")
	await("the command's notice", mark, "termwrite err=<nil>")
	typed := send("after-osc-probe")
	await("typing after the write", typed, "after-osc-probe")

	all := output.since(0)
	at := bytes.Index(all, []byte(termWriteOSC))
	if at < mark {
		t.Fatalf("the OSC 777 string never reached the terminal after the command; last output: %q", tail())
	}
	if n := bytes.Count(all, []byte("777;notify")); n != 1 {
		t.Fatalf("the notification text appears %d times, want once inside the intact string; last output: %q", n, tail())
	}
	before := all[:at]
	if open, closed := bytes.Count(before, []byte("\x1b[?2026h")), bytes.Count(before, []byte("\x1b[?2026l")); open == 0 || open != closed {
		t.Fatalf("the string was written inside a frame (%d frames begun, %d ended before it); last output: %q", open, closed, tail())
	}
	if bytes.HasSuffix(before, []byte("\x1b")) {
		t.Fatalf("the string splits an escape sequence; last output: %q", tail())
	}
	after := all[at+len(termWriteOSC):]
	if !bytes.Contains(after, []byte("\x1b[?2026h")) || !bytes.Contains(after, []byte("after-osc-probe")) {
		t.Fatalf("no frame drew the typed text after the string; last output: %q", tail())
	}
	// The string changes nothing the TUI tracks, so the frames after it stay differential: a clear would mean the renderer lost the screen.
	if bytes.Contains(after, []byte("\x1b[2J")) || bytes.Contains(after, []byte("\x1b[3J")) {
		t.Fatalf("the TUI cleared the screen after the string; last output: %q", tail())
	}
}

// Print, JSON and RPC mode have no terminal UI: the call writes nothing to stdout and returns an error that names the mode and what stdout carries.
func TestGoExtensionWriteTerminalFailsWithoutATerminal(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the pig binary")
	}
	binary := buildPigBinaryForSignalTest(t)
	probe := writeTermWriteProbe(t)
	home := t.TempDir()
	pigHome, agentDir := filepath.Join(home, ".pig"), filepath.Join(home, "agent")
	seedFirstRunDone(t, agentDir)
	prebuildTermWriteProbe(t, binary, termWriteEnv(home, pigHome, agentDir), probe)
	for _, tc := range []struct {
		name  string
		args  []string
		stdin string
		want  string
	}{
		{"print", []string{"-p", "/termwrite"}, "", "pig runs in print mode, which has no terminal UI (stdout carries the printed result)"},
		{"json", []string{"--mode", "json", "-p", "/termwrite"}, "", "pig runs in json mode, which has no terminal UI (stdout carries the JSON event stream)"},
		{"rpc", []string{"--mode", "rpc"}, `{"type":"prompt","message":"/termwrite"}` + "\n", "pig runs in rpc mode, which has no terminal UI (stdout carries the RPC protocol)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "result")
			cmd := exec.CommandContext(t.Context(), binary, append([]string{"--no-extensions", "-e", probe, "--model", "test-faux/faux-1", "--no-session"}, tc.args...)...)
			cmd.Dir = t.TempDir()
			cmd.Env = termWriteEnv(home, pigHome, agentDir, "TERMW_OUT="+out)
			stdinReader, stdinWriter, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stdin = stdinReader
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			_ = stdinReader.Close()
			_, _ = stdinWriter.WriteString(tc.stdin)
			if tc.name != "rpc" {
				_ = stdinWriter.Close()
			}
			exited := make(chan struct{})
			go func() { _ = cmd.Wait(); close(exited) }()
			t.Cleanup(func() { _ = stdinWriter.Close(); _ = cmd.Process.Kill(); <-exited })
			want := "termwrite err=ui.writeTerminal: " + tc.want + "; nothing was written"
			deadline := time.Now().Add(testbudget.Wait(t))
			for {
				if data, err := os.ReadFile(out); err == nil && len(data) > 0 {
					if got := string(data); got != want {
						t.Fatalf("result = %q, want %q", got, want)
					}
					break
				}
				if time.Now().After(deadline) {
					_ = cmd.Process.Kill()
					<-exited
					t.Fatalf("the call never returned; stdout %q stderr %q", stdout.String(), stderr.String())
				}
				time.Sleep(20 * time.Millisecond)
			}
			// RPC mode ends at stdin's end; print and JSON mode end after the command.
			_ = stdinWriter.Close()
			select {
			case <-exited:
			case <-time.After(testbudget.Wait(t)):
				_ = cmd.Process.Kill()
				<-exited
				t.Fatalf("pig did not exit; stderr %q", stderr.String())
			}
			if strings.Contains(stdout.String(), "777;notify") {
				t.Fatalf("stdout carries the string: %q", stdout.String())
			}
		})
	}
}
