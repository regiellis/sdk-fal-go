package fal

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// collectSSE drains a decoder over raw into the slice of event data strings it
// yields, failing the test on any non-EOF error.
func collectSSE(t *testing.T, raw string) []string {
	t.Helper()
	dec := newSSEDecoder(strings.NewReader(raw))
	var events []string
	for {
		data, err := dec.next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return events
			}
			t.Fatalf("decoder.next: unexpected error: %v", err)
		}
		events = append(events, string(data))
	}
}

func TestSSEDecoder(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "single line data",
			raw:  "data: {\"a\":1}\n\n",
			want: []string{`{"a":1}`},
		},
		{
			name: "multiple events",
			raw:  "data: one\n\ndata: two\n\n",
			want: []string{"one", "two"},
		},
		{
			name: "multi line data joined with newline",
			raw:  "data: line1\ndata: line2\ndata: line3\n\n",
			want: []string{"line1\nline2\nline3"},
		},
		{
			name: "comments ignored",
			raw:  ": this is a comment\ndata: payload\n: another comment\n\n",
			want: []string{"payload"},
		},
		{
			name: "crlf terminators",
			raw:  "data: first\r\n\r\ndata: second\r\n\r\n",
			want: []string{"first", "second"},
		},
		{
			name: "event and id fields ignored",
			raw:  "event: update\nid: 42\ndata: value\nretry: 1000\n\n",
			want: []string{"value"},
		},
		{
			name: "event with no data produces nothing",
			raw:  "event: ping\nid: 7\n\ndata: real\n\n",
			want: []string{"real"},
		},
		{
			name: "value without leading space preserved",
			raw:  "data:nospace\n\n",
			want: []string{"nospace"},
		},
		{
			name: "only one leading space stripped",
			raw:  "data:  two-spaces\n\n",
			want: []string{" two-spaces"},
		},
		{
			name: "empty data field dispatches empty event",
			raw:  "data:\n\n",
			want: []string{""},
		},
		{
			name: "incomplete trailing event discarded",
			raw:  "data: done\n\ndata: partial\n",
			want: []string{"done"},
		},
		{
			name: "no events",
			raw:  ": just a comment\n\n",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collectSSE(t, tt.raw)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d events %q, want %d %q", len(got), got, len(tt.want), tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("event %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSSEDecoderLongLine(t *testing.T) {
	// A data payload larger than bufio.Scanner's default 64KB token limit, to
	// confirm the bufio.Reader-based decoder handles arbitrarily long lines.
	const size = 200 * 1024
	payload := strings.Repeat("x", size)
	raw := "data: " + payload + "\n\n"

	got := collectSSE(t, raw)
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0] != payload {
		t.Fatalf("payload length = %d, want %d", len(got[0]), size)
	}
}
