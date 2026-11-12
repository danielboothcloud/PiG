package extensionconformance

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/MichaelKinsy/PiG/coding/extension/host/subprocess"
)

// ctx.relaunch (D238) in every SDK and placement against the real subprocess Host. The rig's mode refuses with a text only it writes, so an SDK that drops the call, answers locally or swallows the host's error fails; the request the mode receives must carry the arguments byte for byte (an empty string and a non-ASCII path included), a null value as a removal, and the session file the host read from its own session binding. A host-owned key and a host whose mode binds no relaunch are refused by the host before the mode sees anything.
func TestRelaunchAcrossSDKs(t *testing.T) {
	args := []any{"--models", "x/y", "-e", "/ext/zwei wörter", ""}
	env := map[string]any{"AIO_RECIPE_STAGE": "1", "AIO_RECIPE_GONE": nil}
	relaunch := func(r *modelTypesRig, args []any, env map[string]any) string {
		r.t.Helper()
		outcome, _ := r.toolWith("c3_relaunch", map[string]any{"args": args, "env": env})["error"].(string)
		return outcome
	}
	eachModelTypesPlacement(t, func(t *testing.T, r *modelTypesRig) {
		if got := relaunch(r, args, env); !strings.Contains(got, "relaunch is not available in this host") {
			t.Fatalf("relaunch without a mode = %q, want the host's refusal", got)
		}
		var mu sync.Mutex
		var requests []subprocess.RelaunchRequest
		refuse := true
		r.bridge.SetHostAction("getSessionFile", func() string { return "/rig/sessions/rig-session.jsonl" })
		r.bridge.SetHostAction("relaunch", func(request subprocess.RelaunchRequest) error {
			mu.Lock()
			defer mu.Unlock()
			requests = append(requests, request)
			if refuse {
				return errors.New("relaunch: the conformance rig cannot replace this process")
			}
			return nil
		})
		if got := relaunch(r, args, env); !strings.Contains(got, "relaunch: the conformance rig cannot replace this process") {
			t.Fatalf("relaunch refused by the mode = %q, want the mode's error", got)
		}
		mu.Lock()
		refuse = false
		mu.Unlock()
		if got := relaunch(r, []any{}, nil); got != "<nil>" {
			t.Fatalf("accepted relaunch = %q", got)
		}
		if got := relaunch(r, []any{"-p"}, map[string]any{"PIG_EXT_SOCKET": "/tmp/x"}); !strings.Contains(got, `environment key "PIG_EXT_SOCKET" is owned by the extension host`) {
			t.Fatalf("relaunch with a host-owned key = %q, want the host's refusal", got)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(requests) != 2 {
			t.Fatalf("the mode received %d requests, want 2 (the refused key must not reach it): %+v", len(requests), requests)
		}
		first := requests[0]
		wantArgs := []string{"--models", "x/y", "-e", "/ext/zwei wörter", ""}
		if !slices.Equal(first.Args, wantArgs) || first.SessionFile != "/rig/sessions/rig-session.jsonl" {
			t.Fatalf("request args %q session %q, want %q and the host's session file", first.Args, first.SessionFile, wantArgs)
		}
		if got := slices.Sorted(maps.Keys(first.Env)); !slices.Equal(got, []string{"AIO_RECIPE_GONE", "AIO_RECIPE_STAGE"}) || first.Env["AIO_RECIPE_GONE"] != nil || first.Env["AIO_RECIPE_STAGE"] == nil || *first.Env["AIO_RECIPE_STAGE"] != "1" {
			t.Fatalf("request env %v, want AIO_RECIPE_STAGE=1 and AIO_RECIPE_GONE removed", first.Env)
		}
		if second := requests[1]; second.Args == nil || len(second.Args) != 0 || len(second.Env) != 0 {
			t.Fatalf("empty request = %+v, want empty args and no environment change", second)
		}
	})
}
