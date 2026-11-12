package sdk

// Ports packages/coding-agent/src/core/model-runtime.ts (the provider composer passes the caller's request options, function members included, to an extension's streamSimple).
// Ports packages/ai/src/types.ts (ProviderRequestOptions.onPayload and fetch, StreamOptions.onResponse and onProviderStreamEvent).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
)

// legacyStreamCallbacks puts the caller's request callbacks a provider_stream_simple request names into options, as the function members Pi's composer passes to streamSimple: "onPayload" an [OnPayloadFunc], "onResponse" an [OnResponseFunc], "onProviderStreamEvent" an [OnProviderStreamEventFunc] and "fetch" a [ModelFetchFunc]. Each calls the caller in the host through provider.callback, with ctx's request as its parent, so it works only while the request runs.
func legacyStreamCallbacks(ctx Context, provider string, names []string, options map[string]any) {
	call := func(method string, params any) (json.RawMessage, error) {
		return providerCallbackValue[json.RawMessage](ctx, provider, method, params)
	}
	for _, name := range names {
		switch name {
		case "onPayload":
			options[name] = OnPayloadFunc(func(payload any, model map[string]any) (any, error) {
				raw, err := call(name, map[string]any{"value": payload, "model": model})
				if err != nil {
					return nil, err
				}
				var replacement any
				if err := json.Unmarshal(raw, &replacement); err != nil {
					return nil, err
				}
				return replacement, nil
			})
		case "onResponse":
			options[name] = OnResponseFunc(func(response, model map[string]any) error {
				_, err := call(name, map[string]any{"value": response, "model": model})
				return err
			})
		case "onProviderStreamEvent":
			options[name] = OnProviderStreamEventFunc(func(data any, model map[string]any) error {
				_, err := call(name, map[string]any{"value": data, "model": model})
				return err
			})
		case "fetch":
			options[name] = callerFetchFunc(call)
		}
	}
}

// callerFetchFunc is the caller's fetch as a [ModelFetchFunc]: the host runs each request through the caller's transport, and the response body streams back in bounded reads.
func callerFetchFunc(call func(string, any) (json.RawMessage, error)) ModelFetchFunc {
	var next atomic.Uint64
	return func(request *http.Request) (*http.Response, error) {
		id := strconv.FormatUint(next.Add(1), 10)
		value := map[string]any{"id": id, "url": request.URL.String(), "method": request.Method, "headers": request.Header}
		if request.Body != nil {
			body, err := io.ReadAll(request.Body)
			closeErr := request.Body.Close()
			if err != nil {
				return nil, err
			}
			if closeErr != nil {
				return nil, closeErr
			}
			value["body"] = body
		}
		if err := request.Context().Err(); err != nil {
			return nil, err
		}
		raw, err := call("fetch", map[string]any{"value": value})
		if err != nil {
			return nil, err
		}
		var result struct {
			Status     int         `json:"status"`
			StatusText string      `json:"statusText"`
			Headers    [][2]string `json:"headers"`
			Body       bool        `json:"body"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("decode caller fetch response: %w", err)
		}
		header := make(http.Header)
		for _, pair := range result.Headers {
			header.Add(pair[0], pair[1])
		}
		var body io.ReadCloser = http.NoBody
		if result.Body {
			body = &callerFetchBody{call: call, id: id}
		}
		status := strconv.Itoa(result.Status)
		if result.StatusText != "" {
			status += " " + result.StatusText
		}
		return &http.Response{StatusCode: result.Status, Status: status, Header: header, Body: body, ContentLength: -1, Request: request}, nil
	}
}

// callerFetchBody pulls only what its reader asks for; Close releases the host's reader.
type callerFetchBody struct {
	call      func(string, any) (json.RawMessage, error)
	id        string
	done      bool
	closeOnce sync.Once
	closeErr  error
}

func (b *callerFetchBody) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if b.done {
		return 0, io.EOF
	}
	size := min(len(buffer), 64*1024)
	raw, err := b.call("fetchRead", map[string]any{"value": map[string]any{"id": b.id, "size": size}})
	if err != nil {
		return 0, err
	}
	var chunk struct {
		Data []byte `json:"data"`
		Done bool   `json:"done"`
	}
	if err := json.Unmarshal(raw, &chunk); err != nil {
		return 0, fmt.Errorf("decode caller fetch body: %w", err)
	}
	if len(chunk.Data) > size {
		return 0, fmt.Errorf("caller fetch body returned %d bytes for a %d-byte read", len(chunk.Data), size)
	}
	b.done = chunk.Done
	n := copy(buffer, chunk.Data)
	if b.done {
		return n, io.EOF
	}
	return n, nil
}

func (b *callerFetchBody) Close() error {
	b.closeOnce.Do(func() {
		_, b.closeErr = b.call("fetchClose", map[string]any{"value": map[string]any{"id": b.id}})
	})
	return b.closeErr
}

// cancelled reports whether ctx is done; a nil ctx never is.
func cancelled(ctx context.Context) bool { return ctx != nil && ctx.Err() != nil }
