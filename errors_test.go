package fal

import (
	"errors"
	"net/http"
	"testing"
)

func TestNewAPIError(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		header        http.Header
		body          string
		wantMessage   string
		wantErrorType string
	}{
		{
			name:          "json detail and error_type",
			status:        422,
			body:          `{"detail":"invalid input","error_type":"validation"}`,
			wantMessage:   "invalid input",
			wantErrorType: "validation",
		},
		{
			name:          "json without error_type falls back to header",
			status:        400,
			header:        http.Header{headerFalErrorType: {"bad_request"}},
			body:          `{"detail":"nope"}`,
			wantMessage:   "nope",
			wantErrorType: "bad_request",
		},
		{
			name:        "structured detail returned as json",
			status:      422,
			body:        `{"detail":[{"loc":["body"],"msg":"required"}]}`,
			wantMessage: `[{"loc":["body"],"msg":"required"}]`,
		},
		{
			name:        "non-json body used verbatim",
			status:      500,
			body:        "internal server error",
			wantMessage: "internal server error",
		},
		{
			name:          "non-json body with error type header",
			status:        503,
			header:        http.Header{headerFalErrorType: {"unavailable"}},
			body:          "<html>nginx</html>",
			wantMessage:   "<html>nginx</html>",
			wantErrorType: "unavailable",
		},
		{
			name:        "empty body",
			status:      500,
			body:        "",
			wantMessage: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := tt.header
			if header == nil {
				header = http.Header{}
			}
			resp := &http.Response{StatusCode: tt.status, Header: header}
			apiErr := newAPIError(resp, []byte(tt.body))

			if apiErr.StatusCode != tt.status {
				t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, tt.status)
			}
			if apiErr.Message != tt.wantMessage {
				t.Errorf("Message = %q, want %q", apiErr.Message, tt.wantMessage)
			}
			if apiErr.ErrorType != tt.wantErrorType {
				t.Errorf("ErrorType = %q, want %q", apiErr.ErrorType, tt.wantErrorType)
			}
			if string(apiErr.Body) != tt.body {
				t.Errorf("Body = %q, want %q", apiErr.Body, tt.body)
			}
		})
	}
}

func TestAPIErrorAsTarget(t *testing.T) {
	resp := &http.Response{StatusCode: 429, Header: http.Header{}}
	var err error = newAPIError(resp, []byte(`{"detail":"slow down"}`))

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatal("errors.As did not match *APIError")
	}
	if apiErr.StatusCode != 429 {
		t.Errorf("StatusCode = %d, want 429", apiErr.StatusCode)
	}
}

func TestAPIErrorMessage(t *testing.T) {
	withMsg := &APIError{StatusCode: 422, Message: "invalid input"}
	if got, want := withMsg.Error(), "fal: request failed with status 422: invalid input"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	noMsg := &APIError{StatusCode: 500}
	if got, want := noMsg.Error(), "fal: request failed with status 500"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestErrMissingCredentialsSentinel(t *testing.T) {
	if !errors.Is(ErrMissingCredentials, ErrMissingCredentials) {
		t.Fatal("ErrMissingCredentials must match itself with errors.Is")
	}
}
