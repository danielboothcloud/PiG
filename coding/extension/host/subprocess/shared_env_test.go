package subprocess

import (
	"encoding/json"
	"slices"
	"testing"
)

// pig divergence (D234): a Node realm the Host starts after a shared environment change inherits it. A set value replaces the inherited one, and a deletion removes a value inherited from pig's own environment, as `delete process.env[key]` does in Pi's one process.
func TestSharedEnvAppliesToANewRealmsSpawnEnvironment(t *testing.T) {
	h := &Host{}
	for _, args := range []string{`{"key":"SET_ME","value":"new"}`, `{"key":"DROP_ME","value":null}`, `{"key":"ADDED","value":""}`} {
		if _, err := h.handleSetSharedEnv(&CallPayload{Method: CallSetSharedEnv, Args: json.RawMessage(args)}); err != nil {
			t.Fatalf("%s: %v", args, err)
		}
	}
	got := h.withSharedEnv([]string{"KEEP=1", "SET_ME=old", "DROP_ME=inherited"})
	want := []string{"KEEP=1", "ADDED=", "SET_ME=new"}
	if !slices.Equal(got, want) {
		t.Fatalf("spawn environment = %q, want %q", got, want)
	}
}

func TestSharedEnvRejectsKeysBeforeRecordingThem(t *testing.T) {
	h := &Host{}
	for _, args := range []string{`{"key":"","value":"x"}`, `{"key":"A=B","value":"x"}`, `{"key":"Pig_Ext_Socket","value":"x"}`, `{"key":"PIG_HARNESS_ARGV_FILE","value":null}`, `{"key":"OK","value":"a\u0000b"}`} {
		if _, err := h.handleSetSharedEnv(&CallPayload{Method: CallSetSharedEnv, Args: json.RawMessage(args)}); err == nil {
			t.Fatalf("%s was accepted", args)
		}
	}
	if len(h.sharedEnv.values) != 0 {
		t.Fatalf("rejected calls recorded %v", h.sharedEnv.values)
	}
}
