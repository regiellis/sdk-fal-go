package fal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/vmihailenco/msgpack/v5"
)

func TestParseTokenResponse(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantToken string
		wantErr   bool
	}{
		{name: "bare string", body: `"abc.def.ghi"`, wantToken: "abc.def.ghi"},
		{name: "token object", body: `{"token":"tok-123"}`, wantToken: "tok-123"},
		{name: "detail object", body: `{"detail":"app not allowed"}`, wantErr: true},
		{name: "empty bare string", body: `""`, wantErr: true},
		{name: "empty object", body: `{}`, wantErr: true},
		{name: "garbage", body: `not json at all`, wantErr: true},
		{name: "json number", body: `12345`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTokenResponse([]byte(tt.body))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseTokenResponse(%q) = %q, want error", tt.body, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseTokenResponse(%q) unexpected error: %v", tt.body, err)
			}
			if got != tt.wantToken {
				t.Errorf("parseTokenResponse(%q) = %q, want %q", tt.body, got, tt.wantToken)
			}
		})
	}
}

func TestMintRealtimeTokenRequest(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotAuth   string
		gotBody   struct {
			AllowedApps     []string `json:"allowed_apps"`
			TokenExpiration int      `json:"token_expiration"`
		}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `"minted-token"`)
	}))
	defer srv.Close()

	c := NewClient(WithKey("secret-key"), WithRestURL(srv.URL))
	id, err := parseAppID("owner/my-app")
	if err != nil {
		t.Fatalf("parseAppID: %v", err)
	}

	token, err := c.mintRealtimeToken(context.Background(), id, 90*time.Second)
	if err != nil {
		t.Fatalf("mintRealtimeToken: %v", err)
	}
	if token != "minted-token" {
		t.Errorf("token = %q, want minted-token", token)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/tokens/" {
		t.Errorf("path = %q, want /tokens/", gotPath)
	}
	if gotAuth != "Key secret-key" {
		t.Errorf("auth = %q, want Key secret-key", gotAuth)
	}
	if len(gotBody.AllowedApps) != 1 || gotBody.AllowedApps[0] != "my-app" {
		t.Errorf("allowed_apps = %v, want [my-app]", gotBody.AllowedApps)
	}
	if gotBody.TokenExpiration != 90 {
		t.Errorf("token_expiration = %d, want 90", gotBody.TokenExpiration)
	}
}

func TestMintRealtimeTokenAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"detail":"forbidden"}`)
	}))
	defer srv.Close()

	c := NewClient(WithKey("k"), WithRestURL(srv.URL))
	id, _ := parseAppID("owner/app")
	_, err := c.mintRealtimeToken(context.Background(), id, time.Minute)
	if err == nil {
		t.Fatal("expected error for non-2xx response")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", apiErr.StatusCode)
	}
}

func TestRealtimeURL(t *testing.T) {
	c := NewClient(WithKey("k"), WithRunURL("https://fal.run"))

	tests := []struct {
		name       string
		app        string
		cfg        realtimeConfig
		token      string
		wantScheme string
		wantPath   string
		wantToken  string // expected fal_jwt_token; "" means must be absent
		wantBuffer string // expected max_buffering; "" means must be absent
	}{
		{
			name:       "plain app with token",
			app:        "owner/app",
			cfg:        realtimeConfig{path: "/realtime"},
			token:      "tok",
			wantScheme: "wss",
			wantPath:   "/owner/app/realtime",
			wantToken:  "tok",
		},
		{
			name:       "namespaced workflows app",
			app:        "workflows/owner/flow",
			cfg:        realtimeConfig{path: "/realtime"},
			token:      "tok",
			wantScheme: "wss",
			wantPath:   "/workflows/owner/flow/realtime",
			wantToken:  "tok",
		},
		{
			name:       "custom path",
			app:        "owner/app",
			cfg:        realtimeConfig{path: "/ws"},
			token:      "tok",
			wantScheme: "wss",
			wantPath:   "/owner/app/ws",
			wantToken:  "tok",
		},
		{
			name:       "max buffering set",
			app:        "owner/app",
			cfg:        realtimeConfig{path: "/realtime", maxBuffering: 30},
			token:      "tok",
			wantScheme: "wss",
			wantPath:   "/owner/app/realtime",
			wantToken:  "tok",
			wantBuffer: "30",
		},
		{
			name:       "token omitted (without jwt)",
			app:        "owner/app",
			cfg:        realtimeConfig{path: "/realtime", maxBuffering: 5},
			token:      "",
			wantScheme: "wss",
			wantPath:   "/owner/app/realtime",
			wantToken:  "",
			wantBuffer: "5",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := parseAppID(tt.app)
			if err != nil {
				t.Fatalf("parseAppID: %v", err)
			}
			raw, err := c.realtimeURL(id, &tt.cfg, tt.token)
			if err != nil {
				t.Fatalf("realtimeURL: %v", err)
			}
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatalf("url.Parse(%q): %v", raw, err)
			}
			if u.Scheme != tt.wantScheme {
				t.Errorf("scheme = %q, want %q", u.Scheme, tt.wantScheme)
			}
			if u.Path != tt.wantPath {
				t.Errorf("path = %q, want %q", u.Path, tt.wantPath)
			}
			q := u.Query()
			if got := q.Get("fal_jwt_token"); got != tt.wantToken {
				t.Errorf("fal_jwt_token = %q, want %q", got, tt.wantToken)
			}
			if tt.wantToken == "" && q.Has("fal_jwt_token") {
				t.Errorf("fal_jwt_token present, want absent")
			}
			if got := q.Get("max_buffering"); got != tt.wantBuffer {
				t.Errorf("max_buffering = %q, want %q", got, tt.wantBuffer)
			}
			if tt.wantBuffer == "" && q.Has("max_buffering") {
				t.Errorf("max_buffering present, want absent")
			}
		})
	}
}

func TestRealtimeURLSchemeConversion(t *testing.T) {
	c := NewClient(WithKey("k"), WithRunURL("http://127.0.0.1:8080"))
	id, _ := parseAppID("owner/app")
	raw, err := c.realtimeURL(id, &realtimeConfig{path: "/realtime"}, "")
	if err != nil {
		t.Fatalf("realtimeURL: %v", err)
	}
	u, _ := url.Parse(raw)
	if u.Scheme != "ws" {
		t.Errorf("scheme = %q, want ws", u.Scheme)
	}
}

func TestWithMaxBufferingBounds(t *testing.T) {
	tests := []struct {
		n       int
		wantErr bool
	}{
		{n: 0, wantErr: true},
		{n: 1, wantErr: false},
		{n: 30, wantErr: false},
		{n: 60, wantErr: false},
		{n: 61, wantErr: true},
		{n: -1, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.n), func(t *testing.T) {
			var cfg realtimeConfig
			err := WithMaxBuffering(tt.n)(&cfg)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("WithMaxBuffering(%d) = nil, want error", tt.n)
				}
				return
			}
			if err != nil {
				t.Fatalf("WithMaxBuffering(%d) unexpected error: %v", tt.n, err)
			}
			if cfg.maxBuffering != tt.n {
				t.Errorf("maxBuffering = %d, want %d", cfg.maxBuffering, tt.n)
			}
		})
	}
}

// realtimeTestServer wires an httptest server that mints a token at /tokens/ and
// upgrades every other path to a WebSocket handled by wsHandler. It returns a
// client pointed at the server.
func realtimeTestServer(t *testing.T, tokenBody string, wsHandler func(ctx context.Context, conn *websocket.Conn)) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/tokens/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, tokenBody)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("websocket accept: %v", err)
			return
		}
		conn.SetReadLimit(-1)
		wsHandler(r.Context(), conn)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return NewClient(WithKey("test-key"), WithRunURL(srv.URL), WithRestURL(srv.URL), WithHTTPClient(srv.Client()))
}

func TestRealtimeSendServerDecodesMsgpack(t *testing.T) {
	got := make(chan []byte, 1)

	c := realtimeTestServer(t, `"tok"`, func(ctx context.Context, conn *websocket.Conn) {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			t.Errorf("server read: %v", err)
			return
		}
		if typ != websocket.MessageBinary {
			t.Errorf("frame type = %v, want binary", typ)
		}
		// The frame must be valid msgpack; decode it back to JSON.
		asJSON, derr := decodeMsgpackToJSON(data)
		if derr != nil {
			t.Errorf("server msgpack decode: %v", derr)
		}
		got <- asJSON
		_ = conn.Close(websocket.StatusNormalClosure, "")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rc, err := c.Realtime(ctx, "owner/app")
	if err != nil {
		t.Fatalf("Realtime: %v", err)
	}
	defer func() { _ = rc.Close() }()

	if err := rc.Send(ctx, map[string]any{"prompt": "hello", "steps": 4}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case asJSON := <-got:
		var decoded struct {
			Prompt string `json:"prompt"`
			Steps  int    `json:"steps"`
		}
		if err := json.Unmarshal(asJSON, &decoded); err != nil {
			t.Fatalf("unmarshal server-decoded JSON %q: %v", string(asJSON), err)
		}
		if decoded.Prompt != "hello" {
			t.Errorf("prompt = %q, want hello", decoded.Prompt)
		}
		if decoded.Steps != 4 {
			t.Errorf("steps = %d, want 4", decoded.Steps)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for server to receive message")
	}
}

func TestRealtimeRecvMsgpackRoundTrip(t *testing.T) {
	c := realtimeTestServer(t, `"tok"`, func(ctx context.Context, conn *websocket.Conn) {
		out := map[string]any{
			"images": []any{map[string]any{"url": "https://cdn/x.png"}},
			"seed":   int64(7),
		}
		data, err := msgpack.Marshal(out)
		if err != nil {
			t.Errorf("server marshal: %v", err)
			return
		}
		if err := conn.Write(ctx, websocket.MessageBinary, data); err != nil {
			t.Errorf("server write: %v", err)
		}
		_ = conn.Close(websocket.StatusNormalClosure, "")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rc, err := c.Realtime(ctx, "owner/app")
	if err != nil {
		t.Fatalf("Realtime: %v", err)
	}
	defer func() { _ = rc.Close() }()

	msg, err := rc.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	var decoded struct {
		Images []struct {
			URL string `json:"url"`
		} `json:"images"`
		Seed int `json:"seed"`
	}
	if err := json.Unmarshal(msg, &decoded); err != nil {
		t.Fatalf("unmarshal recv JSON %q: %v", string(msg), err)
	}
	if decoded.Seed != 7 {
		t.Errorf("seed = %d, want 7", decoded.Seed)
	}
	if len(decoded.Images) != 1 || decoded.Images[0].URL != "https://cdn/x.png" {
		t.Errorf("images = %+v, want one url", decoded.Images)
	}
}

func TestRealtimeRecvErrorFrame(t *testing.T) {
	c := realtimeTestServer(t, `"tok"`, func(ctx context.Context, conn *websocket.Conn) {
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"x-fal-error","error":"VALIDATION","reason":"bad input"}`))
		_ = conn.Close(websocket.StatusNormalClosure, "")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rc, err := c.Realtime(ctx, "owner/app")
	if err != nil {
		t.Fatalf("Realtime: %v", err)
	}
	defer func() { _ = rc.Close() }()

	_, err = rc.Recv(ctx)
	var rtErr *RealtimeError
	if !errors.As(err, &rtErr) {
		t.Fatalf("Recv error = %v, want *RealtimeError", err)
	}
	if rtErr.Code != "VALIDATION" {
		t.Errorf("code = %q, want VALIDATION", rtErr.Code)
	}
	if rtErr.Reason != "bad input" {
		t.Errorf("reason = %q, want bad input", rtErr.Reason)
	}
	if rtErr.Error() != "VALIDATION: bad input" {
		t.Errorf("Error() = %q, want %q", rtErr.Error(), "VALIDATION: bad input")
	}
	if rtErr.Payload["type"] != "x-fal-error" {
		t.Errorf("payload missing type field: %v", rtErr.Payload)
	}
}

