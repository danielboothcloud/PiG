package subprocess

// Ports packages/coding-agent/src/core/model-runtime.ts (the provider composer passes the caller's request options, function members included, to an extension's streamSimple).
// Ports packages/ai/src/types.ts (ProviderRequestOptions.onPayload and fetch, StreamOptions.onResponse and onProviderStreamEvent).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/MichaelKinsy/PiG/ai"
)

// legacyStreamKey names one in-flight provider_stream_simple request: the connection that serves it and the request's envelope ID, which the extension sends back as the parent request of its provider.callback calls.
type legacyStreamKey struct {
	conn *Conn
	id   string
}

// legacyStreamCallbackNames lists the caller's function-valued request options a legacy streamSimple receives, in wire order. Pi passes the caller's options object to extension.streamSimple whole (provider-composer.ts streamWith), so a wrapper can compose them.
func legacyStreamCallbackNames(options ai.StreamOptions) []string {
	names := nativeStreamCallbackNames(options)
	if options.Fetch != nil {
		names = append(names, "fetch")
	}
	return names
}

// addLegacyStreamCallback admits provider.callback calls whose parent is the request id on conn until the returned release runs.
func (h *Host) addLegacyStreamCallback(conn *Conn, id string, callback nativeProviderCallback) func() {
	key := legacyStreamKey{conn: conn, id: id}
	h.legacyStreamsMu.Lock()
	if h.legacyStreams == nil {
		h.legacyStreams = map[legacyStreamKey]nativeProviderCallback{}
	}
	h.legacyStreams[key] = callback
	h.legacyStreamsMu.Unlock()
	return func() {
		h.legacyStreamsMu.Lock()
		delete(h.legacyStreams, key)
		h.legacyStreamsMu.Unlock()
	}
}

// legacyStreamCallback returns the callback of the in-flight legacy stream that parent names on conn, or nil.
func (h *Host) legacyStreamCallback(conn *Conn, parent string) nativeProviderCallback {
	if parent == "" {
		return nil
	}
	h.legacyStreamsMu.Lock()
	defer h.legacyStreamsMu.Unlock()
	return h.legacyStreams[legacyStreamKey{conn: conn, id: parent}]
}

// newLegacyStreamCallback answers one legacy stream's provider.callback calls: the request callbacks by their names, and the caller's fetch through the bounded fetch, fetchRead and fetchClose reads model_fetch.go uses in the other direction.
func newLegacyStreamCallback(ctx context.Context, options ai.StreamOptions, model *ai.Model, fetch *callerFetch) nativeProviderCallback {
	requestCallbacks := nativeStreamCallback(ctx, options, model)
	return func(raw json.RawMessage) (json.RawMessage, error) {
		var call struct {
			Method string `json:"method"`
			Params struct {
				Value json.RawMessage `json:"value"`
			} `json:"params"`
		}
		if err := json.Unmarshal(raw, &call); err != nil {
			return nil, err
		}
		switch call.Method {
		case "fetch", "fetchRead", "fetchClose":
			if fetch == nil {
				return nil, fmt.Errorf("Provider callback %s is absent", call.Method)
			}
			result, err := fetch.serve(ctx, call.Method, call.Params.Value)
			if err != nil {
				return nil, err
			}
			return json.Marshal(result)
		}
		return requestCallbacks(raw)
	}
}

// callerFetch runs fetches through the caller's transport for an extension, and holds each response body until the extension reads it to the end or closes it. Close ends every fetch still in flight.
type callerFetch struct {
	client  *http.Client
	ctx     context.Context
	mu      sync.Mutex
	closed  bool
	entries map[string]*callerFetchEntry
}

type callerFetchEntry struct {
	cancel context.CancelFunc
	body   io.ReadCloser
}

func newCallerFetch(ctx context.Context, client *http.Client) *callerFetch {
	if client == nil {
		return nil
	}
	return &callerFetch{client: client, ctx: ctx, entries: map[string]*callerFetchEntry{}}
}

var errCallerFetchClosed = errors.New("caller fetch transport is closed")

