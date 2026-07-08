package fal

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// writeJSON writes v as a JSON response body.
func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("writing json response: %v", err)
	}
}

func TestUploadV3SingleShot(t *testing.T) {
	var (
		mu            sync.Mutex
		baseURL       string
		gotAuth       string
		gotUA         string
		gotCT         string
		gotFileName   string
		gotLifecycle  string
		gotLifePref   string
		gotBody       []byte
		gotTokenAuth  string
		tokenRequests int
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/storage/auth/token":
			mu.Lock()
			tokenRequests++
			gotTokenAuth = r.Header.Get("Authorization")
			bu := baseURL
			mu.Unlock()
			if r.URL.Query().Get("storage_type") != "fal-cdn-v3" {
				t.Errorf("token storage_type = %q, want fal-cdn-v3", r.URL.Query().Get("storage_type"))
			}
			writeJSON(t, w, map[string]any{
				"token":      "cdn-token",
				"token_type": "Bearer",
				"base_url":   bu,
				"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
			})
		case "/files/upload":
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			gotAuth = r.Header.Get("Authorization")
			gotUA = r.Header.Get("User-Agent")
			gotCT = r.Header.Get("Content-Type")
			gotFileName = r.Header.Get("X-Fal-File-Name")
			gotLifecycle = r.Header.Get("X-Fal-Object-Lifecycle")
			gotLifePref = r.Header.Get("X-Fal-Object-Lifecycle-Preference")
			gotBody = body
			mu.Unlock()
			writeJSON(t, w, map[string]string{"access_url": "https://cdn.example/file.bin"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	mu.Lock()
	baseURL = srv.URL
	mu.Unlock()

	c := NewClient(WithKey("acct-key"), WithRestURL(srv.URL))

	payload := []byte("the quick brown fox")
	url, err := c.Upload(context.Background(), payload, "application/pdf",
		WithFileName("doc.pdf"),
		WithLifecycle(StorageSettings{
			ExpiresIn: 90 * time.Second,
			InitialACL: &StorageACL{
				Default: ACLAllow,
				Rules:   []ACLRule{{User: "u1", Decision: ACLHide}},
			},
		}),
	)
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	if url != "https://cdn.example/file.bin" {
		t.Errorf("Upload() url = %q", url)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotTokenAuth != "Key acct-key" {
		t.Errorf("token request auth = %q, want account key", gotTokenAuth)
	}
	if gotAuth != "Bearer cdn-token" {
		t.Errorf("upload auth = %q, want CDN token auth", gotAuth)
	}
	if gotUA != userAgent {
		t.Errorf("upload user-agent = %q, want %q", gotUA, userAgent)
	}
	if gotCT != "application/pdf" {
		t.Errorf("upload content-type = %q", gotCT)
	}
	if gotFileName != "doc.pdf" {
		t.Errorf("upload X-Fal-File-Name = %q", gotFileName)
	}
	wantLifecycle := `{"expiration_duration_seconds":90,"initial_acl":{"default":"allow","rules":[{"user":"u1","decision":"hide"}]}}`
	if gotLifecycle != wantLifecycle {
		t.Errorf("X-Fal-Object-Lifecycle = %q, want %q", gotLifecycle, wantLifecycle)
	}
	if gotLifePref != wantLifecycle {
		t.Errorf("X-Fal-Object-Lifecycle-Preference = %q, want %q", gotLifePref, wantLifecycle)
	}
	if string(gotBody) != string(payload) {
		t.Errorf("upload body = %q, want %q", gotBody, payload)
	}
	if tokenRequests != 1 {
		t.Errorf("token requests = %d, want 1", tokenRequests)
	}
}

func TestUploadFileSingleShot(t *testing.T) {
	var (
		mu          sync.Mutex
		baseURL     string
		gotCT       string
		gotFileName string
		gotBody     []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/storage/auth/token":
			mu.Lock()
			bu := baseURL
			mu.Unlock()
			writeJSON(t, w, map[string]any{
				"token":      "cdn-token",
				"token_type": "Bearer",
				"base_url":   bu,
				"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
			})
		case "/files/upload":
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			gotCT = r.Header.Get("Content-Type")
			gotFileName = r.Header.Get("X-Fal-File-Name")
			gotBody = body
			mu.Unlock()
			writeJSON(t, w, map[string]string{"access_url": "https://cdn.example/up.png"})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	mu.Lock()
	baseURL = srv.URL
	mu.Unlock()

	data := []byte{0x89, 0x50, 0x4e, 0x47, 0x01, 0x02, 0x03}
	path := filepath.Join(t.TempDir(), "picture.png")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	c := NewClient(WithKey("acct-key"), WithRestURL(srv.URL))
	url, err := c.UploadFile(context.Background(), path)
	if err != nil {
		t.Fatalf("UploadFile() error = %v", err)
	}
	if url != "https://cdn.example/up.png" {
		t.Errorf("UploadFile() url = %q", url)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotCT != "image/png" {
		t.Errorf("content-type = %q, want image/png (guessed from extension)", gotCT)
	}
	if gotFileName != "picture.png" {
		t.Errorf("X-Fal-File-Name = %q, want the file base name", gotFileName)
	}
	if string(gotBody) != string(data) {
		t.Errorf("uploaded body = %q, want %q", gotBody, data)
	}
}

func TestUploadTokenCachedAcrossConcurrentCalls(t *testing.T) {
	var (
		mu         sync.Mutex
		baseURL    string
		tokenCount int32
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/storage/auth/token":
			atomic.AddInt32(&tokenCount, 1)
			mu.Lock()
			bu := baseURL
			mu.Unlock()
			writeJSON(t, w, map[string]any{
				"token":      "cdn-token",
				"token_type": "Bearer",
				"base_url":   bu,
				"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
			})
		case "/files/upload":
			_, _ = io.Copy(io.Discard, r.Body)
			writeJSON(t, w, map[string]string{"access_url": "https://cdn.example/file.bin"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	mu.Lock()
	baseURL = srv.URL
	mu.Unlock()

	c := NewClient(WithKey("acct-key"), WithRestURL(srv.URL))

	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Upload(context.Background(), []byte("data"), "text/plain"); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Upload() error = %v", err)
	}

	if got := atomic.LoadInt32(&tokenCount); got != 1 {
		t.Errorf("token fetches = %d, want 1 (cached)", got)
	}
}

func TestUploadTokenRefreshOnExpiry(t *testing.T) {
	var (
		mu         sync.Mutex
		baseURL    string
		tokenCount int32
	)
	reference := time.Now()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/storage/auth/token":
			atomic.AddInt32(&tokenCount, 1)
			mu.Lock()
			bu := baseURL
			mu.Unlock()
			writeJSON(t, w, map[string]any{
				"token":      "cdn-token",
				"token_type": "Bearer",
				"base_url":   bu,
				"expires_at": reference.Add(10 * time.Second).Format(time.RFC3339),
			})
		case "/files/upload":
			_, _ = io.Copy(io.Discard, r.Body)
			writeJSON(t, w, map[string]string{"access_url": "https://cdn.example/file.bin"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	mu.Lock()
	baseURL = srv.URL
	mu.Unlock()

	c := NewClient(WithKey("acct-key"), WithRestURL(srv.URL))

	// Install a controllable clock before the first token fetch.
	var nowNanos atomic.Int64
	nowNanos.Store(reference.UnixNano())
	mgr := c.v3Tokens()
	mgr.now = func() time.Time { return time.Unix(0, nowNanos.Load()) }

	ctx := context.Background()
	if _, err := c.Upload(ctx, []byte("a"), "text/plain"); err != nil {
		t.Fatalf("Upload() 1 error = %v", err)
	}
	if _, err := c.Upload(ctx, []byte("b"), "text/plain"); err != nil {
		t.Fatalf("Upload() 2 error = %v", err)
	}
	if got := atomic.LoadInt32(&tokenCount); got != 1 {
		t.Fatalf("token fetches before expiry = %d, want 1", got)
	}

	// Advance past expiry; the next upload must refresh.
	nowNanos.Store(reference.Add(30 * time.Second).UnixNano())
	if _, err := c.Upload(ctx, []byte("c"), "text/plain"); err != nil {
		t.Fatalf("Upload() 3 error = %v", err)
	}
	if got := atomic.LoadInt32(&tokenCount); got != 2 {
		t.Errorf("token fetches after expiry = %d, want 2", got)
	}
}

func TestUploadLegacy(t *testing.T) {
	var (
		gotInitiateAuth string
		gotInitiateBody []byte
		gotLifecycle    string
		gotPutAuth      string
		gotPutCT        string
		gotPutBody      []byte
		putTarget       string
		mu              sync.Mutex
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/storage/upload/initiate":
			if r.URL.Query().Get("storage_type") != "gcs" {
				t.Errorf("initiate storage_type = %q, want gcs", r.URL.Query().Get("storage_type"))
			}
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			gotInitiateAuth = r.Header.Get("Authorization")
			gotInitiateBody = body
			gotLifecycle = r.Header.Get("X-Fal-Object-Lifecycle")
			pt := putTarget
			mu.Unlock()
			writeJSON(t, w, map[string]string{
				"upload_url": pt,
				"file_url":   "https://storage.example/final.bin",
			})
		case "/put-target":
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			gotPutAuth = r.Header.Get("Authorization")
			gotPutCT = r.Header.Get("Content-Type")
			gotPutBody = body
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	mu.Lock()
	putTarget = srv.URL + "/put-target"
	mu.Unlock()

	c := NewClient(WithKey("acct-key"), WithRestURL(srv.URL))

	payload := []byte("legacy payload")
	url, err := c.Upload(context.Background(), payload, "text/plain",
		WithRepository(RepositoryLegacy),
		WithFileName("note.txt"),
		WithLifecycle(StorageSettings{ExpiresIn: 60 * time.Second}),
	)
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	if url != "https://storage.example/final.bin" {
		t.Errorf("Upload() url = %q", url)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotInitiateAuth != "Key acct-key" {
		t.Errorf("initiate auth = %q, want account key", gotInitiateAuth)
	}
	var initiated struct {
		FileName    string `json:"file_name"`
		ContentType string `json:"content_type"`
	}
	if err := json.Unmarshal(gotInitiateBody, &initiated); err != nil {
		t.Fatalf("initiate body: %v", err)
	}
	if initiated.FileName != "note.txt" || initiated.ContentType != "text/plain" {
		t.Errorf("initiate body = %+v", initiated)
	}
	if gotLifecycle != `{"expiration_duration_seconds":60}` {
		t.Errorf("legacy lifecycle header = %q", gotLifecycle)
	}
	if gotPutAuth != "" {
		t.Errorf("presigned PUT carried Authorization = %q, want none", gotPutAuth)
	}
	if gotPutCT != "text/plain" {
		t.Errorf("PUT content-type = %q", gotPutCT)
	}
	if string(gotPutBody) != string(payload) {
		t.Errorf("PUT body = %q, want %q", gotPutBody, payload)
	}
}

func TestUploadFallbackOrdering(t *testing.T) {
	t.Run("v3 fails then legacy succeeds", func(t *testing.T) {
		var putTarget string
		var mu sync.Mutex
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/storage/auth/token":
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"detail":"v3 down"}`))
			case "/storage/upload/initiate":
				mu.Lock()
				pt := putTarget
				mu.Unlock()
				writeJSON(t, w, map[string]string{"upload_url": pt, "file_url": "https://storage.example/ok.bin"})
			case "/put-target":
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(http.StatusOK)
			default:
				t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			}
		}))
		defer srv.Close()
		mu.Lock()
		putTarget = srv.URL + "/put-target"
		mu.Unlock()

		c := NewClient(WithKey("acct-key"), WithRestURL(srv.URL))
		url, err := c.Upload(context.Background(), []byte("x"), "text/plain")
		if err != nil {
			t.Fatalf("Upload() error = %v", err)
		}
		if url != "https://storage.example/ok.bin" {
			t.Errorf("Upload() url = %q, want legacy result", url)
		}
	})

	t.Run("both fail returns last error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/storage/auth/token":
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"detail":"v3 down"}`))
			case "/storage/upload/initiate":
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"detail":"legacy rejected"}`))
			default:
				t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			}
		}))
		defer srv.Close()

		c := NewClient(WithKey("acct-key"), WithRestURL(srv.URL))
		_, err := c.Upload(context.Background(), []byte("x"), "text/plain")
		if err == nil {
			t.Fatal("Upload() expected error when all repositories fail")
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("error = %v, want *APIError", err)
		}
		if apiErr.StatusCode != http.StatusBadRequest {
			t.Errorf("last error status = %d, want 400 (legacy, the last in chain)", apiErr.StatusCode)
		}
	})
}

func TestUploadRepositoryValidation(t *testing.T) {
	c := NewClient(WithKey("acct-key"))
	tests := []struct {
		name string
		opts []UploadOption
	}{
		{"unknown primary", []UploadOption{WithRepository("bogus")}},
		{"unknown fallback", []UploadOption{WithFallback(RepositoryLegacy, "nope")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := c.Upload(context.Background(), []byte("x"), "text/plain", tt.opts...)
			if err == nil {
				t.Fatal("expected repository validation error")
			}
			if !strings.Contains(err.Error(), "unknown repository") {
				t.Errorf("error = %v, want unknown repository", err)
			}
		})
	}
}

func TestRepositoriesDedupe(t *testing.T) {
	cfg := newUploadConfig([]UploadOption{
		WithRepository(RepositoryV3),
		WithFallback(RepositoryV3, RepositoryLegacy, RepositoryLegacy),
	})
	chain, err := cfg.repositories()
	if err != nil {
		t.Fatalf("repositories() error = %v", err)
	}
	want := []Repository{RepositoryV3, RepositoryLegacy}
	if len(chain) != len(want) {
		t.Fatalf("chain = %v, want %v", chain, want)
	}
	for i := range want {
		if chain[i] != want[i] {
			t.Errorf("chain[%d] = %q, want %q", i, chain[i], want[i])
		}
	}
}

func TestRepositoriesDefaultChain(t *testing.T) {
	cfg := newUploadConfig(nil)
	chain, err := cfg.repositories()
	if err != nil {
		t.Fatalf("repositories() error = %v", err)
	}
	if len(chain) != 2 || chain[0] != RepositoryV3 || chain[1] != RepositoryLegacy {
		t.Errorf("default chain = %v, want [fal_v3 fal]", chain)
	}
}

func TestLifecycleExpirationMustBePositive(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example/upload", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	// 400ms rounds to 0 seconds, which is not positive.
	err = applyLifecycleHeaders(req, &StorageSettings{ExpiresIn: 400 * time.Millisecond})
	if err == nil {
		t.Fatal("expected error for sub-second expiration")
	}
	if !strings.Contains(err.Error(), "must be positive") {
		t.Errorf("error = %v", err)
	}
}

func TestUploadImageRoundTrip(t *testing.T) {
	var (
		mu      sync.Mutex
		baseURL string
		gotBody []byte
		gotCT   string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/storage/auth/token":
			mu.Lock()
			bu := baseURL
			mu.Unlock()
			writeJSON(t, w, map[string]any{
				"token":      "cdn-token",
				"token_type": "Bearer",
				"base_url":   bu,
				"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
			})
		case "/files/upload":
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			gotBody = body
			gotCT = r.Header.Get("Content-Type")
			mu.Unlock()
			writeJSON(t, w, map[string]string{"access_url": "https://cdn.example/img.png"})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	mu.Lock()
	baseURL = srv.URL
	mu.Unlock()

	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(2, 1, color.RGBA{B: 255, A: 255})

	c := NewClient(WithKey("acct-key"), WithRestURL(srv.URL))
	url, err := c.UploadImage(context.Background(), img, WithImageFormat(ImageFormatPNG))
	if err != nil {
		t.Fatalf("UploadImage() error = %v", err)
	}
	if url != "https://cdn.example/img.png" {
		t.Errorf("UploadImage() url = %q", url)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotCT != "image/png" {
		t.Errorf("content-type = %q, want image/png", gotCT)
	}
	decoded, err := png.Decode(strings.NewReader(string(gotBody)))
	if err != nil {
		t.Fatalf("uploaded bytes are not valid PNG: %v", err)
	}
	if b := decoded.Bounds(); b.Dx() != 3 || b.Dy() != 2 {
		t.Errorf("decoded bounds = %v, want 3x2", b)
	}
}
