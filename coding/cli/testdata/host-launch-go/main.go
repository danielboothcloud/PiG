// Command host-launch-go is a Go-SDK extension that reports how pig was started (ctx.HostLaunch) when the session starts, as a Pi extension reads process.argv and process.stdin.isTTY, and with HOST_LAUNCH_EXIT set ends startup with ctx.ShutdownWithCode, as a Pi extension calls process.exit(code) (D236). It appends one JSON line per observation to HOST_LAUNCH_REPORT: the launch, the shutdown call's return, before_agent_start (a prompt reached the agent) and session_shutdown.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/MichaelKinsy/PiG/extensions/sdk"
)

func report(record map[string]any) {
	line, err := json.Marshal(record)
	if err != nil {
		return
	}
	f, err := os.OpenFile(os.Getenv("HOST_LAUNCH_REPORT"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		_, _ = f.Write(append(line, '\n'))
		_ = f.Close()
	}
}

func main() {
	ext := sdk.New("host-launch-go")
	ext.OnSessionStart(func(ctx sdk.Context, _ map[string]any) (any, error) {
		launch, err := ctx.HostLaunch()
		if err != nil {
			report(map[string]any{"event": "hostLaunch", "error": err.Error()})
			return nil, nil
		}
		report(map[string]any{"event": "hostLaunch", "argv": launch.Argv, "mode": launch.Mode, "contextMode": ctx.Mode(), "stdinIsTTY": launch.StdinIsTTY, "stdoutIsTTY": launch.StdoutIsTTY})
		if value := os.Getenv("HOST_LAUNCH_EXIT"); value != "" {
			code, err := strconv.Atoi(value)
			if err != nil {
				return nil, err
			}
			if err := ctx.ShutdownWithCode(code); err != nil {
				report(map[string]any{"event": "shutdownWithCode", "error": err.Error()})
				return nil, nil
			}
			report(map[string]any{"event": "shutdownWithCode"})
		}
		return nil, nil
	})
	ext.OnEvent("before_agent_start", func(sdk.Context, map[string]any) (any, error) {
		report(map[string]any{"event": "before_agent_start"})
		return nil, nil
	})
	ext.OnSessionShutdown(func(sdk.Context, map[string]any) (any, error) {
		report(map[string]any{"event": "session_shutdown"})
		return nil, nil
	})
	if err := ext.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "host-launch-go: %v\n", err)
		os.Exit(1)
	}
}
