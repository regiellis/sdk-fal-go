package fal

import "testing"

// TestHeaderConstants pins the per-request header names to the exact strings the
// fal gateway expects, guarding against accidental edits.
func TestHeaderConstants(t *testing.T) {
	tests := []struct {
		got  string
		want string
	}{
		{headerRequestTimeout, "X-Fal-Request-Timeout"},
		{headerRequestTimeoutType, "X-Fal-Request-Timeout-Type"},
		{headerRunnerHint, "X-Fal-Runner-Hint"},
		{headerQueuePriority, "X-Fal-Queue-Priority"},
		{headerFalRequestID, "X-Fal-Request-Id"},
		{headerFalErrorType, "X-Fal-Error-Type"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("header constant = %q, want %q", tt.got, tt.want)
		}
	}
}
