package extensionconformance

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
)

// modelCancelGaps are the SDKs whose model calls cannot be cancelled by the extension yet (D235). A gap row asserts that its fixture still lacks the probe, so the row fails when an SDK gains it and the entry must be deleted.
var modelCancelGaps = map[string]string{
	"subprocess-rust":          "Rust ModelRegistry::stream/complete take no cancellation token (D235 follow-up)",
	"subprocess-python":        "Python ModelRegistry.stream/complete take no cancel event (D235 follow-up)",
	"subprocess-lisp":          "Lisp model-registry stream/complete take no cancellation (D235 follow-up)",
	"subprocess-rust-packed":   "Rust ModelRegistry::stream/complete take no cancellation token (D235 follow-up)",
	"subprocess-python-packed": "Python ModelRegistry.stream/complete take no cancel event (D235 follow-up)",
	"subprocess-lisp-packed":   "Lisp model-registry stream/complete take no cancellation (D235 follow-up)",
}

// Pi aborts ctx.modelRegistry.complete through options.signal: the provider request is cancelled and the call resolves with the provider's aborted message. Node passes an AbortSignal; Go passes a context.Context to CompleteContext (D235). The host double never answers until the provider context is cancelled, then ends with an aborted message whose errorMessage only it writes, so an SDK that fabricates its own aborted result fails.
func TestModelCompleteCancellationSDKsMatch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping model cancellation conformance in short mode (builds subprocess fixtures)")
	}
	cases := append(sdkHarnessCases(), packedSDKHarnessCases()...)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.make(t)
			t.Cleanup(func() {
				if h.cleanup != nil {
					h.cleanup()
				}
				if h.host != nil {
					h.host.Shutdown("test done")
				}
			})
			command, ok := findCommand(h.runner, "model-cancel-probe")
			if reason, gap := modelCancelGaps[tc.name]; gap {
				if ok {
					t.Fatalf("%s now registers model-cancel-probe; delete its modelCancelGaps entry (%s)", tc.name, reason)
				}
				t.Skipf("known gap: %s", reason)
			}
			if !ok {
				t.Fatal("model-cancel-probe command not registered")
			}
			providerClosed := make(chan struct{})
			h.bridge.SetHostAction("streamModel", func(callCtx context.Context, model map[string]any, _ map[string]any) (*ai.AssistantMessageEventStream, error) {
				provider, _ := model["provider"].(string)
				modelID, _ := model["modelId"].(string)
				stream := ai.NewAssistantMessageEventStream()
				partial := &ai.AssistantMessage{API: ai.API("faux"), Provider: provider, Model: modelID, StopReason: ai.StopReasonPending}
				if err := stream.Push(ai.StartEvent{Partial: partial}); err != nil {
					return nil, err
				}
				go func() {
					<-callCtx.Done()
					close(providerClosed)
					aborted := &ai.AssistantMessage{API: ai.API("faux"), Provider: provider, Model: modelID, StopReason: ai.StopReasonAborted, ErrorMessage: "conformance provider aborted"}
					_ = stream.Push(ai.ErrorEvent{Reason: ai.StopReasonAborted, Error: aborted})
				}()
				return stream, nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			start := time.Now()
			if err := command.Handler(ctx, ""); err != nil {
				t.Fatal(err)
			}
			want := "model-cancel=aborted:conformance provider aborted:info"
			waitFor(t, func() bool { return slices.Contains(h.ui.Recorded(), want) })
			if elapsed := time.Since(start); elapsed >= time.Second {
				t.Fatalf("cancelled completion returned after %v, want within 1 s", elapsed)
			}
			select {
			case <-providerClosed:
			default:
				t.Fatalf("the provider request was not cancelled; recorded %s", strings.Join(h.ui.Recorded(), " | "))
			}
		})
	}
}
