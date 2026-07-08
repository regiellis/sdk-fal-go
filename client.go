package fal

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// defaultTimeout is the per-request HTTP timeout used when none is configured.
const defaultTimeout = 120 * time.Second

// Client is a fal.ai API client. A zero Client is not usable; construct one with
// NewClient. A Client is safe for concurrent use by multiple goroutines.
type Client struct {
	creds      CredentialsProvider
	httpClient *http.Client
	userAgent  string

	runURL   string
	queueURL string
	restURL  string
	cdnURL   string

	// storageTokens caches the v3 CDN storage token for this client.
	storageTokens *v3TokenManager
}

// NewClient constructs a Client. It never returns an error; credential problems
// surface on the first call that needs them. Options are applied in order.
func NewClient(opts ...ClientOption) *Client {
	cfg := &clientConfig{
		timeout:   defaultTimeout,
		userAgent: userAgent,
		runURL:    defaultRunURL(),
		queueURL:  defaultQueueURL(),
		restURL:   "https://rest.fal.ai",
		cdnURL:    "https://v3.fal.media",
	}
	for _, opt := range opts {
		opt(cfg)
	}

	if cfg.httpClient == nil {
		cfg.httpClient = &http.Client{Timeout: cfg.timeout}
	}
	if cfg.userAgent == "" {
		cfg.userAgent = userAgent
	}

	creds := cfg.creds
	if creds == nil {
		if cfg.key != "" {
			creds = staticCredentialsProvider{creds: Credentials{Scheme: "Key", Token: cfg.key}}
		} else {
			creds = newDefaultCredentialsProvider(cfg.httpClient, cfg.auth0URL)
		}
	}

	client := &Client{
		creds:      creds,
		httpClient: cfg.httpClient,
		userAgent:  cfg.userAgent,
		runURL:     strings.TrimRight(cfg.runURL, "/"),
		queueURL:   strings.TrimRight(cfg.queueURL, "/"),
		restURL:    strings.TrimRight(cfg.restURL, "/"),
		cdnURL:     strings.TrimRight(cfg.cdnURL, "/"),
	}
	client.storageTokens = &v3TokenManager{client: client}
	return client
}

// defaultRunURL computes the run base URL, honoring FAL_RUN_HOST.
func defaultRunURL() string {
	return "https://" + runHost()
}

// defaultQueueURL computes the queue base URL, honoring FAL_QUEUE_RUN_HOST and
// falling back to queue.<run host>.
func defaultQueueURL() string {
	if host := os.Getenv("FAL_QUEUE_RUN_HOST"); host != "" {
		return "https://" + host
	}
	return "https://queue." + runHost()
}

// runHost returns the run host, honoring FAL_RUN_HOST.
func runHost() string {
	if host := os.Getenv("FAL_RUN_HOST"); host != "" {
		return host
	}
	return "fal.run"
}

// newRequest builds an authenticated request with the User-Agent header set. It
// resolves credentials, so credential problems surface here. The body, when
// non-nil, is made replayable so the request can be safely retried.
func (c *Client) newRequest(ctx context.Context, method, rawURL string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, fmt.Errorf("fal: building request: %w", err)
	}

	creds, err := c.creds.Credentials(ctx)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", creds.header())
	req.Header.Set("User-Agent", c.userAgent)
	return req, nil
}
