package fal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/vmihailenco/msgpack/v5"
)

// Realtime defaults and limits.
const (
	// defaultRealtimePath is the WebSocket path appended to the app route when no
	// path is configured.
	defaultRealtimePath = "/realtime"
	// defaultTokenExpiration is the lifetime requested for a realtime token when
	// none is configured.
	defaultTokenExpiration = 120 * time.Second
	// realtimeHandshakeTimeout bounds the WebSocket handshake when the caller's
	// context carries no sooner deadline.
	realtimeHandshakeTimeout = 90 * time.Second
	// minMaxBuffering and maxMaxBuffering bound the accepted max_buffering value.
	minMaxBuffering = 1
	maxMaxBuffering = 60
)

// realtimeConfig holds the resolved settings for a realtime connection.
type realtimeConfig struct {
	path            string
	maxBuffering    int // 0 means unset
	tokenExpiration time.Duration
	withoutJWT      bool
}

// RealtimeOption configures a realtime connection. An option may report an
// error, which surfaces from Realtime before any network activity.
type RealtimeOption func(*realtimeConfig) error

// WithRealtimePath overrides the WebSocket path appended to the app route. The
// default is "/realtime".
func WithRealtimePath(path string) RealtimeOption {
	return func(c *realtimeConfig) error {
		c.path = path
		return nil
	}
}

// WithMaxBuffering sets the server-side max_buffering value, which must be
// between 1 and 60 inclusive. Values outside that range are rejected.
func WithMaxBuffering(n int) RealtimeOption {
	return func(c *realtimeConfig) error {
		if n < minMaxBuffering || n > maxMaxBuffering {
			return fmt.Errorf("fal: max buffering must be between %d and %d, got %d", minMaxBuffering, maxMaxBuffering, n)
		}
		c.maxBuffering = n
		return nil
	}
}

// WithTokenExpiration sets the requested lifetime of the short-lived realtime
// token, sent to the server as whole seconds. The default is 120 seconds.
func WithTokenExpiration(d time.Duration) RealtimeOption {
	return func(c *realtimeConfig) error {
		c.tokenExpiration = d
		return nil
	}
}

// WithoutJWT skips minting a realtime token and instead authenticates the
// WebSocket handshake with the account Authorization header.
func WithoutJWT() RealtimeOption {
	return func(c *realtimeConfig) error {
		c.withoutJWT = true
		return nil
	}
}

// RealtimeError is a structured error frame delivered over a realtime
// connection. It is returned by RealtimeConn.Recv and satisfies the error
// interface.
type RealtimeError struct {
	// Code is the error code reported by the server, or "UNKNOWN_ERROR" when
	// the frame carries none.
	Code string
	// Reason is an optional human-readable explanation.
	Reason string
	// Payload is the full decoded error frame.
	Payload map[string]any
}

// Error implements the error interface. It renders as "CODE: reason", or just
// the code when no reason is present.
func (e *RealtimeError) Error() string {
	if e.Reason == "" {
		return e.Code
	}
	return e.Code + ": " + e.Reason
}

// RealtimeConn is a live realtime WebSocket connection to a fal app. It is not
// safe for concurrent Send or concurrent Recv from multiple goroutines, though
// one goroutine may Send while another calls Recv. Close is always safe to call.
type RealtimeConn struct {
	conn      *websocket.Conn
	closeOnce sync.Once
	closeErr  error
}

