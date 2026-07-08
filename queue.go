package fal

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// defaultPollInterval is the wait between status polls when none is configured.
const defaultPollInterval = 100 * time.Millisecond

// bestEffortCancelTimeout bounds the auto-cancel issued when a Subscribe call's
// context ends before the request completes.
const bestEffortCancelTimeout = 5 * time.Second

// Priority selects the queue priority for a submitted request.
type Priority string

// Recognized queue priorities.
const (
	// PriorityNormal is the default queue priority.
	PriorityNormal Priority = "normal"
	// PriorityLow queues the request behind normal-priority work.
	PriorityLow Priority = "low"
)

// Request is a handle to a queued request. Construct one with [Client.Submit]
// or reattach to an existing request with [Client.Request].
type Request struct {
	// ID is the server-assigned request identifier.
	ID string

	client      *Client
	statusURL   string
	responseURL string
	cancelURL   string
}

// Submit enqueues a request and returns a handle for polling and retrieval.
// A non-success HTTP response is reported as [*APIError].
func (c *Client) Submit(ctx context.Context, app string, input any, opts ...SubmitOption) (*Request, error) {
	return c.submit(ctx, app, input, submitOptions(opts))
}

// submit performs an enqueue with already-collected options. It is shared by
// [Client.Submit] and [Client.Subscribe].
func (c *Client) submit(ctx context.Context, app string, input any, o callOptions) (*Request, error) {
	id, err := parseAppID(app)
	if err != nil {
		return nil, err
	}

	u := appPath(c.queueURL, id, o.path)
	if o.webhook != "" {
		u += "?" + url.Values{"fal_webhook": {o.webhook}}.Encode()
	}

	body, err := marshalInput(input)
	if err != nil {
		return nil, err
	}

	req, err := c.newRequest(ctx, http.MethodPost, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := applyCallHeaders(req, &o); err != nil {
		return nil, err
	}

	respBody, err := c.doAndRead(ctx, req)
	if err != nil {
		return nil, err
	}

	var sr struct {
		RequestID   string `json:"request_id"`
		ResponseURL string `json:"response_url"`
		StatusURL   string `json:"status_url"`
		CancelURL   string `json:"cancel_url"`
	}
	if err := json.Unmarshal(respBody, &sr); err != nil {
		return nil, fmt.Errorf("fal: decoding submit response: %w", err)
	}
	if sr.RequestID == "" {
		return nil, fmt.Errorf("fal: submit response missing request_id")
	}

	return &Request{
		ID:          sr.RequestID,
		client:      c,
		statusURL:   sr.StatusURL,
		responseURL: sr.ResponseURL,
		cancelURL:   sr.CancelURL,
	}, nil
}

// Request reattaches to a previously submitted request by id, reconstructing
// its status, result, and cancel URLs from the queue base URL.
func (c *Client) Request(app, requestID string) (*Request, error) {
	if requestID == "" {
		return nil, fmt.Errorf("fal: request id must not be empty")
	}
	id, err := parseAppID(app)
	if err != nil {
		return nil, err
	}
	base := requestsBase(c.queueURL, id) + "/requests/" + requestID
	return &Request{
		ID:          requestID,
		client:      c,
		statusURL:   base + "/status",
		responseURL: base,
		cancelURL:   base + "/cancel",
	}, nil
}

// requestsBase builds "{queueURL}/[namespace/]owner/alias", the base under
// which reattach request URLs are formed. Any per-app subpath is ignored.
func requestsBase(queueURL string, id appID) string {
	var b strings.Builder
	b.WriteString(queueURL)
	b.WriteByte('/')
	if id.Namespace != "" {
		b.WriteString(id.Namespace)
		b.WriteByte('/')
	}
	b.WriteString(id.Owner)
	b.WriteByte('/')
	b.WriteString(id.Alias)
	return b.String()
}

// Status fetches the current [Status] of the request. Pass [WithLogs] to
// include log entries.
func (r *Request) Status(ctx context.Context, opts ...StatusOption) (Status, error) {
	o := statusOptions(opts)
	return r.status(ctx, o.logs)
}

// status fetches the current status, requesting logs when logs is true.
func (r *Request) status(ctx context.Context, logs bool) (Status, error) {
	u, err := withLogsQuery(r.statusURL, logs)
	if err != nil {
		return nil, err
	}
	req, err := r.client.newRequest(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	body, err := r.client.doAndRead(ctx, req)
	if err != nil {
		return nil, err
	}
	return parseStatus(body)
}

// withLogsQuery returns rawURL with the logs query parameter set to "true" or
// "false", preserving any existing query parameters.
func withLogsQuery(rawURL string, logs bool) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("fal: parsing status url: %w", err)
	}
	q := u.Query()
	q.Set("logs", strconv.FormatBool(logs))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Events returns an iterator that polls the request and yields each observed
// [Status]. It yields a non-nil error and stops on a failed poll or when the
// context ends; on success it stops after yielding a [Completed] status. The
// poll interval defaults to 100ms; set it with [WithPollInterval].
func (r *Request) Events(ctx context.Context, opts ...EventsOption) iter.Seq2[Status, error] {
	return r.events(ctx, eventsOptions(opts))
}

// events implements Events with already-collected options, shared with
// [Client.Subscribe].
func (r *Request) events(ctx context.Context, o callOptions) iter.Seq2[Status, error] {
	interval := o.pollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	logs := o.logs

	return func(yield func(Status, error) bool) {
		for {
			st, err := r.status(ctx, logs)
			if err != nil {
				yield(nil, err)
				return
			}
			if !yield(st, nil) {
				return
			}
			if _, done := st.(Completed); done {
				return
			}
			if err := sleepContext(ctx, interval); err != nil {
				yield(nil, err)
				return
			}
		}
	}
}

// Result polls the request to completion and returns the raw JSON result body.
// A server-side failure or a non-success response fetching the result is
// returned as an error.
func (r *Request) Result(ctx context.Context) (json.RawMessage, error) {
	for {
		st, err := r.status(ctx, false)
		if err != nil {
			return nil, err
		}
		if c, ok := st.(Completed); ok {
			if c.Error != "" {
				return nil, completionError(r.ID, c)
			}
			break
		}
		if err := sleepContext(ctx, defaultPollInterval); err != nil {
			return nil, err
		}
	}

	req, err := r.client.newRequest(ctx, http.MethodGet, r.responseURL, nil)
	if err != nil {
		return nil, err
	}
	body, err := r.client.doAndRead(ctx, req)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}

// completionError formats the error carried by a failed [Completed] status.
func completionError(requestID string, c Completed) error {
	if c.ErrorType != "" {
		return fmt.Errorf("fal: request %s failed: %s (%s)", requestID, c.Error, c.ErrorType)
	}
	return fmt.Errorf("fal: request %s failed: %s", requestID, c.Error)
}

// Cancel requests cancellation of the queued request.
func (r *Request) Cancel(ctx context.Context) error {
	req, err := r.client.newRequest(ctx, http.MethodPut, r.cancelURL, nil)
	if err != nil {
		return err
	}
	_, err = r.client.doAndRead(ctx, req)
	return err
}

// bestEffortCancel attempts to cancel the request on a fresh, bounded context
// derived from parent so that a cancelled parent does not abort the cleanup.
// The outcome is intentionally discarded: the request may already be terminal.
func (r *Request) bestEffortCancel(parent context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), bestEffortCancelTimeout)
	defer cancel()
	_ = r.Cancel(ctx)
}

