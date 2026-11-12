//go:build darwin || linux

package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/internal/testbudget"
)

// pig divergence (D238): a Pi extension restarts Pi with another extension set or model scope by spawning a new Pi on the terminal; a Go extension calls ctx.Relaunch and pig replaces its own process image. These tests run the real binary with the relaunch-go fixture, which relaunches with --models and a second extension (relaunch-second-go) and reports, in each runtime, the session, the scope, the launch argv and the pig processes whose command line holds the run's token.

// relaunchRecord is one line of the relaunch fixtures' report.
type relaunchRecord struct {
	Event       string   `json:"event"`
	Ext         string   `json:"ext"`
	Stage       string   `json:"stage"`
	At          string   `json:"at"`
	Reason      string   `json:"reason"`
	Error       string   `json:"error"`
	SessionID   string   `json:"sessionId"`
	SessionFile string   `json:"sessionFile"`
	Mode        string   `json:"mode"`
	Argv        []string `json:"argv"`
	Scoped      []string `json:"scoped"`
	Processes   []string `json:"processes"`
}

func (r relaunchRecord) String() string {
	text := r.Stage + ":" + r.Ext + ":" + r.Event
	if r.Reason != "" {
		text += "(" + r.Reason + ")"
	}
	if r.Error != "" {
		text += ":" + r.Error
	}
	return text
}

// relaunchRig is one isolated pig setup: the binary, both fixtures, the report and the run's token.
type relaunchRig struct {
	binary, first, second string
	report, token, cwd    string
	env                   []string
}

func newRelaunchRig(t *testing.T) *relaunchRig {
	t.Helper()
	home := t.TempDir()
	agentDir := filepath.Join(home, "agent")
	seedFirstRunDone(t, agentDir)
	r := &relaunchRig{
		binary: buildPigBinaryForSignalTest(t),
		first:  buildGoSDKFixture(t, "relaunch-go"),
		second: buildGoSDKFixture(t, "relaunch-second-go"),
		report: filepath.Join(t.TempDir(), "report.jsonl"),
		token:  fmt.Sprintf("relaunch-token-%d-%d", os.Getpid(), time.Now().UnixNano()),
		cwd:    t.TempDir(),
	}
	r.env = append(os.Environ(), "HOME="+home, "PIG_HOME="+filepath.Join(home, ".pig"), "PI_HOME="+filepath.Join(home, ".pi"),
		"PIG_CODING_AGENT_DIR="+agentDir, "PI_CODING_AGENT_DIR="+agentDir, "PIG_TEST_FAUX=1", "PIG_OFFLINE=1",
		"RELAUNCH_REPORT="+r.report, "RELAUNCH_TOKEN="+r.token, "RELAUNCH_STAGE=", "RELAUNCH_AT=", "RELAUNCH_ARGS=")
	return r
}

// base is the arguments every run shares: the faux model, no ambient resources, and the token in the system prompt so that ps finds this run's pig.
func (r *relaunchRig) base() []string {
	return []string{"--model", "test-faux/faux-1", "--no-extensions", "--no-skills", "--no-prompt-templates", "--append-system-prompt", r.token}
}

// relaunchEnv returns the environment of a run whose relaunch-go fixture relaunches at `at` with mode and then message (if any) after the scope, both fixtures and the base arguments.
func (r *relaunchRig) relaunchEnv(t *testing.T, at string, mode []string, message string) []string {
	t.Helper()
	args := append(slices.Clone(mode), "--models", "test-faux/faux-1", "-e", r.first, "-e", r.second)
	args = append(args, r.base()...)
	if message != "" {
		args = append(args, message)
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return append(slices.Clone(r.env), "RELAUNCH_AT="+at, "RELAUNCH_ARGS="+string(encoded))
}

func (r *relaunchRig) records(t *testing.T) []relaunchRecord {
	t.Helper()
	file, err := os.Open(r.report)
	if err != nil {
		t.Fatalf("the fixtures wrote no report: %v", err)
	}
	defer func() { _ = file.Close() }()
	var records []relaunchRecord
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		var record relaunchRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("report line %q: %v", scanner.Text(), err)
		}
		records = append(records, record)
	}
	return records
}

