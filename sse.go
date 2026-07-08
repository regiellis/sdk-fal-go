package fal

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

// sseDecoder reads a text/event-stream body and yields each event's data.
//
// It implements the subset of the Server-Sent Events wire format that the fal
// streaming API uses: multi-line "data:" fields joined with a newline, events
// dispatched on a blank line, comment lines (leading ':') ignored, and the
// "event:", "id:", and "retry:" fields ignored. Both LF and CRLF line
// terminators are tolerated. It reads lines through a bufio.Reader, which grows
// to hold arbitrarily long lines rather than being capped like bufio.Scanner.
type sseDecoder struct {
	r *bufio.Reader
}

// newSSEDecoder wraps r in an SSE decoder.
func newSSEDecoder(r io.Reader) *sseDecoder {
	return &sseDecoder{r: bufio.NewReader(r)}
}

// next reads until it dispatches one event and returns that event's data as
// json.RawMessage. Events carrying no "data:" field produce nothing and are
// skipped. It returns io.EOF once the stream ends; per the SSE format, an
// incomplete trailing event (data not terminated by a blank line) is discarded.
func (d *sseDecoder) next() (json.RawMessage, error) {
	var data []byte
	hasData := false

	for {
		line, err := d.readLine()
		if err != nil && err != io.EOF {
			return nil, err
		}
		atEOF := err == io.EOF

		// Process the line unless it is the empty remainder that marks a clean
		// EOF with nothing pending on the line itself.
		if !atEOF || line != "" {
			switch {
			case line == "":
				// Blank line dispatches the accumulated event.
				if hasData {
					return json.RawMessage(data), nil
				}
			case line[0] == ':':
				// Comment line: ignored.
			default:
				field, value := splitSSEField(line)
				if field == "data" {
					if hasData {
						data = append(data, '\n')
					}
					data = append(data, value...)
					hasData = true
				}
				// "event", "id", "retry", and any unknown field are ignored.
			}
		}

		if atEOF {
			return nil, io.EOF
		}
	}
}

// readLine reads a single line and strips its trailing CR and/or LF terminator.
// The returned error mirrors bufio.Reader.ReadString, so a non-empty final line
// without a terminator is returned alongside io.EOF.
func (d *sseDecoder) readLine() (string, error) {
	s, err := d.r.ReadString('\n')
	s = strings.TrimSuffix(s, "\n")
	s = strings.TrimSuffix(s, "\r")
	return s, err
}

// splitSSEField splits an SSE field line into its name and value. When a colon
// is present, the name precedes it and the value follows, with a single leading
// space of the value removed. A line with no colon is a field name with an
// empty value.
func splitSSEField(line string) (field, value string) {
	idx := strings.IndexByte(line, ':')
	if idx < 0 {
		return line, ""
	}
	field = line[:idx]
	value = strings.TrimPrefix(line[idx+1:], " ")
	return field, value
}
