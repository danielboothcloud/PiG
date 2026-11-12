package extensionconformance

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

// Pi 1.5.0 api/anthropic-messages.lazy.js + api/lazy.js returns a stream immediately and forwards setup failures as error events. The API leaf never dispatches registered providers.
func TestAnthropicMessagesAPIRawTransport(t *testing.T) {
	for _, topology := range []string{"isolated", "packed", "fused", "python-isolated", "python-packed"} {
		t.Run(topology, func(t *testing.T) {
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
			bridge := subprocess.NewUIBridge(func() {})
			detach := icodingagent.WireModelOperations(bridge, icodingagent.ModelOperationBindings{CurrentModel: session.Model, ModelLookup: services.ModelRuntime().GetModel, ModelCatalog: func(...string) []*ai.Model { return services.ModelRuntime().GetModels() }, Registry: services.Registry().ModelRegistry, ModelBuilder: func(spec string) (*ai.Model, error) { return coding.BuildModel(spec, services) }, SessionHandle: session})
			t.Cleanup(detach)
			host.SetUIBridge(bridge)
			var loaded *extension.Extension
			// LoadInProcess uses the same SDK connection as a D31 fused factory.
			if topology == "fused" {
				loaded, err = host.LoadInProcess(t.Context(), subprocess.ExtConfig{Name: "raw-anthropic", Enabled: true}, func(conn net.Conn) error { return testfixture.AnthropicAPIProbe().RunWithConn(conn) })
			} else {
				fixture, siblingFixture := "testdata/raw-anthropic-go", "testdata/provider-go-sibling"
				if strings.HasPrefix(topology, "python-") {
					fixture, siblingFixture = "testdata/raw-anthropic-python", "testdata/provider-python-sibling"
				}
				source, pathErr := filepath.Abs(fixture)
				if pathErr != nil {
					t.Fatal(pathErr)
				}
				cfg, _, resolveErr := subprocess.ResolveExtConfigWithIdentity(source, "raw-anthropic")
				if resolveErr != nil {
					t.Fatal(resolveErr)
				}
				if strings.HasSuffix(topology, "isolated") {
					cfg.Isolation = "isolated"
				}
				cfgs := []subprocess.ExtConfig{cfg}
				if strings.HasSuffix(topology, "packed") {
					siblingPath, pathErr := filepath.Abs(siblingFixture)
					if pathErr != nil {
						t.Fatal(pathErr)
					}
					sibling, _, resolveErr := subprocess.ResolveExtConfigWithIdentity(siblingPath, "provider-sibling")
					if resolveErr != nil {
						t.Fatal(resolveErr)
					}
					cfgs = append(cfgs, sibling)
				}
				exts, loadErrors := host.LoadAll(t.Context(), cfgs)
				if len(loadErrors) > 0 {
					t.Fatal(loadErrors)
				}
				loaded = &exts[0]
			}
			if err != nil {
				t.Fatal(err)
			}
			var callbacks atomic.Int32
			if err := services.Registry().RegisterExtensionProvider("anthropic", extension.ProviderConfig{API: ai.APIAnthropicMessages, BaseURL: "https://registered.invalid", APIKey: "registered-key", Models: []extension.ProviderModelConfig{{ID: "claude-test", Name: "Test", ContextWindow: 8192, MaxTokens: 64}}, StreamSimple: func(extension.Model, extension.AIContext, extension.SimpleStreamOptions) extension.AssistantMessageEventStream {
				callbacks.Add(1)
				stream := ai.NewAssistantMessageEventStream()
				stream.End(&ai.AssistantMessage{StopReason: ai.StopReasonError, ErrorMessage: "registered wrapper recursed"})
				return stream
			}}); err != nil {
				t.Fatal(err)
			}
			for _, simple := range []bool{false, true} {
				for _, key := range []string{"test-api-key", "sk-ant-oat01-test-oauth"} {
					for _, mode := range []string{"success", "cancel", "payload-error"} {
						t.Run(fmt.Sprintf("simple=%t/%s/%s", simple, key, mode), func(t *testing.T) {
							entered, closed := make(chan struct{}), make(chan struct{})
							var requests atomic.Int32
							server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
								requests.Add(1)
								var body map[string]any
								if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
									t.Error(err)
								}
								if r.URL.Path != "/custom/v1/messages" || r.Header.Get("X-Raw-Fetch") != "extension" || body["model"] != "claude-test" {
									t.Errorf("request path=%s headers=%v body=%v", r.URL.Path, r.Header, body)
								}
								metadata, _ := body["metadata"].(map[string]any)
								if metadata["user_id"] != "raw-payload-hook" {
									t.Errorf("metadata=%v", metadata)
								}
								if key == "test-api-key" && r.Header.Get("X-Api-Key") != key {
									t.Errorf("API key header=%v", r.Header)
								}
								if key != "test-api-key" && r.Header.Get("Authorization") != "Bearer "+key {
									t.Errorf("OAuth header=%v", r.Header)
								}
								close(entered)
								if mode == "cancel" {
									<-r.Context().Done()
									close(closed)
									return
								}
								w.Header().Set("Content-Type", "text/event-stream")
								_, _ = fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":0}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
							}))
							defer server.Close()
							args, _ := json.Marshal(map[string]any{"Model": map[string]any{"id": "claude-test", "provider": "anthropic", "api": "anthropic-messages", "baseUrl": server.URL + "/custom", "maxTokens": 64, "contextWindow": 8192}, "Key": key, "Simple": simple, "Cancel": mode == "cancel", "Fail": mode == "payload-error"})
							ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
							defer cancel()
							done := make(chan error, 1)
							go func() { done <- loaded.Commands["raw-probe"].Handler(ctx, string(args)) }()
							if mode == "cancel" {
								select {
								case <-entered:
								case err := <-done:
									t.Fatalf("finished before HTTP request: %v", err)
								case <-ctx.Done():
									t.Fatal(ctx.Err())
								}
								if err := loaded.Commands["raw-cancel"].Handler(ctx, ""); err != nil {
									t.Fatal(err)
								}
							}
							select {
							case err := <-done:
								if err != nil {
									t.Fatal(err)
								}
							case <-ctx.Done():
								t.Fatal(ctx.Err())
							}
							if mode == "cancel" {
								select {
								case <-closed:
								case <-ctx.Done():
									t.Fatal("HTTP request did not close")
								}
							}
							expected := int32(1)
							if mode == "payload-error" {
								expected = 0
							}
							if requests.Load() != expected || callbacks.Load() != 0 {
								t.Fatalf("requests=%d registered callbacks=%d", requests.Load(), callbacks.Load())
							}
						})
					}
				}
			}
		})
	}
}