// waitRecord waits until the report holds a record match accepts.
func (r *relaunchRig) waitRecord(t *testing.T, what string, done <-chan struct{}, match func(relaunchRecord) bool) {
	t.Helper()
	deadline := time.Now().Add(testbudget.Wait(t))
	for time.Now().Before(deadline) {
		if _, err := os.Stat(r.report); err == nil && slices.ContainsFunc(r.records(t), match) {
			return
		}
		select {
		case <-done:
			t.Fatalf("pig exited before %s; report %v", what, r.records(t))
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatalf("timed out waiting for %s; report %v", what, r.records(t))
}

// createSession runs one print prompt in the rig's directory without extensions, so that a later run continues a written session; it returns the session's id.
func (r *relaunchRig) createSession(t *testing.T) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), r.binary, append(r.base(), "-p", "reply with exactly: first answer")...)
	cmd.Dir, cmd.Env, cmd.Stdin = r.cwd, r.env, strings.NewReader("")
	if out, err := cmd.CombinedOutput(); err != nil || string(out) != "first answer\n" {
		t.Fatalf("creating the session: %v\n%s", err, out)
	}
}

// sessionContent returns the ids of the session headers and the user message texts of the session file, in order.
func sessionContent(t *testing.T, path string) (headers, users []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		var entry struct {
			Type    string `json:"type"`
			ID      string `json:"id"`
			Message struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("session line %q: %v", line, err)
		}
		switch {
		case entry.Type == "session":
			headers = append(headers, entry.ID)
		case entry.Type == "message" && entry.Message.Role == "user":
			content, _ := json.Marshal(entry.Message.Content)
			users = append(users, string(content))
		}
	}
	return headers, users
}

// assertRelaunched checks the report of a run that relaunched once: the first runtime ran without the second extension or a scope and shut down with reason quit after an accepted relaunch; the second runtime has the scope and both extensions, continues the same session file and session id, and runs in the same and only pig process, whose command line is now the relaunch's.
func (r *relaunchRig) assertRelaunched(t *testing.T, records []relaunchRecord, pid int, mode string) (sessionFile string) {
	t.Helper()
	var first, second []relaunchRecord
	for _, record := range records {
		if record.Stage == "2" {
			second = append(second, record)
		} else {
			first = append(first, record)
		}
	}
	find := func(records []relaunchRecord, ext, event string) relaunchRecord {
		t.Helper()
		i := slices.IndexFunc(records, func(record relaunchRecord) bool { return record.Ext == ext && record.Event == event })
		if i < 0 {
			t.Fatalf("no %s %s in %v", ext, event, records)
		}
		return records[i]
	}
	before, after := find(first, "relaunch-go", "session_start"), find(second, "relaunch-go", "session_start")
	if relaunch := find(first, "relaunch-go", "relaunch"); relaunch.Error != "" {
		t.Fatalf("relaunch refused: %s", relaunch.Error)
	}
	if shutdown := find(first, "relaunch-go", "session_shutdown"); shutdown.Reason != "quit" {
		t.Fatalf("first runtime's session_shutdown reason %q, want quit", shutdown.Reason)
	}
	if slices.ContainsFunc(first, func(record relaunchRecord) bool { return record.Ext == "relaunch-second-go" }) {
		t.Fatalf("the first runtime loaded the second extension: %v", first)
	}
	find(second, "relaunch-second-go", "session_start")
	if len(before.Scoped) != 0 || !slices.Equal(after.Scoped, []string{"test-faux/faux-1"}) {
		t.Fatalf("scoped models %q then %q, want none then the relaunch's --models", before.Scoped, after.Scoped)
	}
	if after.At != "" || after.Mode != mode || before.Mode != mode {
		t.Fatalf("second runtime at=%q (RELAUNCH_AT must be removed), modes %q/%q, want %q", after.At, before.Mode, after.Mode, mode)
	}
	if before.SessionID == "" || after.SessionID != before.SessionID || after.SessionFile != before.SessionFile {
		t.Fatalf("session %s %s then %s %s, want the same session", before.SessionID, before.SessionFile, after.SessionID, after.SessionFile)
	}
	if !slices.Contains(after.Argv, "--models") || !slices.Contains(after.Argv, r.second) {
		t.Fatalf("second runtime argv %q, want the relaunch's arguments", after.Argv)
	}
	want := strconv.Itoa(pid) + " "
	for name, record := range map[string]relaunchRecord{"first": before, "second": after} {
		if len(record.Processes) != 1 || !strings.HasPrefix(record.Processes[0], want) {
			t.Fatalf("%s runtime's pig processes %q, want only pid %d", name, record.Processes, pid)
		}
	}
	if !strings.Contains(after.Processes[0], "--models test-faux/faux-1") || strings.Contains(before.Processes[0], "--models") {
		t.Fatalf("pid %d ran %q then %q, want the relaunch's command line in the same process", pid, before.Processes[0], after.Processes[0])
	}
	return after.SessionFile
}

