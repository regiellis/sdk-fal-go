package fal

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Credentials is an Authorization header value split into its scheme and token.
// The header sent is "<Scheme> <Token>".
type Credentials struct {
	// Scheme is the authorization scheme, "Key" or "Bearer".
	Scheme string
	// Token is the credential token.
	Token string
}

// header returns the full Authorization header value.
func (c Credentials) header() string {
	return c.Scheme + " " + c.Token
}

// CredentialsProvider supplies credentials for outgoing requests. Providers may
// resolve lazily and must be safe for concurrent use.
type CredentialsProvider interface {
	// Credentials returns the credentials to use for a request, or an error
	// (such as ErrMissingCredentials) when none can be resolved.
	Credentials(ctx context.Context) (Credentials, error)
}

// staticCredentialsProvider always returns a fixed set of credentials.
type staticCredentialsProvider struct {
	creds Credentials
}

// Credentials implements CredentialsProvider.
func (p staticCredentialsProvider) Credentials(context.Context) (Credentials, error) {
	return p.creds, nil
}

const (
	auth0DefaultTokenURL = "https://auth.fal.ai/oauth/token"
	auth0ClientID        = "TwXR51Vz8JbY8GUUMy6EyuVR0fTO7N4N"
	authTokenFilename    = "auth0_token"
	jwtExpiryLeeway      = 300 * time.Second
	auth0RefreshTimeout  = 30 * time.Second
)

// defaultCredentialsProvider implements the standard resolution chain:
// environment keys, then the token file written by the fal CLI (with automatic
// Auth0 refresh). The environment result is cached after the first successful
// lookup; the token-file path re-checks JWT expiry on every call.
type defaultCredentialsProvider struct {
	httpClient *http.Client
	auth0URL   string
	mu         sync.Mutex // guards cachedEnv
	cachedEnv  *Credentials
	fileMu     sync.Mutex // serializes token-file access in-process
}

// newDefaultCredentialsProvider constructs the standard provider.
func newDefaultCredentialsProvider(hc *http.Client, auth0URL string) *defaultCredentialsProvider {
	if hc == nil {
		hc = http.DefaultClient
	}
	if auth0URL == "" {
		auth0URL = auth0DefaultTokenURL
	}
	return &defaultCredentialsProvider{httpClient: hc, auth0URL: auth0URL}
}

// Credentials implements CredentialsProvider.
func (p *defaultCredentialsProvider) Credentials(ctx context.Context) (Credentials, error) {
	p.mu.Lock()
	if p.cachedEnv != nil {
		c := *p.cachedEnv
		p.mu.Unlock()
		return c, nil
	}
	p.mu.Unlock()

	if creds, ok := resolveEnvCredentials(); ok {
		p.mu.Lock()
		p.cachedEnv = &creds
		p.mu.Unlock()
		return creds, nil
	}

	token, err := p.loadBearerToken(ctx)
	if err != nil {
		return Credentials{}, err
	}
	if token != "" {
		return Credentials{Scheme: "Bearer", Token: token}, nil
	}

	return Credentials{}, ErrMissingCredentials
}

// resolveEnvCredentials reads credentials from the environment. It reports
// whether credentials were found. FAL_FORCE_AUTH_BY_USER=1 skips the key steps.
func resolveEnvCredentials() (Credentials, bool) {
	if os.Getenv("FAL_FORCE_AUTH_BY_USER") == "1" {
		return Credentials{}, false
	}
	if key := os.Getenv("FAL_KEY"); key != "" {
		return Credentials{Scheme: "Key", Token: key}, true
	}
	keyID := os.Getenv("FAL_KEY_ID")
	keySecret := os.Getenv("FAL_KEY_SECRET")
	if keyID != "" && keySecret != "" {
		return Credentials{Scheme: "Key", Token: keyID + ":" + keySecret}, true
	}
	return Credentials{}, false
}

// falHomeDir returns the directory that holds fal CLI state, honoring
// FAL_HOME_DIR and defaulting to ~/.fal.
func falHomeDir() (string, error) {
	if dir := os.Getenv("FAL_HOME_DIR"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("fal: cannot resolve home directory: %w", err)
	}
	return filepath.Join(home, ".fal"), nil
}

