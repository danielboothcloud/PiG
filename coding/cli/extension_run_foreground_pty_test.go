//go:build linux

package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/internal/testbudget"
)

// D19: a Go extension hands the terminal to a foreground program with ctx.RunForeground, as a Pi extension does with ctx.ui.custom, tui.stop(), spawnSync(stdio: "inherit") and tui.start() (pi-tuicr runner.ts). These tests run the real binary in a pseudo-terminal with a real Go extension.

const foregroundProbeSource = `package fgprobe

import (
	"fmt"
	"os"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"
)

func Extension() *sdk.Extension {
	ext := sdk.New("fgprobe")
	ext.RegisterCommand("fg", sdk.CommandOptions{
		Description: "run a foreground program",
		Handler: func(ctx sdk.Context, _ string) error {
			code, err := ctx.RunForeground("sh", []string{"-c", "printf FG-PROGRAM-x; read a"}, sdk.ForegroundOptions{})
			result := fmt.Sprintf("fg-exit=%d err=%v", code, err)
			if path := os.Getenv("FG_OUT"); path != "" {
				_ = os.WriteFile(path, []byte(result), 0o600)
			}
			ctx.Notify(result, "info")
			return nil
		},
	})
	return ext
}
`

func writeForegroundProbe(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "fgprobe")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	mod := fmt.Sprintf("module example.com/fgprobe\n\ngo 1.26\n\nrequire github.com/MichaelKinsy/PiG/extensions/sdk v0.3.0\n\nreplace github.com/MichaelKinsy/PiG/extensions/sdk => %s\n", filepath.Join(fixtureSourceRoot, "extensions", "sdk"))
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "extension.go"), []byte(foregroundProbeSource), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestExtensionRunForegroundHandsOverTheTerminal runs the program to completion and then interrupts it with Ctrl+C. Each time the TUI must stop (bracketed paste off), the program must own the terminal, and the TUI must come back with a full redraw and an editor that takes input.
func TestExtensionRunForegroundHandsOverTheTerminal(t *testing.T) {
	binary := buildPigBinaryForSignalTest(t)
	probe := writeForegroundProbe(t)
	pigHome, agentDir := t.TempDir(), t.TempDir()
	prebuildExtension(t, binary, pigHome, agentDir, probe)

	s := startSpriteSession(t, binary, pigHome, agentDir, "-e", probe, "--tui-mode", "regular")
	s.await("starting", 0, "\x1b[?2004h")

	handOver := func(stage string) int {
		t.Helper()
		mark := s.send("/fg\r")
		s.await(stage+": stopping the TUI", mark, "\x1b[?2004l")
		s.await(stage+": running the program", mark, "FG-PROGRAM-x")
		return s.output.mark()
	}
	takeBack := func(stage string, mark int, want string) {
		t.Helper()
		s.await(stage+": restarting the TUI", mark, "\x1b[?2004h")
		s.await(stage+": reporting the exit", mark, want)
		// The full redraw repaints the header the first frame drew.
		if !bytes.Contains(s.output.since(mark), []byte("\x1b[2J")) && !bytes.Contains(s.output.since(mark), []byte("\x1b[3J")) {
			t.Fatalf("%s: no full redraw after the program exited; last output: %q", stage, s.tail())
		}
		typed := s.send("typed-after-" + strings.ReplaceAll(stage, " ", "-"))
		s.await(stage+": the editor takes input", typed, "typed-after-"+strings.ReplaceAll(stage, " ", "-"))
		s.send("\x15") // Ctrl+U clears the editor line.
	}

	mark := handOver("exit")
	s.send("answer\r")
	takeBack("exit", mark, "fg-exit=0 err=<nil>")

	mark = handOver("ctrl-c")
	s.send("\x03")
	takeBack("ctrl-c", mark, "fg-exit=-1 err=<nil>")
	select {
	case <-s.exited:
		t.Fatalf("Ctrl+C in the foreground program ended the session; last output: %q", s.tail())
	default:
	}
}

// TestExtensionRunForegroundFailsWithoutATerminal proves print, JSON and RPC modes answer the call with an error at once.
func TestExtensionRunForegroundFailsWithoutATerminal(t *testing.T) {
	binary := buildPigBinaryForSignalTest(t)
	probe := writeForegroundProbe(t)
	pigHome, agentDir := t.TempDir(), t.TempDir()
	prebuildExtension(t, binary, pigHome, agentDir, probe)
	const want = "fg-exit=-1 err=runForeground requires the interactive terminal UI"
	for _, tc := range []struct {
		name  string
		args  []string
		stdin string
	}{
		{"print", []string{"-p", "/fg"}, ""},
		{"json", []string{"--mode", "json", "-p", "/fg"}, ""},
		{"rpc", []string{"--mode", "rpc"}, `{"type":"prompt","message":"/fg"}` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "result")
			cmd := exec.CommandContext(t.Context(), binary, append([]string{"-e", probe, "--model", "test-faux/faux-1", "--no-session"}, tc.args...)...)
			cmd.Dir = t.TempDir()
			cmd.Env = append(os.Environ(), "PIG_HOME="+pigHome, "PIG_CODING_AGENT_DIR="+agentDir, "PIG_TEST_FAUX=1", "PIG_OFFLINE=1", "PIG_TEST_FAUX_SCENARIO=parity-basic", "FG_OUT="+out)
			stdinReader, stdinWriter, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stdin = stdinReader
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			_ = stdinReader.Close()
			_, _ = stdinWriter.WriteString(tc.stdin)
			t.Cleanup(func() { _ = stdinWriter.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
			if tc.name != "rpc" {
				_ = stdinWriter.Close()
			}
			deadline := time.Now().Add(testbudget.Wait(t))
			for time.Now().Before(deadline) {
				if data, err := os.ReadFile(out); err == nil && len(data) > 0 {
					if got := string(data); got != want {
						t.Fatalf("result = %q, want %q", got, want)
					}
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Fatalf("the call never returned; output: %s", output.String())
		})
	}
}
