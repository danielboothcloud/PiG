package subprocess

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/coding/extension"
)

// terminalWriterUI is a UI with a terminal: it records each write.
type terminalWriterUI struct {
	extension.UIContext
	writes []string
}

func (u *terminalWriterUI) WriteTerminal(data string) { u.writes = append(u.writes, data) }

func writeTerminalCall(t *testing.T, data string) *CallPayload {
	t.Helper()
	args, err := json.Marshal(map[string]string{"data": data})
	if err != nil {
		t.Fatal(err)
	}
	return &CallPayload{Method: CallUIWriteTerminal, Args: args}
}

// pig divergence (D237): the strings AIO's notification fallback writes (notifications/backend.ts) pass unchanged: OSC 777, kitty's OSC 99 pair, both inside tmux's passthrough, and BEL.
func TestWriteTerminalPassesNotificationSequences(t *testing.T) {
	osc777 := "\x1b]777;notify;Pi · Finished;Session — Hello World\x07"
	osc99 := "\x1b]99;i=aio:d=0;Pi\x1b\\\x1b]99;i=aio:p=body;Done\x1b\\"
	tmux := func(s string) string { return "\x1bPtmux;" + strings.ReplaceAll(s, "\x1b", "\x1b\x1b") + "\x1b\\" }
	for _, data := range []string{"", "\a", osc777, osc99, tmux(osc777), tmux(osc99), osc777 + "\a", "\x1b]0;title\x1b\\", "\x1b]52;c;aGk=\a", "\x1b]777;notify;Ünïcödé 🎉;b\a"} {
		if err := validateTerminalWrite(data); err != nil {
			t.Errorf("%q: %v", data, err)
		}
	}
}

// A write that prints, moves the cursor or leaves a string open would corrupt the TUI's next frame, so it is refused whole.
func TestWriteTerminalRefusesWhatChangesTheScreen(t *testing.T) {
	for data, want := range map[string]string{
		"hello":                                            "text",
		"\x1b[2J":                                          "CSI",
		"\x1b[1;1H\x1b]777;notify;a;b\a":                   "CSI",
		"\x1b]777;notify;a;b\a\r\n":                        "control byte 0x0d",
		"\x1b]777;notify;a;b":                              "unterminated OSC",
		"\x1b]777;notify;a\nb\a":                           "control byte 0x0a inside an OSC",
		"\x1b]777;notify;a\x1b[31mb\a":                     "ESC inside an OSC",
		"\x1b]777;notify;a\u009db\a":                       "C1 control U+009D",
		"\x1b]8;;https://x\a":                              "OSC 8",
		"\x1bPq#0;2;0;0;0\x1b\\":                           "DCS",
		"\x1bPtmux;\x1b]777;n;a;b\a\x1b\\":                 "single ESC",
		"\x1bPtmux;\x1b\x1b[2J\x1b\\":                      "in the tmux passthrough",
		"\x1bPtmux;\x1b\x1bPtmux;\x1b\x1b\x1b\x1b\\\x1b\\": "in the tmux passthrough",
		"\x1bPtmux;\x1b\x1b]0;t\a":                         "unterminated tmux",
		"\x1b":                                             "lone ESC",
		"\x1b]0;" + strings.Repeat("x", maxTerminalWrite) + "\a": "more than",
	} {
		err := validateTerminalWrite(data)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %v, want one naming %q", data, err, want)
		}
	}
}

// The interactive UI receives a valid write unchanged; an invalid one reaches it not at all.
func TestWriteTerminalReachesTheTerminalUI(t *testing.T) {
	h := &Host{mode: "tui"}
	ui := &terminalWriterUI{UIContext: extension.NoopUIContext}
	h.uiBridge = NewUIBridge(func() {})
	h.uiBridge.SetUIContext(ui)
	osc := "\x1b]777;notify;T;B\a"
	if _, err := h.handleWriteTerminal(writeTerminalCall(t, osc)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.handleWriteTerminal(writeTerminalCall(t, "")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.handleWriteTerminal(writeTerminalCall(t, "plain")); err == nil || !strings.Contains(err.Error(), "nothing was written") {
		t.Fatalf("plain text: %v", err)
	}
	if _, err := h.handleWriteTerminal(&CallPayload{Method: CallUIWriteTerminal, Args: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("a call without data was accepted")
	}
	if !slices.Equal(ui.writes, []string{osc}) {
		t.Fatalf("writes = %q, want only %q", ui.writes, osc)
	}
}

// Print, JSON and RPC mode have no terminal UI: the call writes nothing and the error names the mode and what stdout carries instead.
func TestWriteTerminalFailsWithoutATerminalUI(t *testing.T) {
	for mode, want := range map[string]string{
		"":      "pig runs in print mode, which has no terminal UI (stdout carries the printed result)",
		"print": "pig runs in print mode",
		"json":  "pig runs in json mode, which has no terminal UI (stdout carries the JSON event stream)",
		"rpc":   "pig runs in rpc mode, which has no terminal UI (stdout carries the RPC protocol)",
		"tui":   "the terminal UI is not running yet",
	} {
		for _, bridge := range []bool{false, true} {
			h := &Host{mode: mode}
			if bridge {
				h.uiBridge = NewUIBridge(func() {})
				h.uiBridge.SetUIContext(extension.NoopUIContext)
			}
			_, err := h.handleWriteTerminal(writeTerminalCall(t, "\a"))
			if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "nothing was written") {
				t.Errorf("mode %q bridge %v: error %v, want %q", mode, bridge, err, want)
			}
		}
	}
}