// loadBearerToken returns a valid access token from the CLI token file,
// refreshing it against Auth0 when it is missing or close to expiry. It returns
// an empty string with a nil error when no token file or refresh token exists.
func (p *defaultCredentialsProvider) loadBearerToken(ctx context.Context) (string, error) {
	p.fileMu.Lock()
	defer p.fileMu.Unlock()

	refreshToken, accessToken, err := p.readTokenFile()
	if err != nil {
		return "", err
	}
	if refreshToken == "" {
		return "", nil
	}

	if accessToken != "" && !accessTokenExpired(accessToken) {
		return accessToken, nil
	}

	newRefresh, newAccess, err := p.refreshAccessToken(ctx, refreshToken)
	if err != nil {
		return "", err
	}
	if newRefresh == "" {
		newRefresh = refreshToken
	}
	if err := p.writeTokenFile(newRefresh, newAccess); err != nil {
		return "", err
	}
	return newAccess, nil
}

// tokenFilePath returns the absolute path of the CLI token file.
func tokenFilePath() (string, error) {
	dir, err := falHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, authTokenFilename), nil
}

// readTokenFile reads the refresh token (line 1) and optional cached access
// token (line 2) from the token file. A missing file yields two empty strings
// and a nil error.
func (p *defaultCredentialsProvider) readTokenFile() (refresh, access string, err error) {
	path, err := tokenFilePath()
	if err != nil {
		return "", "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", nil
		}
		return "", "", fmt.Errorf("fal: reading token file: %w", err)
	}

	lines := make([]string, 0, 2)
	for line := range strings.SplitSeq(string(data), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	if len(lines) == 0 {
		return "", "", nil
	}
	refresh = lines[0]
	if len(lines) > 1 {
		access = lines[1]
	}
	return refresh, access, nil
}

// writeTokenFile rewrites the token file with the refresh token on the first
// line and the access token on the second.
func (p *defaultCredentialsProvider) writeTokenFile(refresh, access string) error {
	dir, err := falHomeDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("fal: creating token directory: %w", err)
	}
	contents := refresh
	if access != "" {
		contents += "\n" + access
	}
	if err := os.WriteFile(filepath.Join(dir, authTokenFilename), []byte(contents), 0o600); err != nil {
		return fmt.Errorf("fal: writing token file: %w", err)
	}
	return nil
}

// refreshAccessToken exchanges a refresh token for a new access token at Auth0.
func (p *defaultCredentialsProvider) refreshAccessToken(ctx context.Context, refreshToken string) (newRefresh, newAccess string, err error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {auth0ClientID},
		"refresh_token": {refreshToken},
	}

	reqCtx, cancel := context.WithTimeout(ctx, auth0RefreshTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, p.auth0URL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", fmt.Errorf("fal: building token refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("fal: refreshing auth token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("fal: reading token refresh response: %w", err)
	}

	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	_ = json.Unmarshal(body, &payload) // absence of access_token is handled below

	if resp.StatusCode != http.StatusOK || payload.AccessToken == "" {
		return "", "", fmt.Errorf("fal: failed to refresh auth token (status %d); log in again with the fal CLI", resp.StatusCode)
	}
	return payload.RefreshToken, payload.AccessToken, nil
}

// accessTokenExpired reports whether a JWT access token is expired or within the
// expiry leeway. An unparseable token is treated as expired.
func accessTokenExpired(token string) bool {
	exp, ok := jwtExpiry(token)
	if !ok {
		return true
	}
	return time.Now().Add(jwtExpiryLeeway).After(exp)
}

// jwtExpiry parses the "exp" claim from a JWT payload without verifying the
// signature. It reports whether the claim was read successfully.
func jwtExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp json.Number `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, false
	}
	if claims.Exp == "" {
		return time.Time{}, false
	}
	secs, err := strconv.ParseInt(claims.Exp.String(), 10, 64)
	if err != nil {
		// Tolerate a fractional exp by truncating to whole seconds.
		f, ferr := claims.Exp.Float64()
		if ferr != nil {
			return time.Time{}, false
		}
		secs = int64(f)
	}
	return time.Unix(secs, 0), true
}