func TestRealtimeErrorDefaultCode(t *testing.T) {
	err := parseRealtimeError([]byte(`{"type":"x-fal-error"}`))
	var rtErr *RealtimeError
	if !errors.As(err, &rtErr) {
		t.Fatalf("error = %v, want *RealtimeError", err)
	}
	if rtErr.Code != "UNKNOWN_ERROR" {
		t.Errorf("code = %q, want UNKNOWN_ERROR", rtErr.Code)
	}
	if rtErr.Error() != "UNKNOWN_ERROR" {
		t.Errorf("Error() = %q, want UNKNOWN_ERROR", rtErr.Error())
	}
}

func TestRealtimeRecvSkipsControlFrame(t *testing.T) {
	c := realtimeTestServer(t, `"tok"`, func(ctx context.Context, conn *websocket.Conn) {
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"x-fal-message","message":"queued"}`))
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"status":"ready"}`))
		_ = conn.Close(websocket.StatusNormalClosure, "")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rc, err := c.Realtime(ctx, "owner/app")
	if err != nil {
		t.Fatalf("Realtime: %v", err)
	}
	defer func() { _ = rc.Close() }()

	msg, err := rc.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	var decoded struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(msg, &decoded); err != nil {
		t.Fatalf("unmarshal %q: %v", string(msg), err)
	}
	if decoded.Status != "ready" {
		t.Errorf("status = %q, want ready (control frame not skipped)", decoded.Status)
	}
}

