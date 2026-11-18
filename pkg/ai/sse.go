package ai

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"strings"
)

// SSEFrame represents a single Server-Sent Events message frame.
type SSEFrame struct {
	Event string
	Data  string
}

// SSEScanner reads arbitrarily large SSE frames without the 64KB bufio.Scanner limit.
type SSEScanner struct {
	r *bufio.Reader
}

// NewSSEScanner creates an SSEScanner over r with a 64KB initial read buffer.
func NewSSEScanner(r io.Reader) *SSEScanner {
	return &SSEScanner{
		r: bufio.NewReaderSize(r, 64*1024),
	}
}

// Next reads the next complete SSE frame until a blank line or EOF.
// Returns io.EOF when the stream ends cleanly with no pending frame.
func (s *SSEScanner) Next(ctx context.Context) (SSEFrame, error) {
	var eventName string
	var dataBuf bytes.Buffer
	hasData := false

	for {
		if err := ctx.Err(); err != nil {
			return SSEFrame{}, err
		}

		line, err := s.readFullLine()
		if err != nil && err != io.EOF {
			return SSEFrame{}, err
		}

		trimmed := strings.TrimRight(line, "\r\n")

		if trimmed == "" {
			if hasData || eventName != "" {
				return SSEFrame{
					Event: eventName,
					Data:  dataBuf.String(),
				}, nil
			}
			if err == io.EOF {
				return SSEFrame{}, io.EOF
			}
			continue
		}

		if !strings.HasPrefix(trimmed, ":") {
			field, val, hasColon := strings.Cut(trimmed, ":")
			if hasColon && strings.HasPrefix(val, " ") {
				val = val[1:]
			}
			switch field {
			case "event":
				eventName = val
			case "data":
				if hasData {
					dataBuf.WriteByte('\n')
				}
				dataBuf.WriteString(val)
				hasData = true
			}
		}

		if err == io.EOF {
			if hasData || eventName != "" {
				return SSEFrame{
					Event: eventName,
					Data:  dataBuf.String(),
				}, nil
			}
			return SSEFrame{}, io.EOF
		}
	}
}

func (s *SSEScanner) readFullLine() (string, error) {
	var buf bytes.Buffer
	for {
		chunk, isPrefix, err := s.r.ReadLine()
		if len(chunk) > 0 {
			buf.Write(chunk)
		}
		if err != nil {
			return buf.String(), err
		}
		if !isPrefix {
			return buf.String(), nil
		}
	}
}
