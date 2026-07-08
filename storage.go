package fal

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Repository identifies a fal storage backend that an upload targets.
type Repository string

const (
	// RepositoryV3 is the default v3 CDN storage backend.
	RepositoryV3 Repository = "fal_v3"
	// RepositoryLegacy is the legacy fal storage backend, used as a fallback.
	RepositoryLegacy Repository = "fal"
)

// validate reports whether r is a recognized repository.
func (r Repository) validate() error {
	switch r {
	case RepositoryV3, RepositoryLegacy:
		return nil
	default:
		return fmt.Errorf("fal: unknown repository %q", string(r))
	}
}

// Storage upload sizing. These are package constants so tests can reference the
// thresholds; the internal chunking functions take the part size as a parameter
// so tests can exercise them with small values.
const (
	// multipartThreshold is the size above which a v3 upload switches from a
	// single request to the multipart protocol.
	multipartThreshold int64 = 100 * 1024 * 1024
	// multipartChunkSize is the size of each multipart part.
	multipartChunkSize int64 = 10 * 1024 * 1024
	// multipartConcurrency is the maximum number of parts uploaded at once.
	multipartConcurrency = 10
)

// ACLDecision is an access-control decision applied to an uploaded object.
type ACLDecision string

const (
	// ACLHide hides the object from a principal.
	ACLHide ACLDecision = "hide"
	// ACLForbid forbids access to the object.
	ACLForbid ACLDecision = "forbid"
	// ACLAllow allows access to the object.
	ACLAllow ACLDecision = "allow"
)

// ACLRule grants or denies a specific user access to an uploaded object.
type ACLRule struct {
	// User is the identifier of the principal the rule applies to.
	User string
	// Decision is the access decision for the user.
	Decision ACLDecision
}

// StorageACL is the initial access-control list for an uploaded object.
type StorageACL struct {
	// Default is the decision applied to principals without a matching rule.
	Default ACLDecision
	// Rules are per-user overrides of the default decision.
	Rules []ACLRule
}

// StorageSettings configures lifecycle preferences for an upload.
type StorageSettings struct {
	// ExpiresIn sets how long the object is retained. A zero value keeps the
	// backend default. When set it is rounded to whole seconds and must be
	// positive.
	ExpiresIn time.Duration
	// InitialACL sets the object's initial access-control list. A nil value
	// keeps the backend default.
	InitialACL *StorageACL
}

// ImageFormat selects the encoding used by UploadImage.
type ImageFormat int

const (
	// ImageFormatJPEG encodes the image as JPEG. It is the default.
	ImageFormatJPEG ImageFormat = iota
	// ImageFormatPNG encodes the image as PNG.
	ImageFormatPNG
)

// UploadOption configures a single upload call.
type UploadOption func(*uploadConfig)

// uploadConfig holds the resolved settings for an upload.
type uploadConfig struct {
	fileName    string
	repository  Repository
	fallback    []Repository
	fallbackSet bool
	lifecycle   *StorageSettings
	imageFormat ImageFormat
}

// WithFileName sets the file name reported to the storage backend.
func WithFileName(name string) UploadOption {
	return func(c *uploadConfig) {
		c.fileName = name
	}
}

// WithRepository selects the primary storage backend for the upload. The
// default is RepositoryV3.
func WithRepository(r Repository) UploadOption {
	return func(c *uploadConfig) {
		c.repository = r
	}
}

// WithFallback sets the ordered list of backends to try after the primary one
// fails. The default fallback chain is [RepositoryLegacy]. Passing no
// repositories disables fallback entirely.
func WithFallback(repos ...Repository) UploadOption {
	return func(c *uploadConfig) {
		c.fallback = repos
		c.fallbackSet = true
	}
}

// WithLifecycle sets lifecycle preferences (expiration and initial ACL) for the
// uploaded object.
func WithLifecycle(s StorageSettings) UploadOption {
	return func(c *uploadConfig) {
		s := s
		c.lifecycle = &s
	}
}