func TestRealtimeRecvNonJSONText(t *testing.T) {
	c := realtimeTestServer(t, `"tok"`, func(ctx context.Context, conn *websocket.Conn) {
		_ = conn.Write(ctx, websocket.MessageText, []byte("plain text ping"))
		_ = conn.Close(websocket.StatusNormalClosure, "")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rc, err := c.Realtime(ctx, "owner/app")
	if err != nil {
		t.Fatalf("Realtime: %v", err)
	}
	defer func() { _ = rc.Close() }()

	msg, err := rc.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	var decoded struct {
		Type    string `json:"type"`
		Payload string `json:"payload"`
	}
	if err := json.Unmarshal(msg, &decoded); err != nil {
		t.Fatalf("unmarshal %q: %v", string(msg), err)
	}
	if decoded.Type != "text" || decoded.Payload != "plain text ping" {
		t.Errorf("wrapped = %+v, want {text, plain text ping}", decoded)
	}
}

func TestRealtimeRecvCleanClose(t *testing.T) {
	c := realtimeTestServer(t, `"tok"`, func(ctx context.Context, conn *websocket.Conn) {
		_ = conn.Close(websocket.StatusNormalClosure, "")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rc, err := c.Realtime(ctx, "owner/app")
	if err != nil {
		t.Fatalf("Realtime: %v", err)
	}
	defer func() { _ = rc.Close() }()

	_, err = rc.Recv(ctx)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("Recv error = %v, want io.EOF", err)
	}
}

func TestRealtimeRecvContextCancel(t *testing.T) {
	c := realtimeTestServer(t, `"tok"`, func(ctx context.Context, conn *websocket.Conn) {
		// Hold the connection open without sending anything.
		<-ctx.Done()
		_ = conn.Close(websocket.StatusNormalClosure, "")
	})

	rc, err := c.Realtime(context.Background(), "owner/app")
	if err != nil {
		t.Fatalf("Realtime: %v", err)
	}
	defer func() { _ = rc.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	done := make(chan error, 1)
	go func() {
		_, rerr := rc.Recv(ctx)
		done <- rerr
	}()

	select {
	case rerr := <-done:
		if !errors.Is(rerr, context.Canceled) {
			t.Errorf("Recv error = %v, want context.Canceled", rerr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Recv did not return promptly after context cancel")
	}
}

func TestRealtimeWithoutJWTSendsAuthHeader(t *testing.T) {
	gotAuth := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/tokens/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("token endpoint called with WithoutJWT")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		gotAuth <- r.Header.Get("Authorization")
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		_ = conn.Close(websocket.StatusNormalClosure, "")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(WithKey("secret-key"), WithRunURL(srv.URL), WithRestURL(srv.URL), WithHTTPClient(srv.Client()))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rc, err := c.Realtime(ctx, "owner/app", WithoutJWT())
	if err != nil {
		t.Fatalf("Realtime: %v", err)
	}
	defer func() { _ = rc.Close() }()

	select {
	case auth := <-gotAuth:
		if auth != "Key secret-key" {
			t.Errorf("handshake Authorization = %q, want Key secret-key", auth)
		}
	case <-ctx.Done():
		t.Fatal("handshake did not reach server")
	}
}

func TestRealtimeWithoutJWTOmitsToken(t *testing.T) {
	gotQuery := make(chan url.Values, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery <- r.URL.Query()
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}))
	defer srv.Close()

	c := NewClient(WithKey("k"), WithRunURL(srv.URL), WithRestURL(srv.URL), WithHTTPClient(srv.Client()))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rc, err := c.Realtime(ctx, "owner/app", WithoutJWT())
	if err != nil {
		t.Fatalf("Realtime: %v", err)
	}
	defer func() { _ = rc.Close() }()

	select {
	case q := <-gotQuery:
		if q.Has("fal_jwt_token") {
			t.Errorf("fal_jwt_token present with WithoutJWT: %v", q)
		}
	case <-ctx.Done():
		t.Fatal("handshake did not reach server")
	}
}

func TestRealtimeOptionsApply(t *testing.T) {
	cfg := realtimeConfig{path: defaultRealtimePath, tokenExpiration: defaultTokenExpiration}
	for _, opt := range []RealtimeOption{
		WithRealtimePath("/custom"),
		WithTokenExpiration(45 * time.Second),
	} {
		if err := opt(&cfg); err != nil {
			t.Fatalf("option returned error: %v", err)
		}
	}
	if cfg.path != "/custom" {
		t.Errorf("path = %q, want /custom", cfg.path)
	}
	if cfg.tokenExpiration != 45*time.Second {
		t.Errorf("tokenExpiration = %v, want 45s", cfg.tokenExpiration)
	}
}

func TestRealtimeInvalidAppID(t *testing.T) {
	c := NewClient(WithKey("k"))
	_, err := c.Realtime(context.Background(), "not-a-valid-id-no-slash")
	if err == nil {
		t.Fatal("expected error for invalid app id")
	}
}

func TestRealtimeCloseIdempotent(t *testing.T) {
	c := realtimeTestServer(t, `"tok"`, func(ctx context.Context, conn *websocket.Conn) {
		// Drain reads so the peer's close frame is processed and answered.
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rc, err := c.Realtime(ctx, "owner/app")
	if err != nil {
		t.Fatalf("Realtime: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
