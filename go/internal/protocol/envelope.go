package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

// Error codes shared with the TypeScript host. The host treats -32601 as
// "this agent lacks the method" and falls back (automation-port.ts,
// server-operations.ts), so it must not be reused for other failures.
const (
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
)

// RequestID is a string or an integer. Mirrors nexProtocolRequestIdSchema:
// z.union([z.string(), z.number().int()]).
type RequestID struct {
	isString bool
	str      string
	num      int64
}

func StringID(s string) RequestID { return RequestID{isString: true, str: s} }
func IntID(n int64) RequestID     { return RequestID{num: n} }

func (id RequestID) IsString() bool { return id.isString }
func (id RequestID) String() string {
	if id.isString {
		return id.str
	}
	return fmt.Sprintf("%d", id.num)
}

func (id RequestID) MarshalJSON() ([]byte, error) {
	if id.isString {
		return json.Marshal(id.str)
	}
	return json.Marshal(id.num)
}

// maxSafeInteger is JS Number.MAX_SAFE_INTEGER; zod's z.number().int() rejects
// values outside it, so the host would drop a frame carrying one.
const maxSafeInteger = 1<<53 - 1

// strictSafeInt parses a JSON number that zod's z.number().int() would accept.
// json.Unmarshal into a float64 silently maps null to 0, so the first byte is
// checked explicitly instead of relying on the decode error.
func strictSafeInt(raw json.RawMessage) (int64, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return 0, errors.New("missing number")
	}
	if c := trimmed[0]; c != '-' && (c < '0' || c > '9') {
		return 0, errors.New("not a number")
	}
	var f float64
	if err := json.Unmarshal(trimmed, &f); err != nil {
		return 0, errors.New("not a number")
	}
	if f != math.Trunc(f) || math.Abs(f) > maxSafeInteger {
		return 0, errors.New("not a safe integer")
	}
	return int64(f), nil
}

func parseRequestID(raw json.RawMessage) (RequestID, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return RequestID{}, errors.New("missing id")
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return RequestID{}, err
		}
		return StringID(s), nil
	}
	n, err := strictSafeInt(trimmed)
	if err != nil {
		return RequestID{}, errors.New("id must be a string or a safe integer")
	}
	return IntID(n), nil
}

// Trace mirrors nexProtocolTraceSchema (strict, all fields optional).
type Trace struct {
	Traceparent string `json:"traceparent,omitempty"`
	TraceID     string `json:"traceId,omitempty"`
	ParentID    string `json:"parentId,omitempty"`
	SpanID      string `json:"spanId,omitempty"`
}

// RPCError is the payload of an error message.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// Kind tells the four message shapes apart. The wire has no "jsonrpc" or "type"
// field; the shape is decided by which keys are present, as in the zod union.
type Kind int

const (
	KindRequest Kind = iota + 1
	KindNotification
	KindResponse
	KindError
)

func (k Kind) String() string {
	switch k {
	case KindRequest:
		return "request"
	case KindNotification:
		return "notification"
	case KindResponse:
		return "response"
	case KindError:
		return "error"
	}
	return "unknown"
}

// Message is one decoded frame. Params and Result keep their raw JSON so the
// layer does not need to know any method schema; HasParams/HasResult tell an
// absent key from an explicit null (the zod schema treats them differently
// from "missing" only for result, which is required).
type Message struct {
	Kind      Kind
	ID        RequestID
	Method    string
	Params    json.RawMessage
	HasParams bool
	Result    json.RawMessage
	Error     *RPCError
	Trace     *Trace
}

var (
	requestKeys      = map[string]bool{"id": true, "method": true, "params": true, "trace": true}
	notificationKeys = map[string]bool{"method": true, "params": true, "trace": true}
	responseKeys     = map[string]bool{"id": true, "result": true}
	errorKeys        = map[string]bool{"id": true, "error": true}
)

// ErrInvalidMessage wraps every reason a line is not a valid protocol message.
var ErrInvalidMessage = errors.New("invalid protocol message")

func onlyKeys(obj map[string]json.RawMessage, allowed map[string]bool) bool {
	for k := range obj {
		if !allowed[k] {
			return false
		}
	}
	return true
}

func nonEmptyTrimmed(raw json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, strings.TrimSpace(s) != ""
}

func parseTrace(raw json.RawMessage) (*Trace, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, errors.New("trace must be an object")
	}
	t := &Trace{}
	for k, v := range obj {
		s, ok := nonEmptyTrimmed(v)
		if !ok {
			return nil, fmt.Errorf("trace.%s must be a non-empty string", k)
		}
		switch k {
		case "traceparent":
			t.Traceparent = s
		case "traceId":
			t.TraceID = s
		case "parentId":
			t.ParentID = s
		case "spanId":
			t.SpanID = s
		default:
			return nil, fmt.Errorf("unknown trace key %q", k)
		}
	}
	return t, nil
}

