package fal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// testClient builds a Client with a static key whose run and queue base URLs
// point at the given test server.
func testClient(baseURL string) *Client {
	return NewClient(
		WithKey("test-key"),
		WithRunURL(baseURL),
		WithQueueURL(baseURL),
	)
}

func TestRunSuccess(t *testing.T) {
	var gotPath, gotAuth, gotContentType, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"image":"data"}`))
	}))
	defer srv.Close()

	c := testClient(srv.URL)
	got, err := c.Run(context.Background(), "fal-ai/flux", map[string]string{"prompt": "cat"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if gotPath != "/fal-ai/flux" {
		t.Errorf("path = %q, want /fal-ai/flux", gotPath)
	}
	if gotAuth != "Key test-key" {
		t.Errorf("Authorization = %q, want 'Key test-key'", gotAuth)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotBody != `{"prompt":"cat"}` {
		t.Errorf("body = %q, want {\"prompt\":\"cat\"}", gotBody)
	}
	var out map[string]string
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if out["image"] != "data" {
		t.Errorf("result image = %q, want data", out["image"])
	}
}

func TestRunWithPathAndHeaders(t *testing.T) {
	var gotPath, gotHint, gotTimeout string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHint = r.Header.Get("X-Fal-Runner-Hint")
		gotTimeout = r.Header.Get("X-Fal-Request-Timeout")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := testClient(srv.URL)
	_, err := c.Run(context.Background(), "fal-ai/flux", nil,
		WithPath("dev"),
		WithHint("runner-7"),
		WithStartTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if gotPath != "/fal-ai/flux/dev" {
		t.Errorf("path = %q, want /fal-ai/flux/dev", gotPath)
	}
	if gotHint != "runner-7" {
		t.Errorf("X-Fal-Runner-Hint = %q, want runner-7", gotHint)
	}
	if gotTimeout != "5" {
		t.Errorf("X-Fal-Request-Timeout = %q, want 5", gotTimeout)
	}
}

func TestRunStartTimeoutValidation(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := testClient(srv.URL)
	_, err := c.Run(context.Background(), "fal-ai/flux", nil, WithStartTimeout(500*time.Millisecond))
	if err == nil {
		t.Fatal("Run with sub-1s start timeout: want error, got nil")
	}
	if hits != 0 {
		t.Errorf("server hit %d times, want 0 (request must not be sent)", hits)
	}
}

func TestRunAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Fal-Error-Type", "ValidationError")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"detail":"bad input"}`))
	}))
	defer srv.Close()

	c := testClient(srv.URL)
	_, err := c.Run(context.Background(), "fal-ai/flux", nil)
	if err == nil {
		t.Fatal("Run: want error, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("StatusCode = %d, want 422", apiErr.StatusCode)
	}
	if apiErr.Message != "bad input" {
		t.Errorf("Message = %q, want 'bad input'", apiErr.Message)
	}
	if apiErr.ErrorType != "ValidationError" {
		t.Errorf("ErrorType = %q, want ValidationError", apiErr.ErrorType)
	}
}
