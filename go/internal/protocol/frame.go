package protocol

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"sync"
)

// DefaultMaxFrameBytes bounds one line. A logical v4 frame may be up to 16 MiB
// (PROTOCOL_V4_LIMITS.logicalFrameAssemblyMaxBytes) and travels as fragments
// well below 1 MiB, but legacy responses are not fragmented, so keep headroom.
const DefaultMaxFrameBytes = 32 << 20

// ErrFrameTooLong is returned when a line exceeds the reader's limit. The
// reader cannot resynchronise after it, so callers must stop reading.
var ErrFrameTooLong = errors.New("protocol frame exceeds maximum length")

// FrameReader splits a byte stream into frames on LF only. It deliberately does
// not use bufio.Scanner/ScanLines semantics beyond LF and never treats U+2028 or
// U+2029 as a separator: those can appear raw inside JSON strings and splitting
// on them would cut a valid message in half (see nexStdioTransport.ts).
type FrameReader struct {
	r   *bufio.Reader
	max int
	buf bytes.Buffer
}

func NewFrameReader(r io.Reader, maxFrameBytes int) *FrameReader {
	if maxFrameBytes <= 0 {
		maxFrameBytes = DefaultMaxFrameBytes
	}
	return &FrameReader{r: bufio.NewReaderSize(r, 64<<10), max: maxFrameBytes}
}

// Next returns the next non-blank frame without its line terminator.
// Mirrors the host: one trailing CR is stripped, and lines that are empty after
// trimming whitespace are skipped. A final unterminated line is returned before
// io.EOF, as the host does on stdout end. The returned slice is only valid until
// the next call.
func (fr *FrameReader) Next() ([]byte, error) {
	for {
		fr.buf.Reset()
		for {
			chunk, err := fr.r.ReadSlice('\n')
			if fr.buf.Len()+len(chunk) > fr.max+1 { // +1 for the LF itself
				return nil, ErrFrameTooLong
			}
			fr.buf.Write(chunk)
			if err == nil {
				break
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if errors.Is(err, io.EOF) {
				line := fr.buf.Bytes()
				if frame, ok := cleanLine(line); ok {
					return frame, nil
				}
				return nil, io.EOF
			}
			return nil, err
		}
		line := fr.buf.Bytes()
		line = line[:len(line)-1] // drop LF
		if frame, ok := cleanLine(line); ok {
			return frame, nil
		}
	}
}

func cleanLine(line []byte) ([]byte, bool) {
	if n := len(line); n > 0 && line[n-1] == '\r' {
		line = line[:n-1]
	}
	if len(bytes.TrimSpace(line)) == 0 {
		return nil, false
	}
	return line, true
}

// FrameWriter serialises writes so concurrent senders never interleave bytes.
// Each Write emits exactly one line.
type FrameWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func NewFrameWriter(w io.Writer) *FrameWriter { return &FrameWriter{w: w} }

// WriteFrame writes payload plus LF in a single Write call. payload must not
// contain a raw LF; Encode output never does.
func (fw *FrameWriter) WriteFrame(payload []byte) error {
	if bytes.IndexByte(payload, '\n') >= 0 {
		return errors.New("frame payload contains a raw line feed")
	}
	line := make([]byte, 0, len(payload)+1)
	line = append(line, payload...)
	line = append(line, '\n')
	fw.mu.Lock()
	defer fw.mu.Unlock()
	_, err := fw.w.Write(line)
	return err
}

// WriteMessage encodes and writes one protocol message.
func (fw *FrameWriter) WriteMessage(m Message) error {
	payload, err := Encode(m)
	if err != nil {
		return err
	}
	return fw.WriteFrame(payload)
}
