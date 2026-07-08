package fal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// closeRecorder wraps a response body and records whether Close was called.
type closeRecorder struct {
	io.ReadCloser
	closed atomic.Bool
}

func (c *closeRecorder) Close() error {
	c.closed.Store(true)
	return c.ReadCloser.Close()
}

// recordingTransport delegates to a base RoundTripper and replaces each response
// body with a closeRecorder so tests can assert the body is closed.
type recordingTransport struct {
	base http.RoundTripper
	rec  *closeRecorder
}

func (t *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	t.rec.ReadCloser = resp.Body
	resp.Body = t.rec
	return resp, nil
}

// newRecordingClient builds a client aimed at srv whose response bodies are
// wrapped in the returned closeRecorder.
func newRecordingClient(t *testing.T, srv *httptest.Server) (*Client, *closeRecorder) {
	t.Helper()
	rec := &closeRecorder{}
	hc := &http.Client{Transport: &recordingTransport{base: http.DefaultTransport, rec: rec}}
	c := NewClient(WithKey("test-key"), WithRunURL(srv.URL), WithHTTPClient(hc))
	return c, rec
}

func writeSSE(t *testing.T, w http.ResponseWriter, data string) {
	t.Helper()
	flusher, ok := w.(http.Flusher)
	if !ok {
		t.Fatal("ResponseWriter does not support http.Flusher")
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		t.Fatalf("writing SSE event: %v", err)
	}
	flusher.Flush()
}

func TestStreamRequestShape(t *testing.T) {
	tests := []struct {
		name     string
		opts     []StreamOption
		wantPath string
	}{
		{name: "default path", wantPath: "/fal-ai/flux/stream"},
		{name: "custom path", opts: []StreamOption{WithStreamPath("/stream/v2")}, wantPath: "/fal-ai/flux/stream/v2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				gotMethod string
				gotPath   string
				gotAccept string
				gotAuth   string
				gotBody   string
			)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				gotAccept = r.Header.Get("Accept")
				gotAuth = r.Header.Get("Authorization")
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("reading request body: %v", err)
				}
				gotBody = string(body)
				w.Header().Set("Content-Type", "text/event-stream")
				writeSSE(t, w, `{"ok":true}`)
			}))
			defer srv.Close()

			c := NewClient(WithKey("test-key"), WithRunURL(srv.URL))
			input := map[string]any{"prompt": "hello"}

			var count int
			for _, err := range c.Stream(context.Background(), "fal-ai/flux", input, tt.opts...) {
				if err != nil {
					t.Fatalf("stream error: %v", err)
				}
				count++
			}

			if count != 1 {
				t.Errorf("event count = %d, want 1", count)
			}
			if gotMethod != http.MethodPost {
				t.Errorf("method = %q, want POST", gotMethod)
			}
			if gotPath != tt.wantPath {
				t.Errorf("path = %q, want %q", gotPath, tt.wantPath)
			}
			if gotAccept != "text/event-stream" {
				t.Errorf("Accept = %q, want text/event-stream", gotAccept)
			}
			if gotAuth != "Key test-key" {
				t.Errorf("Authorization = %q, want Key test-key", gotAuth)
			}
			if gotBody != `{"prompt":"hello"}` {
				t.Errorf("body = %q, want {\"prompt\":\"hello\"}", gotBody)
			}
		})
	}
}

