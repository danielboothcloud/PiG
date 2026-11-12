package subprocess

import (
	"encoding/json"
	"strings"
	"testing"
)

// CallShutdownWithCode carries Pi's process.exit(code) status (D236): the host passes a code in 0..255 to the mode and rejects a missing or out-of-range code, and a host whose mode binds no handler fails the call instead of ignoring the status.
func TestShutdownWithCodeCallValidatesTheCode(t *testing.T) {
	var got []int
	actions := &HostCallbacks{ShutdownWithCode: func(code int) { got = append(got, code) }}
	b := NewUIBridge(func() {})
	for _, tc := range []struct {
		args    string
		actions *HostCallbacks
		wantErr string
	}{
		{`{"code":0}`, actions, ""},
		{`{"code":255}`, actions, ""},
		{`{"code":256}`, actions, "exit code 256 is outside 0..255"},
		{`{"code":-1}`, actions, "exit code -1 is outside 0..255"},
		{`{}`, actions, "requires code"},
		{`{"code":1}`, &HostCallbacks{}, "not available in this host"},
	} {
		_, err := b.handleShutdownWithCode(tc.actions, json.RawMessage(tc.args))
		if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Fatalf("%s: error %v, want %q", tc.args, err, tc.wantErr)
		}
	}
	if len(got) != 2 || got[0] != 0 || got[1] != 255 {
		t.Fatalf("mode received %v, want [0 255]", got)
	}
}

// CallHostLaunch returns the host's argv after the executable, its run mode (an unset mode reads "print", as the SDKs default ctx.mode), and the launch terminal state, and the reply is a copy the caller cannot change.
func TestHostLaunchCallReportsTheLaunch(t *testing.T) {
	h := &Host{harnessArgs: []string{"--no-approve", "--models", "a,b"}, launchTerminals: LaunchTerminals{Stdin: true}}
	result, err := h.handleHostLaunch()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(result.Result), `{"argv":["--no-approve","--models","a,b"],"mode":"print","stdinIsTTY":true,"stdoutIsTTY":false}`; got != want {
		t.Fatalf("result %s, want %s", got, want)
	}
	h = &Host{mode: "rpc"}
	if result, err = h.handleHostLaunch(); err != nil || string(result.Result) != `{"argv":[],"mode":"rpc","stdinIsTTY":false,"stdoutIsTTY":false}` {
		t.Fatalf("result %s, %v; want an empty argv array", result.Result, err)
	}
}