// assertNoPigLeft fails when a process with the run's token survives the run.
func (r *relaunchRig) assertNoPigLeft(t *testing.T) {
	t.Helper()
	out, err := exec.Command("ps", "-A", "-o", "pid=,args=").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), r.token) {
		t.Fatalf("a pig process of this run survived:\n%s", out)
	}
}

// In print and JSON mode the fixture relaunches at agent_end of the first prompt: the session file is written by then, so pig adds --session for it. The first runtime writes no result (as for shutdownWithCode); the relaunched runtime answers the relaunch's prompt into the same session, and the process exits 0.
func TestRelaunchRestartsPrintAndJSONModeInTheSameProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the pig binary")
	}
	t.Parallel()
	for _, mode := range []string{"print", "json"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			r := newRelaunchRig(t)
			modeArgs := []string{"-p"}
			if mode == "json" {
				modeArgs = []string{"--mode", "json"}
			}
			args := append(append(slices.Clone(modeArgs), "-e", r.first), r.base()...)
			cmd := exec.CommandContext(t.Context(), r.binary, append(args, "reply with exactly: first answer")...)
			cmd.Dir, cmd.Env, cmd.Stdin = r.cwd, r.relaunchEnv(t, "agent_end", modeArgs, "reply with exactly: second answer"), strings.NewReader("")
			var stdout, stderr strings.Builder
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("pig: %v\nstderr:\n%s", err, stderr.String())
			}
			records := r.records(t)
			file := r.assertRelaunched(t, records, cmd.Process.Pid, mode)
			if mode == "print" && stdout.String() != "second answer\n" {
				t.Fatalf("stdout %q, want only the relaunched runtime's answer", stdout.String())
			}
			if mode == "json" {
				ends := 0
				for line := range strings.SplitSeq(strings.TrimSpace(stdout.String()), "\n") {
					var event struct{ Type string }
					if err := json.Unmarshal([]byte(line), &event); err != nil {
						t.Fatalf("stdout line %q is not JSON: %v", line, err)
					}
					if event.Type == "agent_end" {
						ends++
					}
				}
				if ends != 2 || !strings.Contains(stdout.String(), "second answer") {
					t.Fatalf("stdout has %d agent_end events, want one per runtime:\n%s", ends, stdout.String())
				}
			}
			headers, users := sessionContent(t, file)
			if len(headers) != 1 || headers[0] != records[0].SessionID || len(users) != 2 || !strings.Contains(users[0], "first answer") || !strings.Contains(users[1], "second answer") {
				t.Fatalf("session headers %q users %q, want one session with both prompts in order", headers, users)
			}
			r.assertNoPigLeft(t)
		})
	}
}

