package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/coding/extension/host/subprocess"
)

// The relaunched pig continues the session (D238): pig puts --session <file> before the requested arguments when they choose no session and the file is written. Arguments that choose a session themselves keep their choice, and a session with no file on disk is not carried.
func TestRelaunchArgsCarryTheWrittenSession(t *testing.T) {
	written := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(written, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "not-yet.jsonl")
	for _, tc := range []struct {
		args    []string
		session string
		want    []string
	}{
		{[]string{"--models", "x/y", "hi"}, written, []string{"--session", written, "--models", "x/y", "hi"}},
		{[]string{}, written, []string{"--session", written}},
		{[]string{"--", "--session", "x"}, written, []string{"--session", written, "--", "--session", "x"}},
		{[]string{"--models", "x/y"}, missing, []string{"--models", "x/y"}},
		{[]string{"--models", "x/y"}, "", []string{"--models", "x/y"}},
		{[]string{"--session", "other.jsonl"}, written, []string{"--session", "other.jsonl"}},
		{[]string{"--session-id", "abc"}, written, []string{"--session-id", "abc"}},
		{[]string{"-c"}, written, []string{"-c"}},
		{[]string{"--continue"}, written, []string{"--continue"}},
		{[]string{"-r"}, written, []string{"-r"}},
		{[]string{"--fork", "other.jsonl"}, written, []string{"--fork", "other.jsonl"}},
		{[]string{"--no-session"}, written, []string{"--no-session"}},
	} {
		got := relaunchArgs(tc.args, tc.session)
		if !slices.Equal(got, tc.want) {
			t.Errorf("relaunchArgs(%q, %q) = %q, want %q", tc.args, tc.session, got, tc.want)
		}
	}
}

// The relaunched pig's environment is this process's with the request's changes: a set variable replaces the old entry, a nil one removes it, and other entries keep their order.
func TestRelaunchEnvironAppliesTheChanges(t *testing.T) {
	one, two := "1", "x=y"
	got := relaunchEnviron([]string{"A=a", "GONE=g", "KEEP=k", "SET=old"}, map[string]*string{"GONE": nil, "SET": &one, "NEW": &two, "ABSENT": nil})
	if want := []string{"A=a", "KEEP=k", "NEW=x=y", "SET=1"}; !slices.Equal(got, want) {
		t.Fatalf("relaunchEnviron = %q, want %q", got, want)
	}
}

// acceptRelaunch refuses where a relaunched pig would lose input or a second relaunch would race the first, and leaves no plan behind.
func TestAcceptRelaunchRefusals(t *testing.T) {
	defer pendingRelaunch.Store(nil)
	defer pipedStdinConsumed.Store(false)
	if runtime.GOOS == "windows" {
		if err := acceptRelaunch(subprocess.RelaunchRequest{}); err == nil || !strings.Contains(err.Error(), "Windows cannot replace a running process") {
			t.Fatalf("acceptRelaunch on Windows = %v", err)
		}
		return
	}
	pipedStdinConsumed.Store(true)
	if err := acceptRelaunch(subprocess.RelaunchRequest{}); err == nil || !strings.Contains(err.Error(), "already read its piped stdin") {
		t.Fatalf("acceptRelaunch after piped stdin = %v", err)
	}
	if relaunchPending() {
		t.Fatal("a refused relaunch left a plan")
	}
	pipedStdinConsumed.Store(false)
	pendingRelaunch.Store(&relaunchPlan{path: "/pig"})
	if err := acceptRelaunch(subprocess.RelaunchRequest{}); err == nil || !strings.Contains(err.Error(), "a relaunch is already pending") {
		t.Fatalf("acceptRelaunch with a pending relaunch = %v", err)
	}
	if pendingRelaunch.Load().path != "/pig" {
		t.Fatal("a second relaunch replaced the first")
	}
}
