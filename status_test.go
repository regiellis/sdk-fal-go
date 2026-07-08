package fal

import "testing"

func TestParseStatus(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
		check   func(t *testing.T, s Status)
	}{
		{
			name: "in queue with position",
			body: `{"status":"IN_QUEUE","queue_position":3}`,
			check: func(t *testing.T, s Status) {
				q, ok := s.(Queued)
				if !ok {
					t.Fatalf("got %T, want Queued", s)
				}
				if q.Position != 3 {
					t.Fatalf("Position = %d, want 3", q.Position)
				}
			},
		},
		{
			name: "in progress with logs",
			body: `{"status":"IN_PROGRESS","logs":[{"message":"working"}]}`,
			check: func(t *testing.T, s Status) {
				p, ok := s.(InProgress)
				if !ok {
					t.Fatalf("got %T, want InProgress", s)
				}
				if len(p.Logs) != 1 || p.Logs[0]["message"] != "working" {
					t.Fatalf("Logs = %v, want one entry with message 'working'", p.Logs)
				}
			},
		},
		{
			name: "completed with metrics and error",
			body: `{"status":"COMPLETED","logs":[{"message":"done"}],"metrics":{"inference_time":1.5},"error":"boom","error_type":"UserError"}`,
			check: func(t *testing.T, s Status) {
				c, ok := s.(Completed)
				if !ok {
					t.Fatalf("got %T, want Completed", s)
				}
				if c.Error != "boom" || c.ErrorType != "UserError" {
					t.Fatalf("Error/ErrorType = %q/%q, want boom/UserError", c.Error, c.ErrorType)
				}
				if c.Metrics["inference_time"] != 1.5 {
					t.Fatalf("Metrics = %v, want inference_time 1.5", c.Metrics)
				}
			},
		},
		{
			name: "completed legacy without metrics or error",
			body: `{"status":"COMPLETED"}`,
			check: func(t *testing.T, s Status) {
				c, ok := s.(Completed)
				if !ok {
					t.Fatalf("got %T, want Completed", s)
				}
				if c.Metrics != nil {
					t.Fatalf("Metrics = %v, want nil", c.Metrics)
				}
				if c.Error != "" || c.ErrorType != "" {
					t.Fatalf("Error/ErrorType = %q/%q, want empty", c.Error, c.ErrorType)
				}
			},
		},
		{
			name:    "unknown status",
			body:    `{"status":"EXPLODED"}`,
			wantErr: true,
		},
		{
			name:    "missing status",
			body:    `{"queue_position":1}`,
			wantErr: true,
		},
		{
			name:    "malformed json",
			body:    `{not json`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseStatus([]byte(tt.body))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseStatus(%s) = %+v, want error", tt.body, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseStatus(%s) unexpected error: %v", tt.body, err)
			}
			tt.check(t, got)
		})
	}
}
