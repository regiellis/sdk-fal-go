package fal

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestShouldRetryResponse(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		headers http.Header
		body    string
		want    bool
	}{
		{"408 retry", 408, nil, "", true},
		{"409 retry", 409, nil, "", true},
		{"429 retry", 429, nil, "", true},
		{"200 no retry", 200, nil, "", false},
		{"400 no retry", 400, nil, "", false},
		{"500 no retry", 500, nil, "", false},
		{"502 ingress nginx retry", 502, nil, "<html>nginx</html>", true},
		{"503 ingress nginx retry", 503, nil, "bad gateway from nginx", true},
		{"504 ingress nginx retry", 504, nil, "nginx", true},
		{"502 with request id no retry", 502, http.Header{headerFalRequestID: {"abc"}}, "nginx", false},
		{"502 without nginx no retry", 502, nil, "application error", false},
		{"504 with timeout type no retry", 504, http.Header{headerRequestTimeoutType: {"server"}}, "nginx", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := tt.headers
			if header == nil {
				header = http.Header{}
			}
			resp := &http.Response{StatusCode: tt.status, Header: header}
			if got := shouldRetryResponse(resp, []byte(tt.body)); got != tt.want {
				t.Fatalf("shouldRetryResponse = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRetryDelayBounds(t *testing.T) {
	for attempt := 1; attempt <= 12; attempt++ {
		base := retryBaseSecs * pow2(attempt-1)
		if base > retryMaxSecs {
			base = retryMaxSecs
		}
		lower := time.Duration(base * 0.5 * float64(time.Second))
		upper := time.Duration(retryMaxSecs * float64(time.Second))

		// Sample several times because of jitter.
		for range 50 {
			d := retryDelay(attempt)
			if d < lower {
				t.Fatalf("attempt %d: delay %v below lower bound %v", attempt, d, lower)
			}
			if d > upper {
				t.Fatalf("attempt %d: delay %v above max %v", attempt, d, upper)
			}
		}
	}
}

func pow2(n int) float64 {
	v := 1.0
	for range n {
		v *= 2
	}
	return v
}

func TestDoRetriesThenSucceedsWithReplayableBody(t *testing.T) {
	var attempts atomic.Int32
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := NewClient(WithHTTPClient(srv.Client()))
	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}

	resp, err := c.do(context.Background(), req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", attempts.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	for i, b := range bodies {
		if b != "payload" {
			t.Fatalf("attempt %d body = %q, want payload", i+1, b)
		}
	}
	got, _ := io.ReadAll(resp.Body)
	if string(got) != "ok" {
		t.Fatalf("response body = %q, want ok", got)
	}
}

func TestDoReturnsNonRetryableResponse(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("nope"))
	}))
	defer srv.Close()

	c := NewClient(WithHTTPClient(srv.Client()))
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := c.do(context.Background(), req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1 (no retry)", attempts.Load())
	}
}

func TestDoContextCancellationMidRetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests) // always retryable
	}))
	defer srv.Close()

	c := NewClient(WithHTTPClient(srv.Client()))
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := c.do(ctx, req)
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("do took %v, expected prompt cancellation", elapsed)
	}
}

// bodyNoGetBody wraps a reader so http.NewRequest cannot auto-install GetBody,
// forcing do to buffer the body itself for replay.
type bodyNoGetBody struct{ r io.Reader }

func (b *bodyNoGetBody) Read(p []byte) (int, error) { return b.r.Read(p) }

func TestDoBuffersBodyWithoutGetBody(t *testing.T) {
	var attempts atomic.Int32
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusConflict) // retryable
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(WithHTTPClient(srv.Client()))
	req, err := http.NewRequest(http.MethodPost, srv.URL, &bodyNoGetBody{r: strings.NewReader("data")})
	if err != nil {
		t.Fatal(err)
	}
	if req.GetBody != nil {
		t.Fatal("precondition: expected GetBody to be nil")
	}

	resp, err := c.do(context.Background(), req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", attempts.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	for i, b := range bodies {
		if b != "data" {
			t.Fatalf("attempt %d body = %q, want data", i+1, b)
		}
	}
}

// errRoundTripper always fails, counting invocations.
type errRoundTripper struct {
	calls atomic.Int32
	err   error
}

func (rt *errRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	rt.calls.Add(1)
	return nil, rt.err
}

func TestDoDoesNotRetryTransportErrorWhenTimeoutHeaderSet(t *testing.T) {
	rt := &errRoundTripper{err: errors.New("boom")}
	c := NewClient(WithHTTPClient(&http.Client{Transport: rt}))

	req, _ := http.NewRequest(http.MethodGet, "http://example.invalid", nil)
	req.Header.Set(headerRequestTimeout, "5")

	_, err := c.do(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
	if rt.calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1 (no retry when user timeout set)", rt.calls.Load())
	}
}

func TestDoAbortsWhenContextAlreadyCanceled(t *testing.T) {
	rt := &errRoundTripper{err: errors.New("boom")}
	c := NewClient(WithHTTPClient(&http.Client{Transport: rt}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req, _ := http.NewRequest(http.MethodGet, "http://example.invalid", nil)
	_, err := c.do(ctx, req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if rt.calls.Load() != 0 {
		t.Fatalf("calls = %d, want 0", rt.calls.Load())
	}
}
