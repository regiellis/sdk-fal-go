package fal

import (
	"encoding/json"
	"fmt"
)

// Status is the state of a queued request. It is one of [Queued], [InProgress],
// or [Completed].
type Status interface{ isStatus() }

// Queued means the request is waiting in the queue.
type Queued struct {
	// Position is the request's place in the queue.
	Position int
}

// InProgress means the request is being processed.
type InProgress struct {
	// Logs holds log entries produced so far, when logs were requested.
	Logs []LogEntry
}

// Completed means the request has finished. A non-empty Error reports a
// server-side failure.
type Completed struct {
	// Logs holds the request's log entries, when logs were requested.
	Logs []LogEntry
	// Metrics holds server-reported timing and resource metrics, when present.
	Metrics map[string]any
	// Error is the failure message, empty on success.
	Error string
	// ErrorType classifies the failure, when the server provides one.
	ErrorType string
}

func (Queued) isStatus()     {}
func (InProgress) isStatus() {}
func (Completed) isStatus()  {}

// LogEntry is a single untyped log record from the queue. Its shape is defined
// by the upstream model; the "message" key is the one commonly present.
type LogEntry map[string]any

// parseStatus decodes a queue status payload into a [Status]. An unrecognized
// or missing status field is an error.
func parseStatus(body []byte) (Status, error) {
	var p struct {
		Status        string         `json:"status"`
		QueuePosition int            `json:"queue_position"`
		Logs          []LogEntry     `json:"logs"`
		Metrics       map[string]any `json:"metrics"`
		Error         string         `json:"error"`
		ErrorType     string         `json:"error_type"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("fal: decoding status: %w", err)
	}

	switch p.Status {
	case "IN_QUEUE":
		return Queued{Position: p.QueuePosition}, nil
	case "IN_PROGRESS":
		return InProgress{Logs: p.Logs}, nil
	case "COMPLETED":
		return Completed{
			Logs:      p.Logs,
			Metrics:   p.Metrics,
			Error:     p.Error,
			ErrorType: p.ErrorType,
		}, nil
	case "":
		return nil, fmt.Errorf("fal: status response missing status field")
	default:
		return nil, fmt.Errorf("fal: unknown request status %q", p.Status)
	}
}
