package modelclient

import (
	"encoding/json"
	"errors"
)

// DecodeErrorKind classifies decoder-level failures.
type DecodeErrorKind string

const (
	// ErrInvalidResponse is an unparsable or schema-invalid chunk.
	ErrInvalidResponse DecodeErrorKind = "invalid_response"
)

// DecodeError is a decoder failure that becomes a model error at the client
// boundary.
type DecodeError struct {
	Kind    DecodeErrorKind
	Message string
	Cause   error
	Data    any
}

func (e *DecodeError) Error() string { return e.Message }
func (e *DecodeError) Unwrap() error { return e.Cause }

// StreamError is a provider error object delivered inside a 200 SSE payload
// ({"error": {...}}). It carries the raw chunk so the classifier can detect a
// provider business error from the body.
type StreamError struct {
	Message string
	Raw     json.RawMessage
}

func (e *StreamError) Error() string { return e.Message }

// IsStreamError reports whether err came from an SSE `error` payload.
func IsStreamError(err error) bool {
	var se *StreamError
	return errors.As(err, &se)
}

// IsDecodeError reports whether err is a decoder failure.
func IsDecodeError(err error) bool {
	var de *DecodeError
	return errors.As(err, &de)
}
