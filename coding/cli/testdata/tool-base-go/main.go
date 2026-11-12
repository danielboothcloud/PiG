// Command tool-base-go is a Go-SDK extension that records, for each built-in tool result, the working directory the extension context reports (ctx.Cwd()) and the extension process's own working directory, so a test can compare both with the directory where the built-in write and edit tools resolved a relative path. It appends one JSON line per tool result to TOOL_BASE_REPORT.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/MichaelKinsy/PiG/extensions/sdk"
)

func report(record map[string]any) {
	line, err := json.Marshal(record)
	if err != nil {
		return
	}
	f, err := os.OpenFile(os.Getenv("TOOL_BASE_REPORT"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		_, _ = f.Write(append(line, '\n'))
		_ = f.Close()
	}
}

func main() {
	ext := sdk.New("tool-base-go")
	ext.OnToolResult(func(ctx sdk.Context, data map[string]any) (any, error) {
		processCwd, _ := os.Getwd()
		report(map[string]any{"tool": data["toolName"], "ctxCwd": ctx.Cwd(), "processCwd": processCwd})
		return nil, nil
	})
	if err := ext.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tool-base-go: %v\n", err)
		os.Exit(1)
	}
}
