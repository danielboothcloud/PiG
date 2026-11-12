package subprocess

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

// CallRelaunch (D238): the host decodes and validates the request before the mode sees it, passes the session file its own binding reports, and returns the mode's refusal unchanged.
func TestRelaunchCallValidatesTheRequest(t *testing.T) {
	var got []RelaunchRequest
	actions := &HostCallbacks{
		GetSessionFile: func() string { return "/s/session.jsonl" },
		Relaunch: func(request RelaunchRequest) error {
			got = append(got, request)
			if slices.Contains(request.Args, "refuse") {
				return errors.New("relaunch: refused by the mode")
			}
			return nil
		},
	}
	b := NewUIBridge(func() {})
	for _, tc := range []struct {
		args    string
		actions *HostCallbacks
		wantErr string
	}{
		{`{"args":["--models","x/y"],"env":{"A":"1","B":null}}`, actions, ""},
		{`{"args":[]}`, actions, ""},
		{`{"args":["refuse"]}`, actions, "refused by the mode"},
		{`{}`, actions, "requires args"},
		{`{"args":null}`, actions, "requires args"},
		{`{"args":[1]}`, actions, "decode relaunch"},
		{`{"args":["a\u0000b"]}`, actions, "argument 0 contains NUL"},
		{`{"args":[],"env":{"":"x"}}`, actions, "environment key is empty"},
		{`{"args":[],"env":{"A=B":"x"}}`, actions, "contains '=' or NUL"},
		{`{"args":[],"env":{"pig_ext_socket":"x"}}`, actions, "owned by the extension host"},
		{`{"args":[],"env":{"PIG_HARNESS_BINARY":null}}`, actions, "owned by the extension host"},
		{`{"args":[],"env":{"A":"x\u0000"}}`, actions, "value of \"A\" contains NUL"},
		{`{"args":[]}`, &HostCallbacks{}, "not available in this host"},
		{`{"args":[]}`, nil, "not available in this host"},
	} {
		_, err := b.handleRelaunch(tc.actions, json.RawMessage(tc.args))
		if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Fatalf("%s: error %v, want %q", tc.args, err, tc.wantErr)
		}
	}
	if len(got) != 3 {
		t.Fatalf("mode received %d requests, want 3: %+v", len(got), got)
	}
	first := got[0]
	if !slices.Equal(first.Args, []string{"--models", "x/y"}) || first.SessionFile != "/s/session.jsonl" || *first.Env["A"] != "1" || first.Env["B"] != nil || len(first.Env) != 2 {
		t.Fatalf("first request %+v", first)
	}
	if got[1].Args == nil || len(got[1].Args) != 0 {
		t.Fatalf("empty args reached the mode as %#v, want an empty list", got[1].Args)
	}
}
