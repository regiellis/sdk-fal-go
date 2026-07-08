package fal

import (
	"net/http"
	"time"
)

// ClientOption configures a Client during construction.
type ClientOption func(*clientConfig)

// clientConfig holds the resolved construction settings for a Client.
type clientConfig struct {
	key        string
	creds      CredentialsProvider
	httpClient *http.Client
	timeout    time.Duration
	userAgent  string

	runURL   string
	queueURL string
	restURL  string
	cdnURL   string

	// auth0URL is an internal seam for tests to point the token-refresh flow at
	// a stub server. It is intentionally not exposed as a public option.
	auth0URL string
}

// WithKey sets an explicit API key, sent with the "Key" scheme. It overrides
// the default credential resolution chain.
func WithKey(key string) ClientOption {
	return func(c *clientConfig) {
		c.key = key
	}
}

// WithCredentials sets a custom credentials provider. It takes precedence over
// WithKey and the default chain.
func WithCredentials(p CredentialsProvider) ClientOption {
	return func(c *clientConfig) {
		c.creds = p
	}
}

// WithHTTPClient sets the underlying HTTP client. When unset, a client using the
// configured timeout is created.
func WithHTTPClient(hc *http.Client) ClientOption {
	return func(c *clientConfig) {
		c.httpClient = hc
	}
}

// WithTimeout sets the default per-request HTTP timeout. The default is 120s. It
// is ignored when WithHTTPClient supplies a client.
func WithTimeout(d time.Duration) ClientOption {
	return func(c *clientConfig) {
		c.timeout = d
	}
}

// WithUserAgent overrides the User-Agent header sent with every request.
func WithUserAgent(ua string) ClientOption {
	return func(c *clientConfig) {
		c.userAgent = ua
	}
}

// WithRunURL overrides the base URL for synchronous inference. The default is
// https://fal.run, or https://<FAL_RUN_HOST> when that variable is set.
func WithRunURL(u string) ClientOption {
	return func(c *clientConfig) {
		c.runURL = u
	}
}

// WithQueueURL overrides the base URL for queued inference. The default is
// https://queue.fal.run, or https://<FAL_QUEUE_RUN_HOST> when that variable is
// set (which itself defaults to queue.<FAL_RUN_HOST>).
func WithQueueURL(u string) ClientOption {
	return func(c *clientConfig) {
		c.queueURL = u
	}
}

// WithRestURL overrides the base URL for the REST API. The default is
// https://rest.fal.ai.
func WithRestURL(u string) ClientOption {
	return func(c *clientConfig) {
		c.restURL = u
	}
}

// WithCDNURL overrides the base URL for the file CDN. The default is
// https://v3.fal.media.
func WithCDNURL(u string) ClientOption {
	return func(c *clientConfig) {
		c.cdnURL = u
	}
}