// WithImageFormat selects the encoding used by UploadImage. The default is
// ImageFormatJPEG. It has no effect on Upload or UploadFile.
func WithImageFormat(f ImageFormat) UploadOption {
	return func(c *uploadConfig) {
		c.imageFormat = f
	}
}

// newUploadConfig applies opts to a fresh configuration.
func newUploadConfig(opts []UploadOption) *uploadConfig {
	cfg := &uploadConfig{}
	for _, o := range opts {
		o(cfg)
	}
	return cfg
}

// repositories returns the deduplicated ordered backend chain, resolving
// defaults. It errors on any unknown repository value.
func (cfg *uploadConfig) repositories() ([]Repository, error) {
	primary := cfg.repository
	if primary == "" {
		primary = RepositoryV3
	}
	fallback := cfg.fallback
	if !cfg.fallbackSet {
		fallback = []Repository{RepositoryLegacy}
	}

	combined := make([]Repository, 0, 1+len(fallback))
	combined = append(combined, primary)
	combined = append(combined, fallback...)

	seen := make(map[Repository]struct{}, len(combined))
	out := make([]Repository, 0, len(combined))
	for _, r := range combined {
		if err := r.validate(); err != nil {
			return nil, err
		}
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	return out, nil
}

// Upload uploads data to fal storage and returns the resulting file URL. The
// content type is sent to the backend; an empty content type defaults to
// application/octet-stream. Options select the file name, repository, fallback
// chain, and lifecycle preferences.
func (c *Client) Upload(ctx context.Context, data []byte, contentType string, opts ...UploadOption) (string, error) {
	cfg := newUploadConfig(opts)
	if contentType == "" {
		contentType = defaultContentType
	}
	return c.upload(ctx, &bytesSource{data: data}, contentType, cfg)
}

// UploadFile uploads the file at path to fal storage and returns the resulting
// file URL. The content type is guessed from the file extension. Large files
// are streamed from disk in parts and are never loaded into memory in full. If
// no file name is set via WithFileName, the file's base name is used.
func (c *Client) UploadFile(ctx context.Context, path string, opts ...UploadOption) (string, error) {
	cfg := newUploadConfig(opts)

	src, err := openFileSource(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = src.Close() }()

	contentType := mime.TypeByExtension(filepath.Ext(path))
	if contentType == "" {
		contentType = defaultContentType
	}
	if cfg.fileName == "" {
		cfg.fileName = filepath.Base(path)
	}
	return c.upload(ctx, src, contentType, cfg)
}

// UploadImage encodes img and uploads it to fal storage, returning the file
// URL. The encoding is chosen with WithImageFormat and defaults to JPEG.
func (c *Client) UploadImage(ctx context.Context, img image.Image, opts ...UploadOption) (string, error) {
	cfg := newUploadConfig(opts)

	var buf bytes.Buffer
	var contentType string
	switch cfg.imageFormat {
	case ImageFormatPNG:
		if err := png.Encode(&buf, img); err != nil {
			return "", fmt.Errorf("fal: encoding PNG: %w", err)
		}
		contentType = "image/png"
	case ImageFormatJPEG:
		if err := jpeg.Encode(&buf, img, nil); err != nil {
			return "", fmt.Errorf("fal: encoding JPEG: %w", err)
		}
		contentType = "image/jpeg"
	default:
		return "", fmt.Errorf("fal: unknown image format %d", int(cfg.imageFormat))
	}

	return c.upload(ctx, &bytesSource{data: buf.Bytes()}, contentType, cfg)
}

// upload runs the repository fallback chain, returning the first successful file
// URL. On failure it logs at debug and continues; if every backend fails it
// returns the last error.
func (c *Client) upload(ctx context.Context, src uploadSource, contentType string, cfg *uploadConfig) (string, error) {
	chain, err := cfg.repositories()
	if err != nil {
		return "", err
	}

	// Multipart applies only when the primary repository is V3.
	allowMultipart := chain[0] == RepositoryV3

	var lastErr error
	for _, repo := range chain {
		var (
			url    string
			upErr  error
			handle = repo
		)
		switch handle {
		case RepositoryV3:
			url, upErr = c.uploadV3(ctx, src, contentType, cfg, allowMultipart)
		case RepositoryLegacy:
			url, upErr = c.uploadLegacy(ctx, src, contentType, cfg)
		default:
			upErr = fmt.Errorf("fal: unknown repository %q", string(handle))
		}
		if upErr == nil {
			return url, nil
		}
		slog.DebugContext(ctx, "fal: storage upload attempt failed", "repository", string(handle), "error", upErr)
		lastErr = upErr
	}
	return "", lastErr
}

// v3Token is a cached credential for the v3 CDN storage backend.
type v3Token struct {
	token     string
	tokenType string
	baseURL   string
	expiresAt time.Time
}

// authHeader returns the Authorization header value for CDN requests.
func (t v3Token) authHeader() string {
	return t.tokenType + " " + t.token
}

// v3TokenManager caches and refreshes the v3 CDN token for one client.
type v3TokenManager struct {
	client *Client
	mu     sync.Mutex
	cached *v3Token
	now    func() time.Time // seam for tests; nil means time.Now
}

// clock returns the current time, honoring the test seam.
func (m *v3TokenManager) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// v3Tokens returns the client's v3 token manager.
func (c *Client) v3Tokens() *v3TokenManager {
	return c.storageTokens
}

// token returns a valid v3 token, fetching a new one when the cache is empty or
// expired. The fetch is serialized so concurrent callers share a single token.
func (m *v3TokenManager) token(ctx context.Context) (v3Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cached != nil && m.clock().Before(m.cached.expiresAt) {
		return *m.cached, nil
	}

	tok, err := m.fetch(ctx)
	if err != nil {
		return v3Token{}, err
	}
	m.cached = &tok
	return tok, nil
}

// fetch requests a fresh v3 token from the REST API.
func (m *v3TokenManager) fetch(ctx context.Context) (v3Token, error) {
	c := m.client
	endpoint := c.restURL + "/storage/auth/token?storage_type=fal-cdn-v3"

	req, err := c.newRequest(ctx, http.MethodPost, endpoint, strings.NewReader("{}"))
	if err != nil {
		return v3Token{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.do(ctx, req)
	if err != nil {
		return v3Token{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return v3Token{}, fmt.Errorf("fal: reading storage token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return v3Token{}, newAPIError(resp, body)
	}

	var payload struct {
		Token     string          `json:"token"`
		TokenType string          `json:"token_type"`
		BaseURL   string          `json:"base_url"`
		ExpiresAt json.RawMessage `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return v3Token{}, fmt.Errorf("fal: decoding storage token response: %w", err)
	}
	if payload.Token == "" || payload.BaseURL == "" {
		return v3Token{}, fmt.Errorf("fal: storage token response missing token or base_url")
	}

	expiry, err := parseExpiresAt(payload.ExpiresAt)
	if err != nil {
		return v3Token{}, err
	}

	return v3Token{
		token:     payload.Token,
		tokenType: payload.TokenType,
		baseURL:   payload.BaseURL,
		expiresAt: expiry,
	}, nil
}

// parseExpiresAt reads an expires_at value that may be an RFC 3339 timestamp, a
// numeric unix timestamp, or absent. An absent value yields the zero time,
// which is treated as already expired.
func parseExpiresAt(raw json.RawMessage) (time.Time, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return time.Time{}, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if t, perr := time.Parse(time.RFC3339, s); perr == nil {
			return t, nil
		}
		if n, perr := strconv.ParseInt(s, 10, 64); perr == nil {
			return time.Unix(n, 0), nil
		}
		return time.Time{}, fmt.Errorf("fal: unparseable storage token expires_at %q", s)
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		return time.Unix(int64(n), 0), nil
	}
	return time.Time{}, fmt.Errorf("fal: unparseable storage token expires_at")
}

// uploadV3 uploads via the v3 CDN, choosing single-shot or multipart based on
// size. allowMultipart gates the multipart path.
func (c *Client) uploadV3(ctx context.Context, src uploadSource, contentType string, cfg *uploadConfig, allowMultipart bool) (string, error) {
	tok, err := c.v3Tokens().token(ctx)
	if err != nil {
		return "", err
	}
	if allowMultipart && src.size() > multipartThreshold {
		return c.uploadV3Multipart(ctx, tok, src, contentType, cfg, multipartChunkSize, multipartConcurrency)
	}
	return c.uploadV3SingleShot(ctx, tok, src, contentType, cfg)
}

// uploadV3SingleShot performs a single-request v3 CDN upload.
func (c *Client) uploadV3SingleShot(ctx context.Context, tok v3Token, src uploadSource, contentType string, cfg *uploadConfig) (string, error) {
	reader, length, err := src.newReader()
	if err != nil {
		return "", err
	}

	endpoint := strings.TrimRight(tok.baseURL, "/") + "/files/upload"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, reader)
	if err != nil {
		return "", fmt.Errorf("fal: building upload request: %w", err)
	}
	req.ContentLength = length
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", tok.authHeader())
	req.Header.Set("User-Agent", c.userAgent)
	if cfg.fileName != "" {
		req.Header.Set(headerFileName, cfg.fileName)
	}
	if err := applyLifecycleHeaders(req, cfg.lifecycle); err != nil {
		return "", err
	}

	resp, err := c.do(ctx, req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("fal: reading upload response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", newAPIError(resp, body)
	}

	var out struct {
		AccessURL string `json:"access_url"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("fal: decoding upload response: %w", err)
	}
	if out.AccessURL == "" {
		return "", fmt.Errorf("fal: upload response missing access_url")
	}
	return out.AccessURL, nil
}

// uploadLegacy uploads via the legacy fal repository: initiate a signed URL,
// then PUT the body to it.
func (c *Client) uploadLegacy(ctx context.Context, src uploadSource, contentType string, cfg *uploadConfig) (string, error) {
	fileName := cfg.fileName
	if fileName == "" {
		fileName = randomFileName(contentType)
	}

	initiateBody, err := json.Marshal(struct {
		FileName    string `json:"file_name"`
		ContentType string `json:"content_type"`
	}{FileName: fileName, ContentType: contentType})
	if err != nil {
		return "", fmt.Errorf("fal: encoding upload initiate request: %w", err)
	}

	endpoint := c.restURL + "/storage/upload/initiate?storage_type=gcs"
	req, err := c.newRequest(ctx, http.MethodPost, endpoint, bytes.NewReader(initiateBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := applyLifecycleHeaders(req, cfg.lifecycle); err != nil {
		return "", err
	}

	resp, err := c.do(ctx, req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("fal: reading upload initiate response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", newAPIError(resp, body)
	}

	var initiated struct {
		UploadURL string `json:"upload_url"`
		FileURL   string `json:"file_url"`
	}
	if err := json.Unmarshal(body, &initiated); err != nil {
		return "", fmt.Errorf("fal: decoding upload initiate response: %w", err)
	}
	if initiated.UploadURL == "" || initiated.FileURL == "" {
		return "", fmt.Errorf("fal: upload initiate response missing upload_url or file_url")
	}

	if err := c.putBody(ctx, initiated.UploadURL, contentType, src); err != nil {
		return "", err
	}
	return initiated.FileURL, nil
}

// putBody PUTs the full source content to a presigned URL. Presigned PUTs carry
// no Authorization header.
func (c *Client) putBody(ctx context.Context, url, contentType string, src uploadSource) error {
	reader, length, err := src.newReader()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, reader)
	if err != nil {
		return fmt.Errorf("fal: building upload PUT request: %w", err)
	}
	req.ContentLength = length
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.do(ctx, req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("fal: reading upload PUT response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return newAPIError(resp, body)
	}
	return nil
}

// randomFileName produces a random object name with an extension inferred from
// the content type.
func randomFileName(contentType string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("upload-%d", time.Now().UnixNano())
	}
	name := hex.EncodeToString(b[:])
	if exts, err := mime.ExtensionsByType(contentType); err == nil && len(exts) > 0 {
		name += exts[0]
	}
	return name
}

// lifecyclePayload is the JSON body carried by the lifecycle headers.
type lifecyclePayload struct {
	ExpirationDurationSeconds *int64      `json:"expiration_duration_seconds,omitempty"`
	InitialACL                *aclPayload `json:"initial_acl,omitempty"`
}

// aclPayload is the wire form of StorageACL.
type aclPayload struct {
	Default ACLDecision      `json:"default"`
	Rules   []aclRulePayload `json:"rules"`
}

// aclRulePayload is the wire form of ACLRule.
type aclRulePayload struct {
	User     string      `json:"user"`
	Decision ACLDecision `json:"decision"`
}

// applyLifecycleHeaders sets the object-lifecycle headers on req when settings
// carry meaningful preferences. It errors when a set expiration is not positive.
func applyLifecycleHeaders(req *http.Request, s *StorageSettings) error {
	if s == nil {
		return nil
	}

	var payload lifecyclePayload
	hasData := false

	if s.ExpiresIn != 0 {
		secs := int64(s.ExpiresIn.Round(time.Second) / time.Second)
		if secs <= 0 {
			return fmt.Errorf("fal: lifecycle ExpiresIn must be positive when set")
		}
		payload.ExpirationDurationSeconds = &secs
		hasData = true
	}

	if s.InitialACL != nil {
		acl := &aclPayload{
			Default: s.InitialACL.Default,
			Rules:   make([]aclRulePayload, 0, len(s.InitialACL.Rules)),
		}
		for _, r := range s.InitialACL.Rules {
			acl.Rules = append(acl.Rules, aclRulePayload(r))
		}
		payload.InitialACL = acl
		hasData = true
	}

	if !hasData {
		return nil
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("fal: encoding lifecycle preferences: %w", err)
	}
	req.Header.Set(headerObjectLifecycle, string(data))
	req.Header.Set(headerObjectLifecyclePref, string(data))
	return nil
}

// uploadSource abstracts the bytes to upload, supporting both whole-content
// reads (single-shot and presigned PUT) and ranged reads (multipart streaming).
type uploadSource interface {
	// size returns the total content length in bytes.
	size() int64
	// newReader returns a reader over the full content and its length.
	newReader() (io.Reader, int64, error)
	// readAt reads into p at the given offset, matching io.ReaderAt.
	readAt(p []byte, off int64) (int, error)
	// Close releases any underlying resource.
	Close() error
}

// bytesSource is an in-memory upload source.
type bytesSource struct {
	data []byte
}

func (s *bytesSource) size() int64 { return int64(len(s.data)) }

func (s *bytesSource) newReader() (io.Reader, int64, error) {
	return bytes.NewReader(s.data), int64(len(s.data)), nil
}

func (s *bytesSource) readAt(p []byte, off int64) (int, error) {
	return bytes.NewReader(s.data).ReadAt(p, off)
}

func (s *bytesSource) Close() error { return nil }

// fileSource streams an upload from a file on disk, reading ranges via ReadAt so
// the whole file is never held in memory.
type fileSource struct {
	f  *os.File
	sz int64
}

// openFileSource opens path and stats its size.
func openFileSource(path string) (*fileSource, error) {
	f, err := os.Open(path) // #nosec G304 -- path is supplied by the caller by design
	if err != nil {
		return nil, fmt.Errorf("fal: opening file for upload: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("fal: stat file for upload: %w", err)
	}
	return &fileSource{f: f, sz: info.Size()}, nil
}

func (s *fileSource) size() int64 { return s.sz }

func (s *fileSource) newReader() (io.Reader, int64, error) {
	if _, err := s.f.Seek(0, io.SeekStart); err != nil {
		return nil, 0, fmt.Errorf("fal: seeking upload file: %w", err)
	}
	return s.f, s.sz, nil
}

func (s *fileSource) readAt(p []byte, off int64) (int, error) {
	return s.f.ReadAt(p, off)
}

func (s *fileSource) Close() error { return s.f.Close() }