// In RPC mode the fixture relaunches in session_start, before pig reads a command. The client writes its prompt at once; the first pig never reads it, and the relaunched pig, which continues the --continue session through the --session pig added, answers it.
func TestRelaunchRestartsRPCModeBeforeItReadsCommands(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the pig binary")
	}
	t.Parallel()
	r := newRelaunchRig(t)
	r.createSession(t)
	cmd := exec.CommandContext(t.Context(), r.binary, append([]string{"--mode", "rpc", "--continue", "-e", r.first}, r.base()...)...)
	cmd.Dir, cmd.Env = r.cwd, r.relaunchEnv(t, "session_start", []string{"--mode", "rpc"}, "")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var output strings.Builder
	var outputMu sync.Mutex
	go func() {
		defer close(done)
		data, _ := io.ReadAll(stdout)
		outputMu.Lock()
		output.Write(data)
		outputMu.Unlock()
	}()
	if _, err := io.WriteString(stdin, `{"id":"p1","type":"prompt","message":"reply with exactly: second answer"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	r.waitRecord(t, "the relaunched runtime's answer", done, func(record relaunchRecord) bool { return record.Stage == "2" && record.Event == "agent_end" })
	_ = stdin.Close()
	<-done
	if err := cmd.Wait(); err != nil {
		t.Fatalf("pig: %v\nstderr:\n%s", err, stderr.String())
	}
	records := r.records(t)
	file := r.assertRelaunched(t, records, cmd.Process.Pid, "rpc")
	outputMu.Lock()
	text := output.String()
	outputMu.Unlock()
	if !strings.Contains(text, `"id":"p1"`) || !strings.Contains(text, "second answer") {
		t.Fatalf("rpc output lacks the prompt's response:\n%s", text)
	}
	headers, users := sessionContent(t, file)
	if len(headers) != 1 || headers[0] != records[0].SessionID || len(users) != 2 || !strings.Contains(users[0], "first answer") || !strings.Contains(users[1], "second answer") {
		t.Fatalf("session headers %q users %q, want the continued session with both prompts", headers, users)
	}
	r.assertNoPigLeft(t)
}

// A relaunch refused by the mode returns the host's reason to the extension, and the runtime goes on as if nothing was asked: print mode after it read piped stdin answers that prompt, and RPC mode, once it reads commands, answers the next one. Neither runtime is replaced.
func TestRelaunchRefusalKeepsTheRuntime(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the pig binary")
	}
	t.Parallel()
	t.Run("print after piped stdin", func(t *testing.T) {
		t.Parallel()
		r := newRelaunchRig(t)
		cmd := exec.CommandContext(t.Context(), r.binary, append([]string{"-p", "-e", r.first}, r.base()...)...)
		cmd.Dir, cmd.Env, cmd.Stdin = r.cwd, r.relaunchEnv(t, "session_start", []string{"-p"}, ""), strings.NewReader("reply with exactly: piped answer")
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil || string(out) != "piped answer\n" {
			t.Fatalf("pig: %v, stdout %q\nstderr:\n%s", err, out, stderr.String())
		}
		r.assertRefused(t, "pig already read its piped stdin, which a relaunched pig could not read again")
	})
	t.Run("rpc after the reader attached", func(t *testing.T) {
		t.Parallel()
		r := newRelaunchRig(t)
		cmd := exec.CommandContext(t.Context(), r.binary, append([]string{"--mode", "rpc", "-e", r.first}, r.base()...)...)
		cmd.Dir, cmd.Env = r.cwd, r.relaunchEnv(t, "", []string{"--mode", "rpc"}, "")
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		var waitErr error
		go func() { waitErr = cmd.Wait(); close(done) }()
		if _, err := io.WriteString(stdin, `{"id":"c1","type":"prompt","message":"/relaunch-now"}`+"\n"); err != nil {
			t.Fatal(err)
		}
		r.waitRecord(t, "the refused relaunch", done, func(record relaunchRecord) bool { return record.Event == "relaunch" })
		if _, err := io.WriteString(stdin, `{"id":"p2","type":"prompt","message":"reply with exactly: still here"}`+"\n"); err != nil {
			t.Fatal(err)
		}
		r.waitRecord(t, "the next prompt's answer", done, func(record relaunchRecord) bool { return record.Event == "agent_end" })
		_ = stdin.Close()
		<-done
		if waitErr != nil {
			t.Fatalf("pig: %v\nstderr:\n%s", waitErr, stderr.String())
		}
		if !strings.Contains(stdout.String(), "still here") {
			t.Fatalf("rpc output lacks the answer after the refusal:\n%s", stdout.String())
		}
		r.assertRefused(t, "RPC mode already reads commands from stdin")
	})
}

// assertRefused checks that the fixture's relaunch returned an error holding want, the runtime then ran its prompt, and no second runtime started.
func (r *relaunchRig) assertRefused(t *testing.T, want string) {
	t.Helper()
	records := r.records(t)
	i := slices.IndexFunc(records, func(record relaunchRecord) bool { return record.Event == "relaunch" })
	if i < 0 || !strings.Contains(records[i].Error, want) {
		t.Fatalf("report %v, want a relaunch refused with %q", records, want)
	}
	if !slices.ContainsFunc(records[i:], func(record relaunchRecord) bool { return record.Event == "agent_end" }) {
		t.Fatalf("report %v, want the runtime to answer after the refusal", records)
	}
	if slices.ContainsFunc(records, func(record relaunchRecord) bool { return record.Stage == "2" || record.Ext == "relaunch-second-go" }) {
		t.Fatalf("report %v, want no relaunched runtime", records)
	}
	r.assertNoPigLeft(t)
}