// serve answers one fetch, fetchRead or fetchClose. callbackCtx is the extension's call; cancelling it cancels a fetch or read in progress.
func (f *callerFetch) serve(callbackCtx context.Context, method string, value json.RawMessage) (any, error) {
	switch method {
	case "fetch":
		var request modelFetchRequest
		if err := json.Unmarshal(value, &request); err != nil {
			return nil, err
		}
		fetchCtx, cancel := context.WithCancel(f.ctx)
		var body io.Reader
		if request.Body != nil {
			body = bytes.NewReader(request.Body)
		}
		outgoing, err := http.NewRequestWithContext(fetchCtx, request.Method, request.URL, body)
		if err != nil {
			cancel()
			return nil, err
		}
		outgoing.Header = request.Headers.Clone()
		if outgoing.Header == nil {
			outgoing.Header = http.Header{}
		}
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			cancel()
			return nil, errCallerFetchClosed
		}
		entry := &callerFetchEntry{cancel: cancel}
		f.entries[request.ID] = entry
		f.mu.Unlock()
		stop := context.AfterFunc(callbackCtx, cancel)
		response, err := f.client.Do(outgoing)
		stop()
		hasBody := err == nil && response.Body != nil && response.Body != http.NoBody
		f.mu.Lock()
		closed := f.closed || fetchCtx.Err() != nil
		if err != nil || closed || !hasBody {
			if f.entries[request.ID] == entry {
				delete(f.entries, request.ID)
			}
		} else {
			entry.body = response.Body
		}
		f.mu.Unlock()
		if err != nil {
			cancel()
			return nil, err
		}
		if closed {
			if response.Body != nil {
				_ = response.Body.Close()
			}
			cancel()
			if cause := context.Cause(fetchCtx); cause != nil {
				return nil, cause
			}
			return nil, errCallerFetchClosed
		}
		if !hasBody {
			if response.Body != nil {
				_ = response.Body.Close()
			}
			cancel()
		}
		headers := make([][2]string, 0, len(response.Header))
		for name, values := range response.Header {
			for _, entry := range values {
				headers = append(headers, [2]string{name, entry})
			}
		}
		return modelFetchResponse{Status: response.StatusCode, StatusText: callerFetchStatusText(response), Headers: headers, Body: hasBody}, nil
	case "fetchRead":
		var read modelFetchRead
		if err := json.Unmarshal(value, &read); err != nil {
			return nil, err
		}
		if read.Size < 1 || read.Size > 64*1024 {
			return nil, errors.New("invalid caller fetch read size")
		}
		f.mu.Lock()
		entry := f.entries[read.ID]
		f.mu.Unlock()
		if entry == nil || entry.body == nil {
			return nil, fmt.Errorf("unknown caller fetch response %s", read.ID)
		}
		stop := context.AfterFunc(callbackCtx, entry.cancel)
		defer stop()
		buffer := make([]byte, read.Size)
		n, err := entry.body.Read(buffer)
		for n == 0 && err == nil {
			n, err = entry.body.Read(buffer)
		}
		if err != nil && err != io.EOF {
			return nil, err
		}
		return modelFetchChunk{Data: buffer[:n], Done: err == io.EOF}, nil
	default: // fetchClose
		var closeCall struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(value, &closeCall); err != nil {
			return nil, err
		}
		f.mu.Lock()
		entry := f.entries[closeCall.ID]
		delete(f.entries, closeCall.ID)
		f.mu.Unlock()
		if entry == nil {
			return nil, nil
		}
		entry.cancel()
		if entry.body == nil {
			return nil, nil
		}
		return nil, entry.body.Close()
	}
}

// close ends every fetch still in flight and closes the response bodies the extension did not close.
func (f *callerFetch) close() {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.closed = true
	entries := f.entries
	f.entries = map[string]*callerFetchEntry{}
	f.mu.Unlock()
	for _, entry := range entries {
		entry.cancel()
		if entry.body != nil {
			_ = entry.body.Close()
		}
	}
}

// callerFetchStatusText is the reason phrase of the response's Status, as a fetch Response's statusText.
func callerFetchStatusText(response *http.Response) string {
	code := strconv.Itoa(response.StatusCode)
	if text, ok := strings.CutPrefix(response.Status, code+" "); ok {
		return text
	}
	if response.Status == code {
		return ""
	}
	return response.Status
}
