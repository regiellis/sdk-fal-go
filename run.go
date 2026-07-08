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
	"time"
)

// Run executes a model synchronously and returns the raw JSON response body.
//
// The input is JSON-encoded as the request body; the returned
// [json.RawMessage] is the response body, left undecoded so the caller can
// unmarshal it into its own type. A non-success HTTP response is reported as
// [*APIError].
func (c *Client) Run(ctx context.Context, app string, input any, opts ...RunOption) (json.RawMessage, error) {
	o := runOptions(opts)

	id, err := parseAppID(app)
	if err != nil {
		return nil, err
	}

	body, err := marshalInput(input)
	if err != nil {
		return nil, err
	}

	req, err := c.newRequest(ctx, http.MethodPost, appPath(c.runURL, id, o.path), body)
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
	return json.RawMessage(respBody), nil
}

// appPath builds "{base}/[namespace/]owner/alias[/appPath][/extra]" for a call
// against base. extra is the optional per-call subpath from WithPath; leading
// and trailing slashes are trimmed before it is appended.
func appPath(base string, id appID, extra string) string {
	u := base + "/" + id.path()
	if extra != "" {
		u += "/" + strings.Trim(extra, "/")
	}
	return u
}

// marshalInput JSON-encodes a request body into a replayable reader.
func marshalInput(input any) (io.Reader, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("fal: encoding request body: %w", err)
	}
	return bytes.NewReader(data), nil
}

// applyCallHeaders sets the per-call gateway headers shared by Run and Submit.
// The start timeout, when present, must be greater than one second.
func applyCallHeaders(req *http.Request, o *callOptions) error {
	if o.hint != "" {
		req.Header.Set(headerRunnerHint, o.hint)
	}
	if o.priority != "" {
		req.Header.Set(headerQueuePriority, string(o.priority))
	}
	if o.hasStartTimeout {
		if o.startTimeout <= time.Second {
			return fmt.Errorf("fal: start timeout must be greater than 1s, got %s", o.startTimeout)
		}
		secs := o.startTimeout.Seconds()
		req.Header.Set(headerRequestTimeout, strconv.FormatFloat(secs, 'f', -1, 64))
	}
	return nil
}

