package subprocess

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/MichaelKinsy/PiG/coding/extension"
)

// pig divergence (D237): a Pi extension writes terminal control strings (OSC 777 and OSC 99 notifications, BEL) to process.stdout, which is the terminal Pi's TUI draws on. A subprocess extension's stdout is its host wire, so CallUIWriteTerminal hands the string to the interactive UI, which writes it between frames. Only strings that leave the screen and cursor as they were pass, so the next frame the TUI draws is correct.

// CallUIWriteTerminal (ext→host) writes terminal control strings to the interactive terminal between frames. The args are WriteTerminalArgs and the result is null. It fails, writing nothing, when the data is not BEL, OSC strings or a tmux passthrough of them, and in print, JSON and RPC mode, which have no terminal UI.
const CallUIWriteTerminal = "ui.writeTerminal"

// WriteTerminalArgs is the argument of CallUIWriteTerminal.
type WriteTerminalArgs struct {
	Data *string `json:"data"`
}

// pig divergence (D237): maxTerminalWrite bounds one write; a notification is a few hundred bytes, and the clipboard has its own call.
const maxTerminalWrite = 64 << 10

// handleWriteTerminal serves CallUIWriteTerminal.
func (h *Host) handleWriteTerminal(call *CallPayload) (*CallResultPayload, error) {
	var args WriteTerminalArgs
	if err := json.Unmarshal(call.Args, &args); err != nil {
		return nil, fmt.Errorf("decode %s: %w", call.Method, err)
	}
	if args.Data == nil {
		return nil, errors.New(CallUIWriteTerminal + " requires data")
	}
	if err := validateTerminalWrite(*args.Data); err != nil {
		return nil, fmt.Errorf("%s: %w; nothing was written", CallUIWriteTerminal, err)
	}
	var writer extension.TerminalWriter
	if h.uiBridge != nil {
		writer = h.uiBridge.terminalWriter()
	}
	if writer == nil {
		return nil, noTerminalError(h.mode)
	}
	if *args.Data != "" {
		writer.WriteTerminal(*args.Data)
	}
	return &CallResultPayload{Result: json.RawMessage("null")}, nil
}

// noTerminalError says why a mode has no terminal to write to.
func noTerminalError(mode string) error {
	var stdout string
	switch mode {
	case "tui":
		return errors.New(CallUIWriteTerminal + ": the terminal UI is not running yet; nothing was written")
	case "rpc":
		stdout = "the RPC protocol"
	case "json":
		stdout = "the JSON event stream"
	default:
		mode, stdout = "print", "the printed result"
	}
	return fmt.Errorf("%s: pig runs in %s mode, which has no terminal UI (stdout carries %s); nothing was written", CallUIWriteTerminal, mode, stdout)
}

// terminalWriter returns the bound UI when it has a terminal, or nil.
func (b *UIBridge) terminalWriter() extension.TerminalWriter {
	b.mu.RLock()
	ui := b.uiCtx
	b.mu.RUnlock()
	writer, _ := ui.(extension.TerminalWriter)
	return writer
}

// validateTerminalWrite accepts a sequence of BEL bytes, OSC strings (ESC ] ... BEL or ESC \) and tmux passthroughs (ESC P tmux; ... ESC \) of those. None of them moves the cursor or prints, so the TUI's model of the screen stays true. An OSC 8 hyperlink is refused because it changes the text drawn after it.
func validateTerminalWrite(data string) error {
	if len(data) > maxTerminalWrite {
		return fmt.Errorf("the data is %d bytes, more than %d", len(data), maxTerminalWrite)
	}
	return validateTerminalStrings(data, true)
}

func validateTerminalStrings(data string, passthrough bool) error {
	for i := 0; i < len(data); {
		rest := data[i:]
		switch {
		case rest[0] == '\a':
			i++
		case strings.HasPrefix(rest, "\x1b]"):
			n, err := oscStringLength(rest)
			if err != nil {
				return fmt.Errorf("byte %d: %w", i, err)
			}
			i += n
		case passthrough && strings.HasPrefix(rest, "\x1bPtmux;"):
			inner, n, err := tmuxPassthrough(rest)
			if err != nil {
				return fmt.Errorf("byte %d: %w", i, err)
			}
			if err := validateTerminalStrings(inner, false); err != nil {
				return fmt.Errorf("byte %d: in the tmux passthrough: %w", i, err)
			}
			i += n
		default:
			return fmt.Errorf("byte %d: %s; only BEL, OSC strings and a tmux passthrough of them can be written", i, describeTerminalBytes(rest))
		}
	}
	return nil
}

// oscStringLength returns the length of the OSC string s starts with, terminator included.
func oscStringLength(s string) (int, error) {
	for j := 2; j < len(s); {
		c := s[j]
		switch {
		case c == '\a':
			return j + 1, checkOSCCommand(s[2:j])
		case c == 0x1b:
			if j+1 < len(s) && s[j+1] == '\\' {
				return j + 2, checkOSCCommand(s[2:j])
			}
			return 0, errors.New("ESC inside an OSC string")
		case c < 0x20 || c == 0x7f:
			return 0, fmt.Errorf("control byte 0x%02x inside an OSC string", c)
		case c < utf8.RuneSelf:
			j++
		default:
			r, size := utf8.DecodeRuneInString(s[j:])
			if r == utf8.RuneError && size == 1 {
				return 0, errors.New("invalid UTF-8 inside an OSC string")
			}
			if r >= 0x80 && r <= 0x9f {
				return 0, fmt.Errorf("C1 control U+%04X inside an OSC string", r)
			}
			j += size
		}
	}
	return 0, errors.New("unterminated OSC string (end it with BEL or ESC \\)")
}

func checkOSCCommand(body string) error {
	if body == "8" || strings.HasPrefix(body, "8;") {
		return errors.New("an OSC 8 hyperlink changes the text drawn after it")
	}
	return nil
}

// tmuxPassthrough returns the unescaped content of the tmux passthrough s starts with, and its length. tmux doubles every ESC of the content.
func tmuxPassthrough(s string) (string, int, error) {
	var inner strings.Builder
	for j := len("\x1bPtmux;"); j < len(s); {
		if s[j] != 0x1b {
			inner.WriteByte(s[j])
			j++
			continue
		}
		if j+1 < len(s) {
			switch s[j+1] {
			case 0x1b:
				inner.WriteByte(0x1b)
				j += 2
				continue
			case '\\':
				return inner.String(), j + 2, nil
			}
		}
		return "", 0, errors.New("a single ESC inside a tmux passthrough (tmux needs ESC doubled)")
	}
	return "", 0, errors.New("unterminated tmux passthrough (end it with ESC \\)")
}

func describeTerminalBytes(s string) string {
	switch {
	case strings.HasPrefix(s, "\x1b["):
		return "a CSI sequence (ESC [), which can move the cursor or change the screen"
	case strings.HasPrefix(s, "\x1bP"):
		return "a DCS string other than a tmux passthrough"
	case s[0] == 0x1b && len(s) > 1:
		return fmt.Sprintf("an ESC %q sequence", s[1])
	case s[0] == 0x1b:
		return "a lone ESC"
	case s[0] < 0x20 || s[0] == 0x7f:
		return fmt.Sprintf("control byte 0x%02x", s[0])
	default:
		return "text, which would print into the TUI's frame"
	}
}