func parseRPCError(raw json.RawMessage) (*RPCError, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, errors.New("error must be an object")
	}
	e := &RPCError{}
	var haveCode, haveMessage bool
	for k, v := range obj {
		switch k {
		case "code":
			n, err := strictSafeInt(v)
			if err != nil {
				return nil, errors.New("error.code must be an integer")
			}
			e.Code, haveCode = int(n), true
		case "message":
			s, ok := nonEmptyTrimmed(v)
			if !ok {
				return nil, errors.New("error.message must be a non-empty string")
			}
			e.Message, haveMessage = s, true
		case "data":
			e.Data = v
		default:
			return nil, fmt.Errorf("unknown error key %q", k)
		}
	}
	if !haveCode || !haveMessage {
		return nil, errors.New("error needs code and message")
	}
	return e, nil
}

// ParseMessage decodes one JSON line with the same acceptance rules as
// nexProtocolMessageSchema (strict objects; the four shapes). Anything the
// host would reject must be rejected here too, because the host closes the
// whole transport on a bad frame (protocol_parse_error).
func ParseMessage(line []byte) (Message, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(line, &obj); err != nil {
		return Message{}, fmt.Errorf("%w: %v", ErrInvalidMessage, err)
	}
	if obj == nil { // the literal null
		return Message{}, fmt.Errorf("%w: not an object", ErrInvalidMessage)
	}
	_, hasID := obj["id"]
	_, hasMethod := obj["method"]
	_, hasResult := obj["result"]
	_, hasError := obj["error"]

	fail := func(format string, args ...any) (Message, error) {
		return Message{}, fmt.Errorf("%w: %s", ErrInvalidMessage, fmt.Sprintf(format, args...))
	}

	switch {
	case hasMethod:
		if hasResult || hasError {
			return fail("method with result/error")
		}
		m := Message{Kind: KindNotification}
		allowed := notificationKeys
		if hasID {
			m.Kind, allowed = KindRequest, requestKeys
		}
		if !onlyKeys(obj, allowed) {
			return fail("unknown keys for %s", m.Kind)
		}
		method, ok := nonEmptyTrimmed(obj["method"])
		if !ok {
			return fail("method must be a non-empty string")
		}
		m.Method = method
		if hasID {
			id, err := parseRequestID(obj["id"])
			if err != nil {
				return fail("%v", err)
			}
			m.ID = id
		}
		if p, ok := obj["params"]; ok {
			m.Params, m.HasParams = p, true
		}
		if t, ok := obj["trace"]; ok {
			tr, err := parseTrace(t)
			if err != nil {
				return fail("%v", err)
			}
			m.Trace = tr
		}
		return m, nil
	case hasID && hasResult && !hasError:
		if !onlyKeys(obj, responseKeys) {
			return fail("unknown keys for response")
		}
		id, err := parseRequestID(obj["id"])
		if err != nil {
			return fail("%v", err)
		}
		return Message{Kind: KindResponse, ID: id, Result: obj["result"]}, nil
	case hasID && hasError && !hasResult:
		if !onlyKeys(obj, errorKeys) {
			return fail("unknown keys for error")
		}
		id, err := parseRequestID(obj["id"])
		if err != nil {
			return fail("%v", err)
		}
		e, err := parseRPCError(obj["error"])
		if err != nil {
			return fail("%v", err)
		}
		return Message{Kind: KindError, ID: id, Error: e}, nil
	}
	return fail("no message shape matches")
}

// wire structs keep the field order the TypeScript side emits
// (JSON.stringify of an object literal): id, method, params, trace / id, result /
// id, error.
type requestWire struct {
	ID     RequestID       `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
	Trace  *Trace          `json:"trace,omitempty"`
}
type notificationWire struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
	Trace  *Trace          `json:"trace,omitempty"`
}
type responseWire struct {
	ID     RequestID       `json:"id"`
	Result json.RawMessage `json:"result"`
}
type errorWire struct {
	ID    RequestID `json:"id"`
	Error *RPCError `json:"error"`
}

// Encode serialises a message to one JSON line without the trailing newline.
// json.Marshal escapes U+2028/U+2029 and never emits a raw LF inside a string,
// which is what keeps LF framing safe.
func Encode(m Message) ([]byte, error) {
	switch m.Kind {
	case KindRequest:
		return json.Marshal(requestWire{ID: m.ID, Method: m.Method, Params: orNil(m.Params, m.HasParams), Trace: m.Trace})
	case KindNotification:
		return json.Marshal(notificationWire{Method: m.Method, Params: orNil(m.Params, m.HasParams), Trace: m.Trace})
	case KindResponse:
		result := m.Result
		if len(result) == 0 {
			result = json.RawMessage("null")
		}
		return json.Marshal(responseWire{ID: m.ID, Result: result})
	case KindError:
		if m.Error == nil {
			return nil, errors.New("error message without error payload")
		}
		return json.Marshal(errorWire{ID: m.ID, Error: m.Error})
	}
	return nil, fmt.Errorf("cannot encode message kind %d", m.Kind)
}

func orNil(raw json.RawMessage, has bool) json.RawMessage {
	if !has || len(raw) == 0 {
		return nil
	}
	return raw
}
