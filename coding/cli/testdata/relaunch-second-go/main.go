// Command relaunch-second-go is the extension a relaunch adds with -e (D238): it appends a session_start and a session_shutdown line to RELAUNCH_REPORT, so the report shows which runtime loaded it.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/MichaelKinsy/PiG/extensions/sdk"
)

func report(record map[string]any) {
	record["ext"] = "relaunch-second-go"
	record["stage"] = os.Getenv("RELAUNCH_STAGE")
	line, err := json.Marshal(record)
	if err != nil {
		return
	}
	f, err := os.OpenFile(os.Getenv("RELAUNCH_REPORT"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		_, _ = f.Write(append(line, '\n'))
		_ = f.Close()
	}
}

func main() {
	ext := sdk.New("relaunch-second-go")
	ext.OnSessionStart(func(_ sdk.Context, event map[string]any) (any, error) {
		report(map[string]any{"event": "session_start", "reason": event["reason"]})
		return nil, nil
	})
	ext.OnSessionShutdown(func(_ sdk.Context, event map[string]any) (any, error) {
		report(map[string]any{"event": "session_shutdown", "reason": event["reason"]})
		return nil, nil
	})
	if err := ext.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "relaunch-second-go: %v\n", err)
		os.Exit(1)
	}
}
