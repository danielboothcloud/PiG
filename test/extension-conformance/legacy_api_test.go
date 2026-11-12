package extensionconformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/subprocess"
	icodingagent "github.com/MichaelKinsy/PiG/internal/codingagent"
	"github.com/MichaelKinsy/PiG/test/extension-conformance/testfixture"
)

const legacyAnthropicSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":0}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

// The Go wrapper runs isolated, packed and fused; the Node (testdata/legacy-api.mjs) and Python (testdata/legacy-api-python) wrappers isolated and packed. Rust and Lisp: see legacyCallbackGaps.
// Pi's provider composer passes the caller's options object, function members included, to an extension's streamSimple (provider-composer.ts streamWith), so a registerProvider({streamSimple}) wrapper such as @gotgenes/pi-anthropic-auth composes the caller's onPayload and keeps its fetch, onResponse and onProviderStreamEvent. The wrapper here delegates to the raw built-in transport, and every assertion is on the HTTP exchange or on a callback the caller (this test) owns.
func TestLegacyWrapperRawTransportCallbacks(t *testing.T) {
	for _, topology := range []string{"isolated", "packed", "fused", "node-isolated", "node-packed", "python-isolated", "python-packed"} {
		t.Run(topology, func(t *testing.T) {
			var mu sync.Mutex
			type exchange struct {
				user, fetchTag, auth string
			}
			var exchanges []exchange
			closed := make(chan string, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				metadata, _ := payload["metadata"].(map[string]any)
				user, _ := metadata["user_id"].(string)
				mu.Lock()
				exchanges = append(exchanges, exchange{user: user, fetchTag: r.Header.Get("X-Caller-Fetch"), auth: r.Header.Get("X-Api-Key") + r.Header.Get("Authorization")})
				mu.Unlock()
				if strings.HasPrefix(user, "hang") {
					<-r.Context().Done()
					closed <- user
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, legacyAnthropicSSE)
			}))
			defer server.Close()
			services := loadLegacyWrapper(t, topology, server.URL)
			model := services.ModelRuntime().GetModel("anthropic", "claude-test")
			request := ai.Context{Messages: []ai.Message{ai.UserMessage{Content: ai.UserText("hello")}}}
			type counts struct{ payloads, responses, fetches, events atomic.Int32 }
			options := func(tag string, c *counts, responseErr error) ai.StreamOptions {
				return ai.StreamOptions{
					APIKey: tag + "-key",
					OnPayload: func(payload any, _ *ai.Model) (any, error) {
						c.payloads.Add(1)
						encoded, err := json.Marshal(payload)
						if err != nil {
							return nil, err
						}
						var object map[string]any
						if err := json.Unmarshal(encoded, &object); err != nil {
							return nil, err
						}
						object["metadata"] = map[string]any{"user_id": tag}
						return object, nil
					},
					OnResponse: func(_ context.Context, response ai.ProviderResponse, _ *ai.Model) error {
						c.responses.Add(1)
						if response.Status != http.StatusOK {
							return fmt.Errorf("status %d", response.Status)
						}
						return responseErr
					},
					OnProviderStreamEvent: func(context.Context, any, *ai.Model) error { c.events.Add(1); return nil },
					Fetch: &http.Client{Transport: ai.FetchFunction(func(r *http.Request) (*http.Response, error) {
						c.fetches.Add(1)
						r.Header.Set("X-Caller-Fetch", tag)
						return http.DefaultTransport.RoundTrip(r)
					})},
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()

			// Concurrent streams: each caller's callbacks shape only its own request, with the caller's change before the wrapper's.
			var wg sync.WaitGroup
			tags := []string{"alpha", "sk-ant-oat01-beta"}
			results := make([]*ai.AssistantMessage, len(tags))
			cs := make([]*counts, len(tags))
			for i, tag := range tags {
				cs[i] = &counts{}
				wg.Go(func() {
					results[i] = services.ModelRuntime().StreamSimple(ctx, model, request, options(tag, cs[i], nil)).Result()
				})
			}
			wg.Wait()
			for i, tag := range tags {
				c := cs[i]
				if results[i].StopReason != ai.StopReasonStop || c.payloads.Load() != 1 || c.responses.Load() != 1 || c.fetches.Load() != 1 || c.events.Load() == 0 {
					t.Fatalf("%s: result=%s %q payloads=%d responses=%d fetches=%d events=%d", tag, results[i].StopReason, results[i].ErrorMessage, c.payloads.Load(), c.responses.Load(), c.fetches.Load(), c.events.Load())
				}
			}
			mu.Lock()
			for _, tag := range tags {
				found := false
				for _, e := range exchanges {
					if e.user == tag+"+wrapped" {
						found = true
						if e.fetchTag != tag || !strings.Contains(e.auth, tag+"-key") {
							t.Errorf("%s crossed callbacks or credentials: %+v", tag, e)
						}
					}
				}
				if !found {
					t.Errorf("no request carried %q: %+v", tag+"+wrapped", exchanges)
				}
			}
			mu.Unlock()

			// The caller's onResponse rejection rejects the request.
			rejected := services.ModelRuntime().StreamSimple(ctx, model, request, options("gamma", &counts{}, errors.New("caller rejected the response"))).Result()
			if rejected.StopReason != ai.StopReasonError || !strings.Contains(rejected.ErrorMessage, "caller rejected the response") {
				t.Fatalf("onResponse rejection: %s %q", rejected.StopReason, rejected.ErrorMessage)
			}

			// Cancelling the caller's request closes the HTTP exchange the wrapper started. The host ends a cancelled legacy streamSimple as an error at once (provider_stream_test.go pins it), not with the aborted event the wrapper's delegate would forward in Pi.
			hangCtx, hangCancel := context.WithCancel(ctx)
			hung := &counts{}
			stream := services.ModelRuntime().StreamSimple(hangCtx, model, request, options("hang", hung, nil))
			deadline := time.Now().Add(15 * time.Second)
			for hung.fetches.Load() == 0 && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			hangCancel()
			select {
			case user := <-closed:
				if user != "hang+wrapped" {
					t.Fatalf("closed %q", user)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("cancelling the caller left the HTTP exchange open")
			}
			if result := stream.Result(); result.StopReason != ai.StopReasonError || !strings.Contains(result.ErrorMessage, "context canceled") {
				t.Fatalf("cancelled stream: %s %q", result.StopReason, result.ErrorMessage)
			}
		})
	}
}

// loadLegacyWrapper loads the wrapper in one realization over a Session whose anthropic provider points at endpoint, and returns the services.
func loadLegacyWrapper(t *testing.T, topology, endpoint string) *coding.AgentSessionServices {
	t.Helper()
	services, err := coding.CreateAgentSessionServices(coding.CreateAgentSessionServicesOptions{CWD: t.TempDir(), AgentDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(services.Close)
	session, err := coding.NewSession(services, coding.SessionOptions{NoSession: true, SkipBuiltinTools: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	host := subprocess.NewHost(t.TempDir())
	t.Cleanup(func() { host.Shutdown("test done") })
	host.SetProviderCallbacks(services.Registry().RegisterExtensionProvider, services.Registry().UnregisterProvider)
	bridge := subprocess.NewUIBridge(func() {})
	detach := icodingagent.WireModelOperations(bridge, icodingagent.ModelOperationBindings{CurrentModel: session.Model, ModelLookup: services.ModelRuntime().GetModel, ModelCatalog: func(...string) []*ai.Model { return services.ModelRuntime().GetModels() }, Registry: services.Registry().ModelRegistry, ModelBuilder: func(spec string) (*ai.Model, error) { return coding.BuildModel(spec, services) }, SessionHandle: session})
	t.Cleanup(detach)
	host.SetUIBridge(bridge)
	if err := services.Registry().RegisterExtensionProvider("anthropic", extension.ProviderConfig{API: ai.APIAnthropicMessages, BaseURL: endpoint, APIKey: "registered-key", Models: []extension.ProviderModelConfig{{ID: "claude-test", Name: "Test", ContextWindow: 8192, MaxTokens: 64}}}); err != nil {
		t.Fatal(err)
	}
	if topology == "fused" {
		if _, err := host.LoadInProcess(t.Context(), subprocess.ExtConfig{Name: "legacy-api", Enabled: true}, func(conn net.Conn) error { return testfixture.LegacyAPIWrapper().RunWithConn(conn) }); err != nil {
			t.Fatal(err)
		}
	} else {
		resolve := func(dir, name string) subprocess.ExtConfig {
			path, err := filepath.Abs(dir)
			if err != nil {
				t.Fatal(err)
			}
			cfg, _, err := subprocess.ResolveExtConfigWithIdentity(path, name)
			if err != nil {
				t.Fatal(err)
			}
			return cfg
		}
		fixture, sibling := "testdata/legacy-api-go", "testdata/provider-go-sibling"
		switch {
		case strings.HasPrefix(topology, "node-"):
			fixture, sibling = "testdata/legacy-api.mjs", "testdata/provider-sibling.mjs"
		case strings.HasPrefix(topology, "python-"):
			fixture, sibling = "testdata/legacy-api-python", "testdata/provider-python-sibling"
		}
		cfgs := []subprocess.ExtConfig{resolve(fixture, "legacy-api")}
		if strings.HasSuffix(topology, "isolated") {
			cfgs[0].Isolation = "isolated"
		} else {
			cfgs = append(cfgs, resolve(sibling, "provider-sibling"))
		}
		if _, loadErrors := host.LoadAll(t.Context(), cfgs); len(loadErrors) > 0 {
			t.Fatal(loadErrors)
		}
	}
	if failures := applyQueuedRegistrations(host); len(failures) > 0 {
		t.Fatal(failures)
	}
	return services
}

// legacyCallbackGaps are the SDKs whose provider_stream_simple handler does not yet receive the caller's callbacks and that have no anthropicMessagesApi (test/parity/sdk-exports.toml lane SdkRawAnthropic). A gap's row fails once its fixture exists, so the entry must be deleted and the SDK added to TestLegacyWrapperRawTransportCallbacks and TestAnthropicMessagesAPIRawTransport.
var legacyCallbackGaps = map[string]string{
	"rust": "Rust's legacy streamSimple options are a serde_json::Value, which cannot carry the caller's functions; it needs a callback-carrying handler form and anthropic_messages_api",
	"lisp": "Lisp has neither the callback forwarding nor anthropic-messages-api; sbcl is needed to prove them",
}

func TestLegacyCallbackGapsAreListed(t *testing.T) {
	for sdk, reason := range legacyCallbackGaps {
		t.Run(sdk, func(t *testing.T) {
			for _, fixture := range []string{"testdata/legacy-api-" + sdk, "testdata/raw-anthropic-" + sdk} {
				if _, err := os.Stat(fixture); err == nil {
					t.Fatalf("%s exists: delete the %s entry of legacyCallbackGaps (%s) and add the SDK to the callback and raw transport rows", fixture, sdk, reason)
				}
			}
		})
	}
}
