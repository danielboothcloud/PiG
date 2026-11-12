package sdk

import (
	"context"
	"testing"
)

// upstream: coding-agent shims pi-ai-bridge.mjs bridged: a signal that fired before the request ends the stream as aborted without sending it. A zero Context has no connection, so any host call would panic: the result proves no request started.
func TestAnthropicMessagesAPICancelledSignalSendsNothing(t *testing.T) {
	signal, cancel := context.WithCancel(context.Background())
	cancel()
	model := map[string]any{"id": "claude-test", "provider": "anthropic", "api": "anthropic-messages"}
	for _, simple := range []bool{false, true} {
		streams := AnthropicMessagesAPI(Context{})
		start := streams.Stream
		if simple {
			start = streams.StreamSimple
		}
		stream, err := start(model, map[string]any{"messages": []any{}}, ProviderStreamOptions{Signal: signal})
		if err != nil {
			t.Fatal(err)
		}
		result := stream.Result()
		if result["stopReason"] != "aborted" || result["errorMessage"] != "Request aborted" || result["provider"] != "anthropic" || result["model"] != "claude-test" || result["api"] != "anthropic-messages" {
			t.Fatalf("simple=%t result = %v", simple, result)
		}
	}
}
