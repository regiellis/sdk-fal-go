package fal

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// clearFalEnv blanks the credential environment and points FAL_HOME_DIR at a
// fresh temp directory so tests are isolated from the host and from each other.
func clearFalEnv(t *testing.T) string {
	t.Helper()
	t.Setenv("FAL_KEY", "")
	t.Setenv("FAL_KEY_ID", "")
	t.Setenv("FAL_KEY_SECRET", "")
	t.Setenv("FAL_FORCE_AUTH_BY_USER", "")
	dir := t.TempDir()
	t.Setenv("FAL_HOME_DIR", dir)
	return dir
}

// makeJWT builds an unsigned JWT whose payload carries the given exp claim.
func makeJWT(exp int64) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil, `{"exp":%d}`, exp))
	return header + "." + payload + ".sig"
}

func TestResolveEnvCredentials(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		wantOK    bool
		wantCreds Credentials
	}{
		{
			name:      "fal key",
			env:       map[string]string{"FAL_KEY": "abc123"},
			wantOK:    true,
			wantCreds: Credentials{Scheme: "Key", Token: "abc123"},
		},
		{
			name:      "key id and secret",
			env:       map[string]string{"FAL_KEY_ID": "id", "FAL_KEY_SECRET": "secret"},
			wantOK:    true,
			wantCreds: Credentials{Scheme: "Key", Token: "id:secret"},
		},
		{
			name:   "key id without secret",
			env:    map[string]string{"FAL_KEY_ID": "id"},
			wantOK: false,
		},
		{
			name:   "force user auth skips fal key",
			env:    map[string]string{"FAL_KEY": "abc123", "FAL_FORCE_AUTH_BY_USER": "1"},
			wantOK: false,
		},
		{
			name:      "fal key wins over id and secret",
			env:       map[string]string{"FAL_KEY": "top", "FAL_KEY_ID": "id", "FAL_KEY_SECRET": "secret"},
			wantOK:    true,
			wantCreds: Credentials{Scheme: "Key", Token: "top"},
		},
		{
			name:   "nothing set",
			env:    map[string]string{},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearFalEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			creds, ok := resolveEnvCredentials()
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (creds %+v)", ok, tt.wantOK, creds)
			}
			if ok && creds != tt.wantCreds {
				t.Fatalf("creds = %+v, want %+v", creds, tt.wantCreds)
			}
		})
	}
}

func TestDefaultProviderEnvPathCachesAndPrecedesTokenFile(t *testing.T) {
	dir := clearFalEnv(t)
	t.Setenv("FAL_KEY", "envkey")

	// A token file exists but the env key must win and be cached.
	writeTokenFixture(t, dir, "refresh", makeJWT(time.Now().Add(time.Hour).Unix()))

	p := newDefaultCredentialsProvider(http.DefaultClient, "")
	got, err := p.Credentials(context.Background())
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if want := (Credentials{Scheme: "Key", Token: "envkey"}); got != want {
		t.Fatalf("creds = %+v, want %+v", got, want)
	}
	if p.cachedEnv == nil {
		t.Fatal("env credentials were not cached")
	}
}

func TestDefaultProviderMissingCredentials(t *testing.T) {
	clearFalEnv(t) // no env keys, empty home dir with no token file
	p := newDefaultCredentialsProvider(http.DefaultClient, "")
	_, err := p.Credentials(context.Background())
	if !errors.Is(err, ErrMissingCredentials) {
		t.Fatalf("err = %v, want ErrMissingCredentials", err)
	}
}

func TestDefaultProviderTokenFileValidJWT(t *testing.T) {
	dir := clearFalEnv(t)
	access := makeJWT(time.Now().Add(time.Hour).Unix())
	writeTokenFixture(t, dir, "refresh-token", access)

	// A refresh server that must NOT be called because the JWT is still valid.
	var called atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := newDefaultCredentialsProvider(srv.Client(), srv.URL)
	got, err := p.Credentials(context.Background())
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if want := (Credentials{Scheme: "Bearer", Token: access}); got != want {
		t.Fatalf("creds = %+v, want Bearer valid token", got)
	}
	if called.Load() != 0 {
		t.Fatalf("refresh server called %d times, want 0", called.Load())
	}
}

