// Command relaunch-go is a Go-SDK extension that restarts pig with ctx.Relaunch (D238), as a Pi extension restarts Pi by spawning a new Pi on the terminal. RELAUNCH_AT chooses when: session_start, agent_end, or the /relaunch-now command. RELAUNCH_ARGS is the JSON argument list it passes; the environment change sets RELAUNCH_STAGE=2 and removes RELAUNCH_AT, so the relaunched runtime does not relaunch again. It appends one JSON line per observation to RELAUNCH_REPORT, each with the pig processes whose command line holds RELAUNCH_TOKEN.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/MichaelKinsy/PiG/extensions/sdk"
)

func report(record map[string]any) {
	record["ext"] = "relaunch-go"
	record["stage"] = os.Getenv("RELAUNCH_STAGE")
	record["at"] = os.Getenv("RELAUNCH_AT")
	record["ppid"] = os.Getppid()
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

// pigProcesses lists "pid args" of every process whose command line holds RELAUNCH_TOKEN.
func pigProcesses() []string {
	token := os.Getenv("RELAUNCH_TOKEN")
	out, err := exec.Command("ps", "-A", "-o", "pid=,args=").Output()
	if err != nil || token == "" {
		return []string{"ps: " + fmt.Sprint(err)}
	}
	var lines []string
	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.Contains(line, token) {
			lines = append(lines, strings.TrimSpace(line))
		}
	}
	return lines
}

func relaunch(ctx sdk.Context) {
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("RELAUNCH_ARGS")), &args); err != nil {
		report(map[string]any{"event": "relaunch", "error": "RELAUNCH_ARGS: " + err.Error()})
		return
	}
	stage := "2"
	err := ctx.Relaunch(sdk.RelaunchOptions{Args: args, Env: map[string]*string{"RELAUNCH_STAGE": &stage, "RELAUNCH_AT": nil}})
	record := map[string]any{"event": "relaunch"}
	if err != nil {
		record["error"] = err.Error()
	}
	report(record)
}

func main() {
	ext := sdk.New("relaunch-go")
	ext.OnSessionStart(func(ctx sdk.Context, event map[string]any) (any, error) {
		record := map[string]any{"event": "session_start", "reason": event["reason"], "processes": pigProcesses()}
		if id, err := ctx.GetSessionID(); err == nil {
			record["sessionId"] = id
		}
		if file, err := ctx.GetSessionFile(); err == nil && file != nil {
			record["sessionFile"] = *file
		}
		if launch, err := ctx.HostLaunch(); err == nil {
			record["argv"] = launch.Argv
			record["mode"] = launch.Mode
		}
		if models, err := ctx.ScopedModels(); err == nil {
			scoped := []string{}
			for _, model := range models {
				scoped = append(scoped, fmt.Sprintf("%v/%v", model.Model["provider"], model.Model["id"]))
			}
			record["scoped"] = scoped
		}
		report(record)
		if os.Getenv("RELAUNCH_AT") == "session_start" {
			relaunch(ctx)
		}
		return nil, nil
	})
	ext.OnEvent("before_agent_start", func(sdk.Context, map[string]any) (any, error) {
		report(map[string]any{"event": "before_agent_start"})
		return nil, nil
	})
	ext.OnEvent("agent_end", func(ctx sdk.Context, _ map[string]any) (any, error) {
		report(map[string]any{"event": "agent_end"})
		if os.Getenv("RELAUNCH_AT") == "agent_end" {
			relaunch(ctx)
		}
		return nil, nil
	})
	ext.Command("relaunch-now", "Relaunch pig with RELAUNCH_ARGS", func(ctx sdk.Context, _ string) error {
		relaunch(ctx)
		return nil
	})
	ext.OnSessionShutdown(func(_ sdk.Context, event map[string]any) (any, error) {
		report(map[string]any{"event": "session_shutdown", "reason": event["reason"]})
		return nil, nil
	})
	if err := ext.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "relaunch-go: %v\n", err)
		os.Exit(1)
	}
}
