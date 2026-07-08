package fal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// partResult holds the outcome of uploading one multipart part.
type partResult struct {
	partNumber int
	etag       string
}

// uploadV3Multipart performs a multipart v3 CDN upload. It creates the upload,
// streams parts of partSize bytes (up to concurrency in flight), collects each
// part's ETag, and completes the upload. partSize and concurrency are
// parameters so callers and tests can tune them.
func (c *Client) uploadV3Multipart(ctx context.Context, tok v3Token, src uploadSource, contentType string, cfg *uploadConfig, partSize int64, concurrency int) (string, error) {
	if partSize <= 0 {
		return "", fmt.Errorf("fal: multipart part size must be positive")
	}
	if concurrency < 1 {
		concurrency = 1
	}

	accessURL, uploadID, err := c.multipartCreate(ctx, tok, contentType, cfg)
	if err != nil {
		return "", err
	}

	parts, err := c.multipartUploadParts(ctx, tok, src, accessURL, uploadID, partSize, concurrency)
	if err != nil {
		return "", err
	}

	finalURL, err := c.multipartComplete(ctx, tok, accessURL, uploadID, parts)
	if err != nil {
		return "", err
	}
	if finalURL == "" {
		finalURL = accessURL
	}
	return finalURL, nil
}

// multipartCreate initiates a multipart upload and returns the access URL and
// upload id.
func (c *Client) multipartCreate(ctx context.Context, tok v3Token, contentType string, cfg *uploadConfig) (accessURL, uploadID string, err error) {
	endpoint := strings.TrimRight(tok.baseURL, "/") + "/files/upload/multipart"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return "", "", fmt.Errorf("fal: building multipart create request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", tok.authHeader())
	req.Header.Set("User-Agent", c.userAgent)
	if cfg.fileName != "" {
		req.Header.Set(headerFileName, cfg.fileName)
	}
	if lerr := applyLifecycleHeaders(req, cfg.lifecycle); lerr != nil {
		return "", "", lerr
	}

	resp, err := c.do(ctx, req)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("fal: reading multipart create response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", newAPIError(resp, body)
	}

	var out struct {
		AccessURL string `json:"access_url"`
		UploadID  string `json:"uploadId"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", "", fmt.Errorf("fal: decoding multipart create response: %w", err)
	}
	if out.AccessURL == "" || out.UploadID == "" {
		return "", "", fmt.Errorf("fal: multipart create response missing access_url or uploadId")
	}
	return out.AccessURL, out.UploadID, nil
}

// multipartUploadParts uploads all parts concurrently and returns their results
// ordered by part number. Bounded concurrency is provided by a semaphore
// channel and a wait group; the first error cancels the rest.
func (c *Client) multipartUploadParts(ctx context.Context, tok v3Token, src uploadSource, accessURL, uploadID string, partSize int64, concurrency int) ([]partResult, error) {
	size := src.size()
	numParts := int((size + partSize - 1) / partSize)
	if numParts == 0 {
		numParts = 1 // upload a single empty part for a zero-length object
	}

	results := make([]partResult, numParts)
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	var (
		errMu    sync.Mutex
		firstErr error
	)
	recordErr := func(err error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		errMu.Unlock()
	}

	partCtx, cancel := context.WithCancel(ctx)
	defer cancel()

schedule:
	for i := 0; i < numParts; i++ {
		select {
		case <-partCtx.Done():
			// A previous part failed or ctx was canceled; stop scheduling.
			break schedule
		case sem <- struct{}{}:
		}

		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()

			off := int64(idx) * partSize
			n := partSize
			if off+n > size {
				n = size - off
			}
			if n < 0 {
				n = 0
			}

			buf := make([]byte, n)
			if n > 0 {
				// A trailing io.EOF is acceptable when the full buffer is read;
				// any other short read is an error.
				read, rerr := src.readAt(buf, off)
				if read != len(buf) {
					if rerr == nil {
						rerr = io.ErrUnexpectedEOF
					}
					recordErr(fmt.Errorf("fal: reading part %d: %w", idx+1, rerr))
					cancel()
					return
				}
			}

			etag, perr := c.putPart(partCtx, tok, accessURL, uploadID, idx+1, buf)
			if perr != nil {
				recordErr(perr)
				cancel()
				return
			}
			results[idx] = partResult{partNumber: idx + 1, etag: etag}
		}(i)
	}

	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}
	return results, nil
}

// putPart uploads a single 1-indexed part and returns its ETag.
func (c *Client) putPart(ctx context.Context, tok v3Token, accessURL, uploadID string, partNumber int, chunk []byte) (string, error) {
	partURL := strings.TrimRight(accessURL, "/") + "/multipart/" + uploadID + "/" + strconv.Itoa(partNumber)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, partURL, bytes.NewReader(chunk))
	if err != nil {
		return "", fmt.Errorf("fal: building part %d request: %w", partNumber, err)
	}
	req.ContentLength = int64(len(chunk))
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Authorization", tok.authHeader())
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.do(ctx, req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("fal: reading part %d response: %w", partNumber, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", newAPIError(resp, body)
	}

	etag := resp.Header.Get("ETag")
	if etag == "" {
		return "", fmt.Errorf("fal: part %d response missing ETag", partNumber)
	}
	return etag, nil
}

// completePart is one entry in the multipart completion body.
type completePart struct {
	PartNumber int    `json:"partNumber"`
	ETag       string `json:"etag"`
}

// multipartComplete finalizes the upload with the collected part ETags. It
// returns the access URL from the completion response when present.
func (c *Client) multipartComplete(ctx context.Context, tok v3Token, accessURL, uploadID string, parts []partResult) (string, error) {
	payload := struct {
		Parts []completePart `json:"parts"`
	}{Parts: make([]completePart, len(parts))}
	for i, p := range parts {
		payload.Parts[i] = completePart{PartNumber: p.partNumber, ETag: p.etag}
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("fal: encoding multipart completion: %w", err)
	}

	endpoint := strings.TrimRight(accessURL, "/") + "/multipart/" + uploadID + "/complete"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("fal: building multipart complete request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", tok.authHeader())
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.do(ctx, req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("fal: reading multipart complete response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", newAPIError(resp, body)
	}

	var out struct {
		AccessURL string `json:"access_url"`
	}
	_ = json.Unmarshal(body, &out) // completion body is optional; access_url falls back to the create URL
	return out.AccessURL, nil
}