// doAndRead sends a request through the shared retrying transport, reads the
// buffered response body, and converts a non-2xx status into an [*APIError].
func (c *Client) doAndRead(ctx context.Context, req *http.Request) ([]byte, error) {
	resp, err := c.do(ctx, req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("fal: reading response body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, newAPIError(resp, body)
	}
	return body, nil
}

// callOptions accumulates the settings produced by the functional options in
// this package. A single struct backs every call type; each option
// constructor is typed so it applies only to the calls where it is valid.
type callOptions struct {
	path            string
	hint            string
	startTimeout    time.Duration
	hasStartTimeout bool
	webhook         string
	priority        Priority
	logs            bool
	pollInterval    time.Duration
	onEnqueue       func(requestID string)
	onUpdate        func(Status)
}

// The option types below are distinct interfaces so that one exported
// constructor name (for example WithPath) can serve several call types without
// collision, while the compiler still rejects an option on a call where it does
// not apply. Each constructor returns a small unexported value that satisfies
// exactly the interfaces for the calls it is valid on.

// RunOption configures a [Client.Run] call.
type RunOption interface{ applyRun(*callOptions) }

// SubmitOption configures a [Client.Submit] call.
type SubmitOption interface{ applySubmit(*callOptions) }

// StatusOption configures a [Request.Status] call.
type StatusOption interface{ applyStatus(*callOptions) }

// EventsOption configures a [Request.Events] iterator.
type EventsOption interface{ applyEvents(*callOptions) }

// SubscribeOption configures a [Client.Subscribe] call. It accepts every
// [SubmitOption] plus the polling and callback options.
type SubscribeOption interface{ applySubscribe(*callOptions) }

// runSubmitOption is valid for Run, Submit, and Subscribe calls.
type runSubmitOption struct{ f func(*callOptions) }

func (o runSubmitOption) applyRun(c *callOptions)       { o.f(c) }
func (o runSubmitOption) applySubmit(c *callOptions)    { o.f(c) }
func (o runSubmitOption) applySubscribe(c *callOptions) { o.f(c) }

// submitOnlyOption is valid for Submit and Subscribe calls.
type submitOnlyOption struct{ f func(*callOptions) }

func (o submitOnlyOption) applySubmit(c *callOptions)    { o.f(c) }
func (o submitOnlyOption) applySubscribe(c *callOptions) { o.f(c) }

// logsOption is valid for Status, Events, and Subscribe calls.
type logsOption struct{ f func(*callOptions) }

func (o logsOption) applyStatus(c *callOptions)    { o.f(c) }
func (o logsOption) applyEvents(c *callOptions)    { o.f(c) }
func (o logsOption) applySubscribe(c *callOptions) { o.f(c) }

// pollOption is valid for Events and Subscribe calls.
type pollOption struct{ f func(*callOptions) }

func (o pollOption) applyEvents(c *callOptions)    { o.f(c) }
func (o pollOption) applySubscribe(c *callOptions) { o.f(c) }

// subscribeOnlyOption is valid only for Subscribe calls.
type subscribeOnlyOption struct{ f func(*callOptions) }

func (o subscribeOnlyOption) applySubscribe(c *callOptions) { o.f(c) }

// WithPath sets the per-call subpath appended after the app identifier.
func WithPath(path string) runSubmitOption {
	return runSubmitOption{func(c *callOptions) { c.path = path }}
}

// WithHint requests a specific runner via the X-Fal-Runner-Hint header.
func WithHint(hint string) runSubmitOption {
	return runSubmitOption{func(c *callOptions) { c.hint = hint }}
}

// WithStartTimeout sets the server-side start timeout via the
// X-Fal-Request-Timeout header. The duration must be greater than one second;
// a smaller value makes the call fail before it is sent.
func WithStartTimeout(d time.Duration) runSubmitOption {
	return runSubmitOption{func(c *callOptions) {
		c.startTimeout = d
		c.hasStartTimeout = true
	}}
}

// WithWebhook delivers the result to url via the fal_webhook query parameter.
func WithWebhook(url string) submitOnlyOption {
	return submitOnlyOption{func(c *callOptions) { c.webhook = url }}
}

// WithPriority sets the queue priority via the X-Fal-Queue-Priority header.
func WithPriority(p Priority) submitOnlyOption {
	return submitOnlyOption{func(c *callOptions) { c.priority = p }}
}

// WithLogs requests that status responses include log entries.
func WithLogs() logsOption {
	return logsOption{func(c *callOptions) { c.logs = true }}
}

// WithPollInterval sets the interval between status polls. The default is
// 100ms; a non-positive value keeps the default.
func WithPollInterval(d time.Duration) pollOption {
	return pollOption{func(c *callOptions) { c.pollInterval = d }}
}

// OnEnqueue registers a callback invoked once with the request id after a
// Subscribe call submits its request.
func OnEnqueue(fn func(requestID string)) subscribeOnlyOption {
	return subscribeOnlyOption{func(c *callOptions) { c.onEnqueue = fn }}
}

// OnUpdate registers a callback invoked with each status observed while a
// Subscribe call polls to completion.
func OnUpdate(fn func(Status)) subscribeOnlyOption {
	return subscribeOnlyOption{func(c *callOptions) { c.onUpdate = fn }}
}

// runOptions collects run options into a callOptions value.
func runOptions(opts []RunOption) callOptions {
	var c callOptions
	for _, o := range opts {
		o.applyRun(&c)
	}
	return c
}

// submitOptions collects submit options into a callOptions value.
func submitOptions(opts []SubmitOption) callOptions {
	var c callOptions
	for _, o := range opts {
		o.applySubmit(&c)
	}
	return c
}

// statusOptions collects status options into a callOptions value.
func statusOptions(opts []StatusOption) callOptions {
	var c callOptions
	for _, o := range opts {
		o.applyStatus(&c)
	}
	return c
}

// eventsOptions collects events options into a callOptions value.
func eventsOptions(opts []EventsOption) callOptions {
	var c callOptions
	for _, o := range opts {
		o.applyEvents(&c)
	}
	return c
}

// subscribeOptions collects subscribe options into a callOptions value.
func subscribeOptions(opts []SubscribeOption) callOptions {
	var c callOptions
	for _, o := range opts {
		o.applySubscribe(&c)
	}
	return c
}