// Realtime opens a realtime WebSocket connection to the named app. The returned
// connection must be closed by the caller. The context bounds the handshake;
// after the connection is established, per-message deadlines are controlled by
// the context passed to Send and Recv.
func (c *Client) Realtime(ctx context.Context, app string, opts ...RealtimeOption) (*RealtimeConn, error) {
	cfg := &realtimeConfig{
		path:            defaultRealtimePath,
		tokenExpiration: defaultTokenExpiration,
	}
	for _, opt := range opts {
		if err := opt(cfg); err != nil {
			return nil, err
		}
	}

	id, err := parseAppID(app)
	if err != nil {
		return nil, err
	}

	header := http.Header{}
	header.Set("User-Agent", c.userAgent)

	var token string
	if cfg.withoutJWT {
		creds, cerr := c.creds.Credentials(ctx)
		if cerr != nil {
			return nil, cerr
		}
		header.Set("Authorization", creds.header())
	} else {
		token, err = c.mintRealtimeToken(ctx, id, cfg.tokenExpiration)
		if err != nil {
			return nil, err
		}
	}

	wsURL, err := c.realtimeURL(id, cfg, token)
	if err != nil {
		return nil, err
	}

	dialCtx := ctx
	var cancel context.CancelFunc
	if dl, ok := ctx.Deadline(); !ok || time.Until(dl) > realtimeHandshakeTimeout {
		dialCtx, cancel = context.WithTimeout(ctx, realtimeHandshakeTimeout)
	}
	conn, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		HTTPClient: c.httpClient,
		HTTPHeader: header,
	})
	if cancel != nil {
		cancel()
	}
	if err != nil {
		return nil, fmt.Errorf("fal: realtime handshake: %w", err)
	}
	conn.SetReadLimit(-1)

	return &RealtimeConn{conn: conn}, nil
}

