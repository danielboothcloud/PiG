package testfixture

import (
	"errors"
	"time"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"
)

// LegacyAPIWrapper is a registerProvider({streamSimple}) wrapper of the built-in Anthropic transport, as @gotgenes/pi-anthropic-auth registers one. It composes the caller's onPayload before its own payload change, keeps every other caller option (onResponse, onProviderStreamEvent, fetch) and delegates to the raw transport. Its own change marks metadata.user_id, so a test can tell that the caller ran first.
func LegacyAPIWrapper() *sdk.Extension {
	ext := sdk.New("legacy-api")
	ext.RegisterProvider("anthropic", sdk.ProviderConfig{"api": "anthropic-messages", "streamSimple": sdk.ProviderStreamSimpleFunc(func(ctx sdk.Context, model, transcript, options map[string]any) (*sdk.ModelEventStream, error) {
		caller, _ := options["onPayload"].(sdk.OnPayloadFunc)
		options["onPayload"] = sdk.OnPayloadFunc(func(payload any, model map[string]any) (any, error) {
			if caller == nil {
				return nil, errors.New("caller onPayload missing")
			}
			replacement, err := caller(payload, model)
			if err != nil {
				return nil, err
			}
			if replacement != nil {
				payload = replacement
			}
			object, _ := payload.(map[string]any)
			metadata, _ := object["metadata"].(map[string]any)
			user, _ := metadata["user_id"].(string)
			if user == "" {
				return nil, errors.New("caller did not change the payload first")
			}
			metadata["user_id"] = user + "+wrapped"
			return payload, nil
		})
		return sdk.AnthropicMessagesAPI(ctx).StreamSimple(model, transcript, sdk.ProviderStreamOptions{Signal: requestSignal{ctx}, Values: options})
	})})
	return ext
}

// requestSignal is the provider request's own lifetime as a context.Context: the host cancels it when the caller aborts the stream, as Pi's composer passes the request's signal to streamSimple.
type requestSignal struct{ sdk.Context }

func (requestSignal) Deadline() (time.Time, bool) { return time.Time{}, false }
func (requestSignal) Value(any) any               { return nil }
