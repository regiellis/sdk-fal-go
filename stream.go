package fal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
)

// defaultStreamPath is the app subpath appended for streaming requests.
const defaultStreamPath = "/stream"

// StreamOption configures a Stream call.
type StreamOption func(*streamConfig)

// streamConfig holds the resolved settings for a Stream call.
type streamConfig struct {
	path string
}

// WithStreamPath overrides the app subpath used for the streaming request. The
// default is "/stream". The value is appended to the app identifier, so it
// should begin with a slash.
func WithStreamPath(path string) StreamOption {
	return func(c *streamConfig) {
		c.path = path
	}
}

// Stream runs an app and returns its Server-Sent Events response as an iterator
// over JSON events. It issues POST {runURL}/{app}{path} (path defaulting to
// "/stream") with input marshaled as the JSON request body and an
// "Accept: text/event-stream" header.
//
// Each server event's data is yielded as a json.RawMessage with a nil error.
// The iterator stops after yielding a single non-nil error on a failed request,
// a non-2xx response (an *APIError), a transport failure, or context
// cancellation; it ends silently when the stream reaches a clean EOF. Streamed
// responses are never retried automatically.
//
// The request lifetime is governed entirely by ctx: streaming runs on a copy of
// the client's HTTP client with its Timeout cleared so a long-lived stream is
// not cut short by the default per-request timeout. Cancelling ctx aborts the
// stream promptly. If the consumer stops early, the response body is closed.
func (c *Client) Stream(ctx context.Context, app string, input any, opts ...StreamOption) iter.Seq2[json.RawMessage, error] {
	return func(yield func(json.RawMessage, error) bool) {
		cfg := streamConfig{path: defaultStreamPath}
		for _, opt := range opts {
			opt(&cfg)
		}

		id, err := parseAppID(app)
		if err != nil {
			yield(nil, err)
			return
		}

		body, err := json.Marshal(input)
		if err != nil {
			yield(nil, fmt.Errorf("fal: marshaling stream input: %w", err))
			return
		}

		url := c.runURL + "/" + id.path() + cfg.path
		req, err := c.newRequest(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			yield(nil, err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")

		resp, err := c.streamHTTPClient().Do(req)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				yield(nil, cerr)
				return
			}
			yield(nil, fmt.Errorf("fal: stream request failed: %w", err))
			return
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			errBody, rerr := io.ReadAll(resp.Body)
			if rerr != nil {
				yield(nil, fmt.Errorf("fal: reading error response: %w", rerr))
				return
			}
			yield(nil, newAPIError(resp, errBody))
			return
		}

		dec := newSSEDecoder(resp.Body)
		for {
			data, err := dec.next()
			if err != nil {
				if cerr := ctx.Err(); cerr != nil {
					yield(nil, cerr)
					return
				}
				if err == io.EOF {
					return
				}
				yield(nil, fmt.Errorf("fal: reading stream: %w", err))
				return
			}
			if !yield(data, nil) {
				return
			}
		}
	}
}

// streamHTTPClient returns an HTTP client for streaming derived from the
// client's configured one: the same Transport, redirect policy, and cookie jar,
// but with Timeout cleared. A client-level timeout would abort a long-lived
// stream, so the stream's deadline is left to the caller's context instead.
func (c *Client) streamHTTPClient() *http.Client {
	return &http.Client{
		Transport:     c.httpClient.Transport,
		CheckRedirect: c.httpClient.CheckRedirect,
		Jar:           c.httpClient.Jar,
	}
}
