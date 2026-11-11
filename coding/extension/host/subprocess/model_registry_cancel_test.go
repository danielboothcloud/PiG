package subprocess

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
)

// TestHost_Integration_SDKCompleteContextAbortsProviderRequest drives the Go SDK's ModelRegistry.CompleteContext against a provider whose server never answers. Pi aborts the provider request through options.signal and completes with the provider's aborted message; the Go SDK carries the signal as a context.Context.
func TestHost_Integration_SDKCompleteContextAbortsProviderRequest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	binPath := buildSDKFixture(t)
	shortSockDir(t)

	requestStarted := make(chan struct{})
	requestClosed := make(chan struct{})
	var startOnce, closeOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		startOnce.Do(func() { close(requestStarted) })
		<-r.Context().Done()
		closeOnce.Do(func() { close(requestClosed) })
	}))
	defer server.Close()
	provider := ai.NewOpenAIProvider(ai.OpenAIConfig{Model: "probe", ProviderID: "probe-provider", APIKey: "test", BaseURL: server.URL})
	defer func() { _ = provider.Close() }()

	var mu sync.Mutex
	var notifications []string
	fakeUI := newTestUIContext()
	fakeUI.onNotify = func(msg, _ string) {
		mu.Lock()
		notifications = append(notifications, msg)
		mu.Unlock()
	}
	h := newTestHost(t)
	bridge := NewUIBridge(func() {})
	bridge.SetUIContext(fakeUI)
	bridge.SetActions(&HostCallbacks{StreamModel: func(ctx context.Context, _, _ map[string]any) (*ai.AssistantMessageEventStream, error) {
		return provider.Stream(ctx, ai.NormalizeContext(ai.Context{Messages: []ai.Message{ai.UserMessage{Content: ai.UserText("hello")}}}), ai.StreamOptions{})
	}})
	h.SetUIBridge(bridge)
	defer h.Shutdown("test done")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ext, err := h.Load(ctx, ExtConfig{Name: "sdk-fixture", Path: binPath, Enabled: true})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cmd, ok := ext.Commands["model-complete-cancel"]
	if !ok {
		t.Fatal("SDK extension missing model-complete-cancel command")
	}
	if err := cmd.Handler(ctx, ""); err != nil {
		t.Fatalf("model-complete-cancel: %v", err)
	}

	select {
	case <-requestStarted:
	default:
		t.Fatal("the provider request never reached the server")
	}
	select {
	case <-requestClosed:
	case <-time.After(time.Second):
		t.Fatal("the provider request was not closed after the context was cancelled")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(notifications) != 1 {
		t.Fatalf("notifications = %q, want one report", notifications)
	}
	var report struct {
		ElapsedMs    int64  `json:"elapsedMs"`
		StopReason   string `json:"stopReason"`
		ErrorMessage string `json:"errorMessage"`
	}
	if err := json.Unmarshal([]byte(notifications[0]), &report); err != nil {
		t.Fatalf("report %q: %v", notifications[0], err)
	}
	if report.StopReason != "aborted" || report.ErrorMessage == "" {
		t.Fatalf("result = %+v, want the provider's aborted message", report)
	}
	if report.ElapsedMs < 100 || report.ElapsedMs >= 1000 {
		t.Fatalf("CompleteContext returned after %d ms, want within 1 s of starting with cancellation at 100 ms", report.ElapsedMs)
	}
}
