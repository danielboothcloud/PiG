// Command host-call-reentry-go is a Go-SDK extension whose /probe command makes one host call and records which of its own event handlers ran before the call returned. Each handler waits before it records, so a host that answered the call before the handler ran records the call's end first. It appends one line per record to REENTRY_REPORT: "begin", "event <name>" for a handler that ran while the call was in flight, and "end".
package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MichaelKinsy/PiG/extensions/sdk"
)

var (
	reportMu sync.Mutex
	inCall   atomic.Bool
)

func report(line string) {
	reportMu.Lock()
	defer reportMu.Unlock()
	f, err := os.OpenFile(os.Getenv("REENTRY_REPORT"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.WriteString(line + "\n")
	_ = f.Close()
}

var events = []string{"thinking_level_select", "model_select", "session_info_changed", "session_before_switch", "session_before_tree", "session_tree", "session_shutdown"}

func main() {
	ext := sdk.New("host-call-reentry-go")
	for _, name := range events {
		ext.OnEvent(name, func(sdk.Context, map[string]any) (any, error) {
			if inCall.Load() {
				time.Sleep(200 * time.Millisecond)
				report("event " + name)
			}
			return nil, nil
		})
	}
	if _, err := ext.Events().On("reentry", func(sdk.Context, any) error {
		if inCall.Load() {
			time.Sleep(200 * time.Millisecond)
			report("listener reentry")
		}
		return nil
	}); err != nil {
		fmt.Fprintf(os.Stderr, "host-call-reentry-go: %v\n", err)
		os.Exit(1)
	}
	ext.RegisterCommand("probe", sdk.CommandOptions{Handler: func(ctx sdk.Context, args string) error {
		call, arg, _ := strings.Cut(args, " ")
		var target string
		if call == "navigateTree" {
			if err := ctx.AppendEntry("reentry", map[string]any{"n": 1}); err != nil {
				return err
			}
			leaf, err := ctx.GetLeafID()
			if err != nil || leaf == nil {
				return fmt.Errorf("leaf: %v", err)
			}
			target = *leaf
			if err := ctx.AppendEntry("reentry", map[string]any{"n": 2}); err != nil {
				return err
			}
		}
		report("begin")
		inCall.Store(true)
		var err error
		switch call {
		case "setThinkingLevel":
			ctx.SetThinkingLevel(arg)
		case "setModel":
			_, err = ctx.SetModel(arg)
		case "setSessionName":
			err = ctx.SetSessionName(arg)
		case "newSession":
			_, err = ctx.NewSession(nil)
		case "navigateTree":
			_, err = ctx.NavigateTree(target, nil)
		case "reload":
			err = ctx.Reload()
		case "emit":
			err = ctx.Events().Emit("reentry", map[string]any{})
		default:
			err = fmt.Errorf("unknown call %q", call)
		}
		inCall.Store(false)
		if err != nil {
			report("error " + err.Error())
		}
		report("end")
		return nil
	}})
	if err := ext.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "host-call-reentry-go: %v\n", err)
		os.Exit(1)
	}
}
