package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A Pi extension that guards relative paths of the built-in write and edit tools must know the directory those tools resolve against. Pi's tools resolve against ctx.cwd, the Session's runtime cwd (write.ts and edit.ts `resolveToCwd(path, ctx?.cwd || cwd)`, where the factory cwd is AgentSession's `_cwd`, `sessionManager.getCwd()` in main.ts createRuntime), not against process.cwd(): Pi started in one directory with a Session whose cwd is another writes the relative path under the Session's cwd. A Go extension reads that directory as ctx.Cwd(). The run starts pig in launch with a Session file whose header names session, so the two directories differ and neither is this test process's directory.
func TestBuiltinFileToolsResolveRelativePathsAgainstCtxCwd(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the pig binary")
	}
	t.Parallel()
	binary := buildPigBinaryForSignalTest(t)
	fixture := buildGoSDKFixture(t, "tool-base-go")

	root := t.TempDir()
	launch := canonicalTestDir(t, filepath.Join(root, "launch"))
	session := canonicalTestDir(t, filepath.Join(root, "session"))
	sessionFile := filepath.Join(root, "s.jsonl")
	header, err := json.Marshal(map[string]any{"type": "session", "version": 3, "id": "s1", "timestamp": "2026-01-01T00:00:00.000Z", "cwd": session})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionFile, append(header, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(root, "report.jsonl")
	home := t.TempDir()

	cmd := exec.CommandContext(t.Context(), binary, "-e", fixture, "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-context-files",
		"--model", "test-faux/faux-1", "--session", sessionFile, "-p", "Run: write then edit parity-batched-target.txt")
	cmd.Dir = launch
	cmd.Env = append(os.Environ(), "HOME="+home, "PIG_HOME="+filepath.Join(home, ".pig"), "PI_HOME="+filepath.Join(home, ".pi"),
		"PIG_CODING_AGENT_DIR="+filepath.Join(home, "agent"), "PI_CODING_AGENT_DIR="+filepath.Join(home, "agent"),
		"PIG_TEST_FAUX=1", "PIG_OFFLINE=1", "TOOL_BASE_REPORT="+report)
	cmd.Stdin = strings.NewReader("")
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("pig: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	// The faux model writes parity-batched-target.txt and then edits it, both with that relative path.
	got, err := os.ReadFile(filepath.Join(session, "parity-batched-target.txt"))
	if err != nil || string(got) != "DONE\n" {
		t.Fatalf("session cwd file = %q, %v; want the written and edited %q (stdout %q, stderr %q)", got, err, "DONE\n", stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(launch, "parity-batched-target.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the launch directory has the target (stat err %v); the tools must not resolve against pig's process cwd", err)
	}

	records := readToolBaseReport(t, report)
	var tools []string
	for _, record := range records {
		tools = append(tools, record.Tool)
		if record.CtxCwd != session {
			t.Fatalf("%s result: ctx.Cwd() = %q, want the directory the tool used, %q", record.Tool, record.CtxCwd, session)
		}
		t.Logf("%s result: extension process cwd %q (pig launch directory %q)", record.Tool, record.ProcessCwd, launch)
	}
	slices.Sort(tools)
	if !slices.Equal(tools, []string{"edit", "write"}) {
		t.Fatalf("tool results %q, want edit and write", tools)
	}
}

type toolBaseRecord struct {
	Tool       string `json:"tool"`
	CtxCwd     string `json:"ctxCwd"`
	ProcessCwd string `json:"processCwd"`
}

func readToolBaseReport(t *testing.T, path string) []toolBaseRecord {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("the fixture wrote no report: %v", err)
	}
	defer func() { _ = file.Close() }()
	var records []toolBaseRecord
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record toolBaseRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("report line %q: %v", scanner.Text(), err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return records
}

// canonicalTestDir creates dir and returns its path with symbolic links resolved, as pig resolves a Session's cwd (macOS temporary directories sit behind /var -> /private/var).
func canonicalTestDir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(fmt.Errorf("resolve %s: %w", dir, err))
	}
	return resolved
}