func TestDefaultProviderRefreshesExpiredJWT(t *testing.T) {
	dir := clearFalEnv(t)
	expired := makeJWT(time.Now().Add(-time.Hour).Unix())
	writeTokenFixture(t, dir, "old-refresh", expired)

	newAccess := makeJWT(time.Now().Add(time.Hour).Unix())
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"access_token":  newAccess,
			"refresh_token": "new-refresh",
		})
	}))
	defer srv.Close()

	p := newDefaultCredentialsProvider(srv.Client(), srv.URL)
	got, err := p.Credentials(context.Background())
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if want := (Credentials{Scheme: "Bearer", Token: newAccess}); got != want {
		t.Fatalf("creds = %+v, want refreshed bearer token", got)
	}

	if gotForm.Get("grant_type") != "refresh_token" {
		t.Errorf("grant_type = %q, want refresh_token", gotForm.Get("grant_type"))
	}
	if gotForm.Get("client_id") != auth0ClientID {
		t.Errorf("client_id = %q, want %q", gotForm.Get("client_id"), auth0ClientID)
	}
	if gotForm.Get("refresh_token") != "old-refresh" {
		t.Errorf("refresh_token = %q, want old-refresh", gotForm.Get("refresh_token"))
	}

	// The token file must be rewritten with the new refresh and access tokens.
	refresh, access := readTokenFixture(t, dir)
	if refresh != "new-refresh" {
		t.Errorf("rewritten refresh = %q, want new-refresh", refresh)
	}
	if access != newAccess {
		t.Errorf("rewritten access = %q, want new access", access)
	}
}

func TestDefaultProviderRefreshKeepsOldRefreshWhenServerOmitsIt(t *testing.T) {
	dir := clearFalEnv(t)
	writeTokenFixture(t, dir, "keep-me", makeJWT(time.Now().Add(-time.Hour).Unix()))

	newAccess := makeJWT(time.Now().Add(time.Hour).Unix())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": newAccess})
	}))
	defer srv.Close()

	p := newDefaultCredentialsProvider(srv.Client(), srv.URL)
	if _, err := p.Credentials(context.Background()); err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	refresh, _ := readTokenFixture(t, dir)
	if refresh != "keep-me" {
		t.Errorf("refresh = %q, want retained keep-me", refresh)
	}
}

func TestDefaultProviderRefreshFailure(t *testing.T) {
	dir := clearFalEnv(t)
	writeTokenFixture(t, dir, "refresh", makeJWT(time.Now().Add(-time.Hour).Unix()))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	p := newDefaultCredentialsProvider(srv.Client(), srv.URL)
	_, err := p.Credentials(context.Background())
	if err == nil {
		t.Fatal("expected error on refresh failure")
	}
}

func TestDefaultProviderMissingAccessLineTriggersRefresh(t *testing.T) {
	dir := clearFalEnv(t)
	// Only a refresh token, no cached access line.
	writeTokenFixture(t, dir, "refresh-only", "")

	newAccess := makeJWT(time.Now().Add(time.Hour).Unix())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": newAccess})
	}))
	defer srv.Close()

	p := newDefaultCredentialsProvider(srv.Client(), srv.URL)
	got, err := p.Credentials(context.Background())
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if got.Token != newAccess {
		t.Fatalf("token = %q, want refreshed access", got.Token)
	}
}

func TestAccessTokenExpired(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  bool
	}{
		{"valid far future", makeJWT(time.Now().Add(time.Hour).Unix()), false},
		{"expired", makeJWT(time.Now().Add(-time.Hour).Unix()), true},
		{"within leeway", makeJWT(time.Now().Add(100 * time.Second).Unix()), true},
		{"just past leeway", makeJWT(time.Now().Add(400 * time.Second).Unix()), false},
		{"fractional exp valid", "h." + base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil, `{"exp":%d.5}`, time.Now().Add(time.Hour).Unix())) + ".s", false},
		{"unparseable", "not-a-jwt", true},
		{"missing exp", "h." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"x"}`)) + ".s", true},
		{"garbage payload", "h.!!!not-base64!!!.s", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := accessTokenExpired(tt.token); got != tt.want {
				t.Fatalf("accessTokenExpired = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFalHomeDirHonorsEnv(t *testing.T) {
	t.Setenv("FAL_HOME_DIR", "/custom/fal/home")
	dir, err := falHomeDir()
	if err != nil {
		t.Fatalf("falHomeDir: %v", err)
	}
	if dir != "/custom/fal/home" {
		t.Fatalf("dir = %q, want /custom/fal/home", dir)
	}
}

func writeTokenFixture(t *testing.T, dir, refresh, access string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	contents := refresh
	if access != "" {
		contents += "\n" + access
	}
	if err := os.WriteFile(filepath.Join(dir, authTokenFilename), []byte(contents), 0o600); err != nil {
		t.Fatalf("write token fixture: %v", err)
	}
}

func readTokenFixture(t *testing.T, dir string) (refresh, access string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, authTokenFilename))
	if err != nil {
		t.Fatalf("read token fixture: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	refresh = lines[0]
	if len(lines) > 1 {
		access = lines[1]
	}
	return refresh, access
}
