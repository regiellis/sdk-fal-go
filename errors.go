package fal

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// ErrMissingCredentials is returned by the default credentials provider when no
// credentials can be resolved from the environment or the saved CLI tokens.
var ErrMissingCredentials = errors.New("fal: no credentials found; set FAL_KEY (or FAL_KEY_ID and FAL_KEY_SECRET), or log in with the fal CLI")

// APIError represents a non-success HTTP response from the fal API.
//
// Message is taken from the response body's "detail" field when the body is
// JSON, otherwise it holds the raw body text. ErrorType comes from the body's
// "error_type" field when present, falling back to the X-Fal-Error-Type header.
type APIError struct {
	// StatusCode is the HTTP status code of the response.
	StatusCode int
	// Message describes the failure.
	Message string
	// ErrorType is the fal error classification, when the server provides one.
	ErrorType string
	// Headers is the response header set.
	Headers http.Header
	// Body is the raw response body.
	Body []byte
}

// Error implements the error interface.
func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("fal: request failed with status %d", e.StatusCode)
	}
	return fmt.Sprintf("fal: request failed with status %d: %s", e.StatusCode, e.Message)
}

// newAPIError builds an APIError from a response and its already-read body. The
// caller owns reading and buffering the body; this function does not touch
// resp.Body.
func newAPIError(resp *http.Response, body []byte) *APIError {
	e := &APIError{
		StatusCode: resp.StatusCode,
		Headers:    resp.Header,
		Body:       body,
	}

	var parsed struct {
		Detail    json.RawMessage `json:"detail"`
		ErrorType string          `json:"error_type"`
	}
	if len(body) > 0 && json.Unmarshal(body, &parsed) == nil {
		e.Message = detailMessage(parsed.Detail)
		e.ErrorType = parsed.ErrorType
	}

	if e.Message == "" {
		e.Message = string(body)
	}
	if e.ErrorType == "" {
		e.ErrorType = resp.Header.Get(headerFalErrorType)
	}
	return e
}

// detailMessage extracts a human-readable message from a JSON "detail" value.
// The field may be a plain string or a structured value; anything that is not a
// string is returned as its compact JSON form.
func detailMessage(detail json.RawMessage) string {
	if len(detail) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(detail, &s); err == nil {
		return s
	}
	return string(detail)
}
