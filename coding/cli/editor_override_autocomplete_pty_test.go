//go:build linux || darwin

package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/internal/testbudget"
)

// A Go editor component crashed the host with "autocomplete provider is no longer available" once an extension wrapped the autocomplete chain: the rebuild sent the installed editor the new provider (autocomplete.sync operation "changed"), which no delegated SDK editor answers, and the host treats a failed rebuild as Pi's uncaught exception. Pi's CustomEditor subclass inherits setAutocompleteProvider from its base, which is the host's editor here, so the host no longer sends it. The same paths showed two more differences: the request a base key made was dropped when it ran after the base call, and a menu the base opened later never reached the component's frame, where Pi's tui.requestRender renders the subclass again. Pi's CustomEditor lets a subclass override handleInput, render, setText and the action handlers (custom-editor.ts), so every override must survive plain text, bash mode ("!", "!!"), the "/" menu and Tab completion.

const editorOverrideProbeSource = `package edprobe

import (
	"os"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"
)

type embedOnly struct{ *sdk.Editor }

type inputOverride struct{ *sdk.Editor }

func (e *inputOverride) HandleInput(data string) error { return e.Editor.HandleInput(data) }

type renderOverride struct{ *sdk.Editor }

func (e *renderOverride) Render(width int) ([]string, error) { return e.Editor.Render(width) }

type setTextOverride struct{ *sdk.Editor }

func (e *setTextOverride) SetText(text string) error { return e.Editor.SetText(text) }

func Extension() *sdk.Extension {
	ext := sdk.New("edprobe")
	ext.OnSessionStart(func(ctx sdk.Context, _ map[string]any) (any, error) {
		if os.Getenv("ED_WRAP") == "1" {
			if err := ctx.AddAutocompleteProvider(func(_ sdk.Context, current *sdk.AutocompleteProvider) (*sdk.AutocompleteProvider, error) {
				return current, nil
			}); err != nil {
				return nil, err
			}
		}
		return nil, ctx.SetEditorComponent(func(base *sdk.Editor) sdk.EditorComponent {
			switch os.Getenv("ED_MODE") {
			case "input":
				return &inputOverride{base}
			case "render":
				return &renderOverride{base}
			case "settext":
				return &setTextOverride{base}
			case "action":
				base.OnAction("app.model.select", func() error { return base.SetText("ACTION-HOOK") })
				return &inputOverride{base}
			}
			return &embedOnly{base}
		})
	})
	return ext
}
`

type editorProbeOutput struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (o *editorProbeOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.Write(p)
}

func (o *editorProbeOutput) since(mark int) []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	return bytes.Clone(o.buf.Bytes()[mark:])
}

func (o *editorProbeOutput) mark() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.Len()
}

func TestGoEditorComponentOverridesSurviveAutocomplete(t *testing.T) {
	binary := buildPigBinaryForSignalTest(t)
	probe := filepath.Join(t.TempDir(), "edprobe")
	if err := os.MkdirAll(probe, 0o755); err != nil {
		t.Fatal(err)
	}
	mod := fmt.Sprintf("module example.com/edprobe\n\ngo 1.26\n\nrequire github.com/MichaelKinsy/PiG/extensions/sdk v0.3.0\n\nreplace github.com/MichaelKinsy/PiG/extensions/sdk => %s\n", filepath.Join(fixtureSourceRoot, "extensions", "sdk"))
	for name, content := range map[string]string{"go.mod": mod, "extension.go": editorOverrideProbeSource} {
		if err := os.WriteFile(filepath.Join(probe, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pigHome, agentDir := t.TempDir(), t.TempDir()
	// Build the probe once in print mode, so no case's first wait covers a build.
	seedFirstRunDone(t, agentDir)
	build := exec.CommandContext(t.Context(), binary, "-e", probe, "--model", "test-faux/faux-1", "--mode", "text", "-p", "What is 20+22?")
	build.Dir = t.TempDir()
	build.Env = append(os.Environ(), "PIG_HOME="+pigHome, "PIG_CODING_AGENT_DIR="+agentDir, "PI_CODING_AGENT_DIR="+agentDir, "PIG_TEST_FAUX=1", "PIG_OFFLINE=1", "PIG_TEST_FAUX_SCENARIO=parity-basic")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the probe in print mode: %v\n%s", err, out)
	}
	for _, wrap := range []string{"0", "1"} {
		for _, mode := range []string{"noext", "none", "input", "render", "settext", "action"} {
			if mode == "noext" && wrap == "1" {
				continue
			}
			t.Run("wrap="+wrap+"/"+mode, func(t *testing.T) {
				t.Parallel()
				runEditorOverrideProbe(t, binary, pigHome, agentDir, probe, mode, wrap)
			})
		}
	}
}

func runEditorOverrideProbe(t *testing.T, binary, pigHome, agentDir, probe, mode, wrap string) {
	master, slave := openModePTY(t)
	cwd := t.TempDir()
	// A file for Tab path completion.
	if err := os.WriteFile(filepath.Join(cwd, "zebra-probe.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--no-extensions", "-e", probe, "--model", "test-faux/faux-1", "--tui-mode", "regular"}
	if mode == "noext" {
		args = []string{"--no-extensions", "--model", "test-faux/faux-1", "--tui-mode", "regular"}
	}
	cmd := exec.Command(binary, args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "PIG_HOME="+pigHome, "PIG_CODING_AGENT_DIR="+agentDir, "PI_CODING_AGENT_DIR="+agentDir, "HOME="+t.TempDir(), "PIG_TEST_FAUX=1", "PIG_OFFLINE=1",
		"PIG_TEST_FAUX_SCENARIO=parity-basic", "TERM=xterm-256color", "ED_MODE="+mode, "ED_WRAP="+wrap)
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
	budget := testbudget.Wait(t)
	await := func(stage string, mark int, marker string) {
		t.Helper()
		deadline := time.Now().Add(budget)
		for time.Now().Before(deadline) {
			select {
			case <-exited:
				t.Fatalf("pig exited %s; last output: %q", stage, tail())
			default:
			}
			all := output.since(0)
			if bytes.Contains(all, []byte("uncaughtException")) || bytes.Contains(all, []byte("no longer available")) {
				t.Fatalf("pig crashed %s; last output: %q", stage, tail())
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
		for _, r := range text {
			if _, err := master.WriteString(string(r)); err != nil {
				t.Fatal(err)
			}
			time.Sleep(15 * time.Millisecond)
		}
		return mark
	}
	clear := func() {
		send("\x15")
		time.Sleep(200 * time.Millisecond)
	}
	await("starting", 0, "\x1b[?2004h")
	// "noext" runs the stock editor, the control every component must match; "action" also sets an app-action handler.

	mark := send("plain-text-probe")
	await("typing plain text", mark, "plain-text-probe")
	clear()
	mark = send("!ls-probe")
	await("typing a bash command", mark, "ls-probe")
	clear()
	mark = send("!!ls-probe2")
	await("typing an excluded bash command", mark, "ls-probe2")
	clear()
	mark = send("/mod")
	await("opening the slash-command menu", mark, "model")
	send("\x1b")
	clear()
	mark = send("zebra-pr\t")
	await("Tab path completion", mark, "zebra-probe.txt")
	clear()
	if mode == "action" {
		mark = send("\x0c") // Ctrl+L is app.model.select.
		await("the component's action handler", mark, "ACTION-HOOK")
		clear()
	}
	mark = send("after-all-probe")
	await("typing after every probe", mark, "after-all-probe")
	if strings.Contains(tail(), "editor handleInput failed") {
		t.Fatalf("the component's handleInput failed; last output: %q", tail())
	}
}
