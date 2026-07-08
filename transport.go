package fal

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"time"
)

// Retry policy constants.
const (
	maxAttempts   = 10
	retryBaseSecs = 0.1
	retryMaxSecs  = 30.0
)

// retryStatusCodes are always retried (subject to the never-retry rules).
var retryStatusCodes = map[int]struct{}{
	http.StatusRequestTimeout:  {}, // 408
	http.StatusConflict:        {}, // 409
	http.StatusTooManyRequests: {}, // 429
}

// ingressStatusCodes may be retried when the response looks like it came from
// the ingress proxy rather than the application.
var ingressStatusCodes = map[int]struct{}{
	http.StatusBadGateway:         {}, // 502
	http.StatusServiceUnavailable: {}, // 503
	http.StatusGatewayTimeout:     {}, // 504
}

// do performs an HTTP request with the shared retry policy. It honors ctx,
// including during backoff between attempts, and returns the final response
// (retryable or not) once retries are exhausted.
//
// The returned response body is buffered in memory and replayable; do must not
// be used for streaming responses.
func (c *Client) do(ctx context.Context, req *http.Request) (*http.Response, error) {
	req = req.WithContext(ctx)

	if err := ensureReplayableBody(req); err != nil {
		return nil, err
	}

	honorTimeout := req.Header.Get(headerRequestTimeout) != ""

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		if attempt > 1 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, fmt.Errorf("fal: replaying request body: %w", err)
			}
			req.Body = body
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = fmt.Errorf("fal: request failed: %w", err)
			if honorTimeout || attempt == maxAttempts {
				return nil, lastErr
			}
		} else {
			body, rerr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if rerr != nil {
				lastErr = fmt.Errorf("fal: reading response body: %w", rerr)
				if honorTimeout || attempt == maxAttempts {
					return nil, lastErr
				}
			} else {
				resp.Body = io.NopCloser(bytes.NewReader(body))
				if !shouldRetryResponse(resp, body) || attempt == maxAttempts {
					return resp, nil
				}
			}
		}

		if err := sleepContext(ctx, retryDelay(attempt)); err != nil {
			return nil, err
		}
	}

	// Unreachable: the loop returns on the final attempt.
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("fal: request failed after %d attempts", maxAttempts)
}

// ensureReplayableBody guarantees the request body can be re-read across retry
// attempts by installing a GetBody when one is missing.
func ensureReplayableBody(req *http.Request) error {
	if req.Body == nil || req.GetBody != nil {
		return nil
	}
	buf, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return fmt.Errorf("fal: buffering request body: %w", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(buf))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(buf)), nil
	}
	req.ContentLength = int64(len(buf))
	return nil
}

// shouldRetryResponse applies the response-based retry rules. body is the
// already-read response body used by the ingress heuristic.
func shouldRetryResponse(resp *http.Response, body []byte) bool {
	// Never retry a 504 that carries a user-set timeout type.
	if resp.StatusCode == http.StatusGatewayTimeout && resp.Header.Get(headerRequestTimeoutType) != "" {
		return false
	}
	if isIngressError(resp, body) {
		return true
	}
	_, ok := retryStatusCodes[resp.StatusCode]
	return ok
}

// isIngressError reports whether a 502/503/504 response looks like an ingress
// proxy error rather than an application response. Responses carrying an
// x-fal-request-id header are treated as coming from the application.
func isIngressError(resp *http.Response, body []byte) bool {
	if _, ok := ingressStatusCodes[resp.StatusCode]; !ok {
		return false
	}
	if resp.Header.Get(headerFalRequestID) != "" {
		return false
	}
	return bytes.Contains(body, []byte("nginx"))
}

// retryDelay computes the backoff for a 1-based attempt number: exponential from
// a 0.1s base, capped at 30s, with multiplicative jitter in [0.5, 1.5).
func retryDelay(attempt int) time.Duration {
	delay := retryBaseSecs * math.Pow(2, float64(attempt-1))
	if delay > retryMaxSecs {
		delay = retryMaxSecs
	}
	delay *= 0.5 + rand.Float64() // #nosec G404 -- jitter, not security-sensitive
	if delay > retryMaxSecs {
		delay = retryMaxSecs
	}
	return time.Duration(delay * float64(time.Second))
}

// sleepContext waits for d or until ctx is done, returning ctx.Err() if ctx ends
// first.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
