package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/internal/testbudget"
)

// Pi runs an extension's handlers inside the extension API call that emits the event, so a handler of the calling extension runs before the call returns: setThinkingLevel runs thinking_level_select with `void runner.emit` (agent-session.js:2132-2151), setModel awaits model_select after setThinkingLevel (agent-session.js:2026-2045), setSessionName runs session_info_changed (agent-session.js:3259-3264), newSession awaits session_before_switch and session_shutdown (agent-session-runtime.js:78-112, 144-170), navigateTree awaits session_before_tree and session_tree (agent-session.js:3322, 3425), reload awaits session_shutdown (agent-session.js:3016-3019), and pi.events emits synchronously (event-bus.js:5-7). A Go extension's host call waits for its answer while the SDK runs the event on another goroutine, so the host answers only after the handler ran. The extension API docs ("Host calls that run your own handlers before they return") tell authors not to hold a lock across these calls. Each fixture handler waits before it records, so a host that answered first would record the call's end before the event.
func TestGoHostCallRunsTheCallersHandlersBeforeItReturns(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the pig binary")
	}
	t.Parallel()
	binary := buildPigBinaryForSignalTest(t)
	fixture := buildGoSDKFixture(t, "host-call-reentry-go")
	for _, tc := range []struct {
		call string
		want []string
	}{
		{"setThinkingLevel high", []string{"event thinking_level_select"}},
		{"setModel test-faux/faux-1", []string{"event thinking_level_select", "event model_select"}},
		{"setSessionName reentry", []string{"event session_info_changed"}},
		{"newSession", []string{"event session_before_switch", "event session_shutdown"}},
		{"navigateTree", []string{"event session_before_tree", "event session_tree"}},
		{"reload", []string{"event session_shutdown"}},
		{"emit", []string{"listener reentry"}},
	} {
		for _, mode := range []string{"print", "json"} {
			t.Run(mode+"/"+tc.call, func(t *testing.T) {
				t.Parallel()
				home := t.TempDir()
				report := filepath.Join(home, "report")
				args := []string{"--offline", "--no-extensions", "--no-skills", "--no-prompt-templates", "-e", fixture, "--model", "openai/gpt-5"}
				if mode == "print" {
					args = append(args, "-p")
				} else {
					args = append(args, "--mode", "json")
				}
				cmd := exec.CommandContext(testbudget.Context(t), binary, append(args, "/probe "+tc.call)...)
				cmd.Dir = t.TempDir()
				cmd.Env = append(os.Environ(), "HOME="+home, "PIG_HOME="+filepath.Join(home, ".pig"), "PI_HOME="+filepath.Join(home, ".pi"),
					"PIG_CODING_AGENT_DIR="+filepath.Join(home, "agent"), "PI_CODING_AGENT_DIR="+filepath.Join(home, "agent"),
					"PIG_TEST_FAUX=1", "OPENAI_API_KEY=test", "REENTRY_REPORT="+report)
				cmd.Stdin = strings.NewReader("")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("pig: %v\n%s", err, out)
				}
				data, err := os.ReadFile(report)
				if err != nil {
					t.Fatalf("the fixture wrote no report: %v", err)
				}
				got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
				want := slices.Concat([]string{"begin"}, tc.want, []string{"end"})
				if !slices.Equal(got, want) {
					t.Fatalf("records %q, want %q", got, want)
				}
			})
		}
	}
}