func TestStreamIncrementalOrder(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(t, w, `{"n":1}`)
		<-release // block until the consumer has received the first event
		writeSSE(t, w, `{"n":2}`)
	}))
	defer srv.Close()

	c := NewClient(WithKey("test-key"), WithRunURL(srv.URL))

	var got []string
	for data, err := range c.Stream(context.Background(), "fal-ai/flux", nil) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		got = append(got, string(data))
		if len(got) == 1 {
			// The server only writes the second event after this unblocks,
			// proving events are delivered incrementally.
			close(release)
		}
	}

	want := []string{`{"n":1}`, `{"n":2}`}
	if len(got) != len(want) {
		t.Fatalf("got %d events %q, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestStreamContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(t, w, `{"n":1}`)
		<-r.Context().Done() // hold the stream open until the client cancels
	}))
	defer srv.Close()

	c, rec := newRecordingClient(t, srv)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		gotErr error
		count  int
	)
	for data, err := range c.Stream(ctx, "fal-ai/flux", nil) {
		if err != nil {
			gotErr = err
			break
		}
		_ = data
		count++
		cancel() // cancel after the first event
	}

	if count != 1 {
		t.Fatalf("received %d events before cancel, want 1", count)
	}
	if !errors.Is(gotErr, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", gotErr)
	}
	if !rec.closed.Load() {
		t.Error("response body was not closed after cancellation")
	}
}

func TestStreamEarlyBreakClosesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(t, w, `{"n":1}`)
		writeSSE(t, w, `{"n":2}`)
		writeSSE(t, w, `{"n":3}`)
	}))
	defer srv.Close()

	c, rec := newRecordingClient(t, srv)

	var count int
	for data, err := range c.Stream(context.Background(), "fal-ai/flux", nil) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		_ = data
		count++
		break // consumer stops after the first event
	}

	if count != 1 {
		t.Fatalf("received %d events, want 1", count)
	}
	if !rec.closed.Load() {
		t.Error("response body was not closed after early break")
	}
}

func TestStreamNon2xxYieldsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		if _, err := io.WriteString(w, `{"detail":"bad input","error_type":"validation"}`); err != nil {
			t.Fatalf("writing error body: %v", err)
		}
	}))
	defer srv.Close()

	c := NewClient(WithKey("test-key"), WithRunURL(srv.URL))

	var (
		gotErr error
		count  int
	)
	for _, err := range c.Stream(context.Background(), "fal-ai/flux", nil) {
		if err != nil {
			gotErr = err
			continue
		}
		count++
	}

	if count != 0 {
		t.Errorf("event count = %d, want 0", count)
	}
	var apiErr *APIError
	if !errors.As(gotErr, &apiErr) {
		t.Fatalf("error = %v, want *APIError", gotErr)
	}
	if apiErr.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("StatusCode = %d, want 422", apiErr.StatusCode)
	}
	if apiErr.Message != "bad input" {
		t.Errorf("Message = %q, want %q", apiErr.Message, "bad input")
	}
	if apiErr.ErrorType != "validation" {
		t.Errorf("ErrorType = %q, want validation", apiErr.ErrorType)
	}
}

func TestStreamMarshalError(t *testing.T) {
	c := NewClient(WithKey("test-key"))
	var gotErr error
	// A channel value cannot be marshaled to JSON.
	for _, err := range c.Stream(context.Background(), "fal-ai/flux", make(chan int)) {
		if err != nil {
			gotErr = err
			break
		}
		t.Fatal("did not expect an event when input cannot be marshaled")
	}
	if gotErr == nil {
		t.Fatal("expected a marshal error")
	}
}

func TestStreamTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // close immediately so the address refuses connections

	c := NewClient(WithKey("test-key"), WithRunURL(url))

	var gotErr error
	for _, err := range c.Stream(context.Background(), "fal-ai/flux", nil) {
		if err != nil {
			gotErr = err
			break
		}
		t.Fatal("did not expect an event from an unreachable server")
	}
	if gotErr == nil {
		t.Fatal("expected a transport error")
	}
	if errors.Is(gotErr, context.Canceled) {
		t.Errorf("transport error should not be a context error: %v", gotErr)
	}
}

func TestStreamInvalidAppID(t *testing.T) {
	c := NewClient(WithKey("test-key"))
	var gotErr error
	for _, err := range c.Stream(context.Background(), "not-a-valid-id", nil) {
		if err != nil {
			gotErr = err
			break
		}
		t.Fatal("did not expect an event for an invalid app id")
	}
	if gotErr == nil {
		t.Fatal("expected an error for an invalid app id")
	}
}