// mintRealtimeToken requests a short-lived realtime token scoped to the app.
func (c *Client) mintRealtimeToken(ctx context.Context, id appID, expiration time.Duration) (string, error) {
	reqBody := struct {
		AllowedApps     []string `json:"allowed_apps"`
		TokenExpiration int      `json:"token_expiration"`
	}{
		AllowedApps:     []string{id.Alias},
		TokenExpiration: int(expiration.Seconds()),
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("fal: encoding realtime token request: %w", err)
	}

	req, err := c.newRequest(ctx, http.MethodPost, c.restURL+"/tokens/", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.do(ctx, req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("fal: reading realtime token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", newAPIError(resp, body)
	}

	return parseTokenResponse(body)
}

// parseTokenResponse extracts a token from a mint response. The response is
// either a bare JSON string or an object carrying a "token" or "detail" field;
// anything else is an error.
func parseTokenResponse(body []byte) (string, error) {
	trimmed := bytes.TrimSpace(body)

	var bare string
	if err := json.Unmarshal(trimmed, &bare); err == nil {
		if bare == "" {
			return "", errors.New("fal: realtime token response was an empty string")
		}
		return bare, nil
	}

	var obj struct {
		Token  string `json:"token"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(trimmed, &obj); err == nil {
		if obj.Token != "" {
			return obj.Token, nil
		}
		if obj.Detail != "" {
			return "", fmt.Errorf("fal: realtime token request rejected: %s", obj.Detail)
		}
	}

	return "", fmt.Errorf("fal: unexpected realtime token response: %s", string(body))
}

// realtimeURL builds the WebSocket URL for a realtime connection, converting the
// run base URL to a ws/wss scheme and attaching the app route and query.
func (c *Client) realtimeURL(id appID, cfg *realtimeConfig, token string) (string, error) {
	u, err := url.Parse(c.runURL)
	if err != nil {
		return "", fmt.Errorf("fal: parsing run URL: %w", err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	case "ws", "wss":
		// already a WebSocket scheme
	default:
		return "", fmt.Errorf("fal: unsupported run URL scheme %q", u.Scheme)
	}

	var b strings.Builder
	b.WriteByte('/')
	if id.Namespace != "" {
		b.WriteString(id.Namespace)
		b.WriteByte('/')
	}
	b.WriteString(id.Owner)
	b.WriteByte('/')
	b.WriteString(id.Alias)
	if id.Path != "" {
		b.WriteByte('/')
		b.WriteString(id.Path)
	}
	b.WriteString(cfg.path)
	u.Path = b.String()

	q := url.Values{}
	if token != "" {
		q.Set("fal_jwt_token", token)
	}
	if cfg.maxBuffering != 0 {
		q.Set("max_buffering", strconv.Itoa(cfg.maxBuffering))
	}
	u.RawQuery = q.Encode()

	return u.String(), nil
}

// Send msgpack-encodes input and transmits it as a binary WebSocket frame.
func (rc *RealtimeConn) Send(ctx context.Context, input any) error {
	data, err := msgpack.Marshal(input)
	if err != nil {
		return fmt.Errorf("fal: encoding realtime message: %w", err)
	}
	if err := rc.conn.Write(ctx, websocket.MessageBinary, data); err != nil {
		return fmt.Errorf("fal: sending realtime message: %w", err)
	}
	return nil
}

// Recv reads the next application message from the connection, returning it as
// JSON. Binary frames are decoded from msgpack and re-encoded as JSON; text
// frames are treated as JSON. Server "x-fal-message" frames are skipped; an
// "x-fal-error" frame is returned as a *RealtimeError. A clean close returns
// io.EOF.
func (rc *RealtimeConn) Recv(ctx context.Context) (json.RawMessage, error) {
	for {
		typ, data, err := rc.conn.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				return nil, io.EOF
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, fmt.Errorf("fal: reading realtime message: %w", err)
		}

		switch typ {
		case websocket.MessageBinary:
			out, derr := decodeMsgpackToJSON(data)
			if derr != nil {
				return nil, derr
			}
			return out, nil
		case websocket.MessageText:
			out, deliver, terr := handleTextFrame(data)
			if terr != nil {
				return nil, terr
			}
			if !deliver {
				continue
			}
			return out, nil
		default:
			return nil, fmt.Errorf("fal: unexpected realtime frame type %v", typ)
		}
	}
}

// Close closes the connection with a normal-closure status. It is idempotent;
// repeated calls return the result of the first close.
func (rc *RealtimeConn) Close() error {
	rc.closeOnce.Do(func() {
		rc.closeErr = rc.conn.Close(websocket.StatusNormalClosure, "")
	})
	return rc.closeErr
}

// decodeMsgpackToJSON decodes a msgpack frame into a generic value with string
// map keys and re-encodes it as JSON.
func decodeMsgpackToJSON(data []byte) (json.RawMessage, error) {
	dec := msgpack.NewDecoder(bytes.NewReader(data))
	dec.UseLooseInterfaceDecoding(true)

	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("fal: decoding realtime message: %w", err)
	}

	out, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("fal: re-encoding realtime message: %w", err)
	}
	return out, nil
}

// handleTextFrame interprets a text frame. It reports whether the frame should
// be delivered to the caller (false for silently skipped control frames) and
// returns a *RealtimeError for error frames.
func handleTextFrame(data []byte) (json.RawMessage, bool, error) {
	if json.Valid(data) {
		var typed struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &typed); err == nil {
			switch typed.Type {
			case "x-fal-error":
				return nil, false, parseRealtimeError(data)
			case "x-fal-message":
				return nil, false, nil
			}
		}
		out := make(json.RawMessage, len(data))
		copy(out, data)
		return out, true, nil
	}

	wrapped, err := json.Marshal(struct {
		Type    string `json:"type"`
		Payload string `json:"payload"`
	}{Type: "text", Payload: string(data)})
	if err != nil {
		return nil, false, fmt.Errorf("fal: wrapping realtime text frame: %w", err)
	}
	return wrapped, true, nil
}

// parseRealtimeError builds a *RealtimeError from an x-fal-error frame.
func parseRealtimeError(data []byte) error {
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("fal: decoding realtime error frame: %w", err)
	}

	code := "UNKNOWN_ERROR"
	if v, ok := payload["error"].(string); ok && v != "" {
		code = v
	}
	reason, _ := payload["reason"].(string)

	return &RealtimeError{Code: code, Reason: reason, Payload: payload}
}
