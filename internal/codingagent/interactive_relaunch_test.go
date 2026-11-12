package codingagent_test

import (
	"errors"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding/extension/host/subprocess"
)

// An extension's relaunch accepted in session_start leaves the positional prompts to the relaunched pig, which gets them in its own arguments (D238): the old runtime sends none. A refused relaunch leaves the runtime as it was, so both prompts go out; once pig is shutting down, the request is refused before the command line sees it.
func TestInteractiveRelaunchLeavesStartupMessagesToTheRelaunchedPig(t *testing.T) {
	for _, tc := range []struct {
		name       string
		refuse     error
		wantErr    string
		wantModels int
	}{
		{"accepted", nil, "", 0},
		{"refused", errors.New("relaunch: refused by the command line"), "refused by the command line", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pair := newSessionPair(t, `{"retry":{"enabled":false}}`, 100000, nil,
				reply("one", ai.Usage{Input: 10, Output: 10}),
				reply("two", ai.Usage{Input: 10, Output: 10}),
			)
			var accepted []subprocess.RelaunchRequest
			accept := func(request subprocess.RelaunchRequest) error {
				accepted = append(accepted, request)
				return tc.refuse
			}
			err := pair.harness.RequestRelaunch(accept, subprocess.RelaunchRequest{Args: []string{"--models", "x/y"}})
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || err.Error() != tc.refuse.Error()) {
				t.Fatalf("relaunch error %v, want %q", err, tc.wantErr)
			}
			if len(accepted) != 1 || accepted[0].Args[1] != "x/y" {
				t.Fatalf("the command line received %+v", accepted)
			}
			pair.harness.SubmitStartupMessages("first", []string{"second"})
			pair.harness.WaitIdle(t, 10*time.Second)
			if got := len(pair.provider.requestLog()); got != tc.wantModels {
				t.Fatalf("model requests = %d, want %d", got, tc.wantModels)
			}
			if tc.refuse == nil {
				// The accepted relaunch started the shutdown; a second request is refused by the mode itself.
				if err := pair.harness.RequestRelaunch(accept, subprocess.RelaunchRequest{}); err == nil || err.Error() != "relaunch: pig is already shutting down" {
					t.Fatalf("relaunch while shutting down = %v", err)
				}
				if len(accepted) != 1 {
					t.Fatalf("the command line saw a request made while pig shuts down")
				}
			}
		})
	}
}
