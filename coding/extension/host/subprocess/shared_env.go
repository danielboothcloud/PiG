package subprocess

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
)

// pig divergence (D234): Pi runs every extension and provider in one Node process, so a write to process.env is visible to every later read. Pig runs each extension in its own process, so the Host keeps the environment values extensions share and writes them into the process.env of every Node realm: live realms through a NotifySharedEnv frame, new realms through their spawn environment and a snapshot frame when they connect.

// CallSetSharedEnv (ext→host) sets or deletes one shared environment value. The args are a SetSharedEnvArgs and the result is null. The Host has queued the change to every live Node realm before it answers, so a request the Host sends any Node realm after the answer runs with the new process.env.
const CallSetSharedEnv = "env.setShared"

// NotifySharedEnv (host→Node realm) carries SharedEnvUpdate. The realm applies it when the frame arrives, before any later frame.
const NotifySharedEnv = "env.shared"

// SetSharedEnvArgs is the argument of CallSetSharedEnv. A nil Value deletes Key from process.env.
type SetSharedEnvArgs struct {
	Key   string  `json:"key"`
	Value *string `json:"value"`
}

// SharedEnvUpdate is the argument of NotifySharedEnv: each key's value, or null for a deleted key.
type SharedEnvUpdate struct {
	Values map[string]*string `json:"values"`
}

// sharedEnv is the Host's shared environment. A nil value records a deletion, so a deleted key is also removed from the environment a new realm inherits.
type sharedEnv struct {
	// mu orders each change against the snapshot a connecting realm receives, so no realm misses or reorders a change.
	mu     sync.Mutex
	values map[string]*string
}

// validateSharedEnvKey rejects names a process environment cannot hold and the variables the Host owns.
func validateSharedEnvKey(key string) error {
	switch {
	case key == "":
		return errors.New("shared environment key is empty")
	case strings.ContainsAny(key, "=\x00"):
		return fmt.Errorf("shared environment key %q contains '=' or NUL", key)
	case strings.HasPrefix(strings.ToUpper(key), "PIG_EXT_"), strings.HasPrefix(strings.ToUpper(key), "PIG_HARNESS_"):
		return fmt.Errorf("shared environment key %q is owned by the extension host", key)
	}
	return nil
}

// handleSetSharedEnv serves CallSetSharedEnv.
func (h *Host) handleSetSharedEnv(call *CallPayload) (*CallResultPayload, error) {
	var args SetSharedEnvArgs
	if err := json.Unmarshal(call.Args, &args); err != nil {
		return nil, fmt.Errorf("decode %s: %w", call.Method, err)
	}
	if err := validateSharedEnvKey(args.Key); err != nil {
		return nil, err
	}
	if args.Value != nil && strings.ContainsRune(*args.Value, 0) {
		return nil, fmt.Errorf("shared environment value of %q contains NUL", args.Key)
	}
	h.sharedEnv.mu.Lock()
	defer h.sharedEnv.mu.Unlock()
	if h.sharedEnv.values == nil {
		h.sharedEnv.values = make(map[string]*string)
	}
	h.sharedEnv.values[args.Key] = args.Value
	update := mustJSONObject(SharedEnvUpdate{Values: map[string]*string{args.Key: args.Value}})
	for _, conn := range h.nodeRealmConns() {
		_ = conn.Send(&Envelope{Type: MsgNotify, Notify: &NotifyPayload{Method: NotifySharedEnv, Args: update}})
	}
	return &CallResultPayload{Result: json.RawMessage("null")}, nil
}

// nodeRealmConns returns the open connection of every Node realm, in extension name order.
func (h *Host) nodeRealmConns() []*Conn {
	h.mu.Lock()
	defer h.mu.Unlock()
	var conns []*Conn
	for _, name := range slices.Sorted(maps.Keys(h.exts)) {
		me := h.exts[name]
		if !me.nodeRealm {
			continue
		}
		if conn := me.connection(); conn != nil && !conn.closed.Load() {
			conns = append(conns, conn)
		}
	}
	return conns
}

// sendSharedEnvSnapshot gives a Node realm that just connected every change made since its spawn environment was written.
func (h *Host) sendSharedEnvSnapshot(conn *Conn) {
	h.sharedEnv.mu.Lock()
	defer h.sharedEnv.mu.Unlock()
	if len(h.sharedEnv.values) == 0 {
		return
	}
	update := mustJSONObject(SharedEnvUpdate{Values: maps.Clone(h.sharedEnv.values)})
	_ = conn.Send(&Envelope{Type: MsgNotify, Notify: &NotifyPayload{Method: NotifySharedEnv, Args: update}})
}

// withSharedEnv applies the shared environment to env, the inherited environment of a new Node realm.
func (h *Host) withSharedEnv(env []string) []string {
	h.sharedEnv.mu.Lock()
	defer h.sharedEnv.mu.Unlock()
	for _, key := range slices.Sorted(maps.Keys(h.sharedEnv.values)) {
		env = slices.DeleteFunc(env, func(value string) bool { return envHasKey(value, key) })
		if value := h.sharedEnv.values[key]; value != nil {
			env = append(env, key+"="+*value)
		}
	}
	return env
}