// Subscribe submits a request, polls it to completion, and returns the raw
// JSON result. It fires [OnEnqueue] once after submission and [OnUpdate] for
// each observed status. The caller's context bounds the total time; if it ends
// after submission, the queued request is cancelled on a best-effort basis and
// the call returns a [*TimeoutError] wrapping the context error.
func (c *Client) Subscribe(ctx context.Context, app string, input any, opts ...SubscribeOption) (json.RawMessage, error) {
	o := subscribeOptions(opts)

	req, err := c.submit(ctx, app, input, o)
	if err != nil {
		return nil, err
	}
	if o.onEnqueue != nil {
		o.onEnqueue(req.ID)
	}

	for st, err := range req.events(ctx, o) {
		if err != nil {
			if ctx.Err() != nil {
				req.bestEffortCancel(ctx)
				return nil, &TimeoutError{RequestID: req.ID, Err: ctx.Err()}
			}
			return nil, err
		}
		if o.onUpdate != nil {
			o.onUpdate(st)
		}
	}

	return req.Result(ctx)
}

// TimeoutError reports that a request did not complete before its context
// ended. It carries the request id so the caller can reattach and exposes the
// underlying context error through Unwrap.
type TimeoutError struct {
	// RequestID identifies the affected request.
	RequestID string
	// Err is the underlying cause, typically a context error.
	Err error
}

// Error implements the error interface.
func (e *TimeoutError) Error() string {
	return fmt.Sprintf("fal: request %s did not complete: %v", e.RequestID, e.Err)
}

// Unwrap returns the underlying error.
func (e *TimeoutError) Unwrap() error { return e.Err }
