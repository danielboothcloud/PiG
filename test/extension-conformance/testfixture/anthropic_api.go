package testfixture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"
)

// AnthropicAPIProbe wraps the builtin API with extension-owned payload and fetch hooks.
func AnthropicAPIProbe() *sdk.Extension {
	ext := sdk.New("raw-anthropic")
	var mu sync.Mutex
	var cancel context.CancelFunc
	ext.Command("raw-cancel", "Cancel the raw API request", func(sdk.Context, string) error {
		mu.Lock()
		defer mu.Unlock()
		if cancel == nil {
			return errors.New("no raw request")
		}
		cancel()
		return nil
	})
	ext.Command("raw-probe", "Call the builtin API without registry dispatch", func(ctx sdk.Context, args string) error {
		var input struct {
			Model  map[string]any
			Key    string
			Simple bool
			Cancel bool
			Fail   bool
		}
		if err := json.Unmarshal([]byte(args), &input); err != nil {
			return err
		}
		signal, stop := context.WithCancel(context.Background())
		defer stop()
		mu.Lock()
		cancel = stop
		mu.Unlock()
		defer func() { mu.Lock(); cancel = nil; mu.Unlock() }()
		var order []string
		returned := make(chan struct{})
		opts := sdk.ProviderStreamOptions{Signal: signal, Values: map[string]any{"apiKey": input.Key}}
		opts.OnPayload = func(payload any, _ map[string]any) (any, error) {
			order = append(order, "payload")
			if input.Fail {
				return nil, errors.New("fixture payload rejection")
			}
			payload.(map[string]any)["metadata"] = map[string]any{"user_id": "raw-payload-hook"}
			return payload, nil
		}
		opts.Values["fetch"] = sdk.ModelFetchFunc(func(request *http.Request) (*http.Response, error) {
			select {
			case <-returned:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			order = append(order, "fetch")
			request.Header.Set("X-Raw-Fetch", "extension")
			return http.DefaultTransport.RoundTrip(request)
		})
		opts.OnResponse = func(response map[string]any, _ map[string]any) error {
			order = append(order, "response")
			if response["status"] != float64(http.StatusOK) {
				return fmt.Errorf("response status = %v", response["status"])
			}
			return nil
		}
		api := sdk.AnthropicMessagesAPI(ctx)
		run := api.Stream
		if input.Simple {
			run = api.StreamSimple
		}
		stream, err := run(input.Model, map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hello", "timestamp": 1}}}, opts)
		close(returned)
		if err != nil {
			return fmt.Errorf("synchronous setup failure: %w", err)
		}
		result := stream.Result()
		var terminal map[string]any
		for event := range stream.Events(context.Background()) {
			terminal = event
		}
		wantEvent := "done"
		if input.Cancel || input.Fail {
			wantEvent = "error"
		}
		if terminal["type"] != wantEvent {
			return fmt.Errorf("terminal event = %v, want %s", terminal, wantEvent)
		}
		reason := "stop"
		expectedOrder := []string{"payload", "fetch", "response"}
		if input.Cancel {
			reason = "aborted"
			expectedOrder = []string{"payload", "fetch"}
		}
		if input.Fail {
			reason = "error"
			expectedOrder = []string{"payload"}
		}
		if result["stopReason"] != reason || !slices.Equal(order, expectedOrder) {
			return fmt.Errorf("raw result=%v order=%v", result, order)
		}
		if input.Fail && result["errorMessage"] != "fixture payload rejection" {
			return fmt.Errorf("payload error = %v", result)
		}
		return nil
	})
	return ext
}
