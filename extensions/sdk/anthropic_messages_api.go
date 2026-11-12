package sdk

import (
	"errors"
	"maps"
)

// Ports packages/ai/src/api/anthropic-messages.lazy.ts

// AnthropicMessagesAPI returns Pi's built-in Anthropic transport, bypassing registered provider callbacks. Each call returns immediately; setup and transport failures end its stream with an error event. Options.Signal owns cancellation: a signal already cancelled ends the stream with an aborted error event and sends no request, as Pi's bridged API does. OnPayload, OnResponse, OnProviderStreamEvent and Values["fetch"] use the model-stream callback bridge.
func AnthropicMessagesAPI(ctx Context) ProviderStreams {
	return providerAPIStreams(ctx)
}

func providerAPIStreams(ctx Context) ProviderStreams {
	stream := func(simple bool) ProviderStreamFunc {
		return func(model, transcript map[string]any, options ProviderStreamOptions) (*ModelEventStream, error) {
			// upstream: coding-agent shims pi-ai-bridge.mjs bridged: a signal that fired before the request ends it without sending.
			if cancelled(options.Signal) {
				aborted := modelStreamErrorEvent(errors.New("Request aborted"), model)
				aborted["reason"] = "aborted"
				aborted["error"].(map[string]any)["stopReason"] = "aborted"
				out := newModelEventStream()
				out.push(aborted)
				return out, nil
			}
			values := make(map[string]any, len(options.Values)+3)
			maps.Copy(values, options.Values)
			if options.OnPayload != nil {
				values["onPayload"] = OnPayloadFunc(options.OnPayload)
			}
			if options.OnResponse != nil {
				values["onResponse"] = OnResponseFunc(options.OnResponse)
			}
			if options.OnProviderStreamEvent != nil {
				values["onProviderStreamEvent"] = OnProviderStreamEventFunc(options.OnProviderStreamEvent)
			}
			return (ModelRegistry{context: ctx}).startModelStream(options.Signal, "", model, transcript, values, simple, true), nil
		}
	}
	return ProviderStreams{Stream: stream(false), StreamSimple: stream(true)}
}
