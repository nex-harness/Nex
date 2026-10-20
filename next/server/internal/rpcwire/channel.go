package rpcwire

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"
)

// RequestType is the first header element of a client-to-server payload.
//
// Values mirror RequestType in packages/rpc/src/channels.shared.ts:18-23.
type RequestType int64

// Request types sent by ChannelClient.
const (
	RequestPromise       RequestType = 100
	RequestPromiseCancel RequestType = 101
	RequestEventListen   RequestType = 102
	RequestEventDispose  RequestType = 103
)

// ResponseType is the first header element of a server-to-client payload.
//
// Values mirror ResponseType in packages/rpc/src/channels.shared.ts:25-31.
type ResponseType int64

// Response types sent by ChannelServer.
const (
	ResponseInitialize      ResponseType = 200
	ResponsePromiseSuccess  ResponseType = 201
	ResponsePromiseError    ResponseType = 202
	ResponsePromiseErrorObj ResponseType = 203
	ResponseEventFire       ResponseType = 204
)

// ErrMalformedPayload reports a payload whose header array has the wrong
// shape for the declared request or response type.
var ErrMalformedPayload = errors.New("rpcwire: malformed channel payload")

// Request is a decoded client-to-server channel message.
//
// Header layouts written by ChannelClient (packages/rpc/src/channelClient.ts):
//   - Promise (100):      [type, id, channelName, name] + body = arg    (lines 178-193)
//   - PromiseCancel (101): [type, id]                    + body = undefined (lines 195-207)
//   - EventListen (102):  [type, id, channelName, name] + body = arg
//   - EventDispose (103): [type, id]                     + body = undefined
//
// Channel and Name are empty for the cancel/dispose forms.
type Request struct {
	Type    RequestType
	ID      uint32
	Channel string
	Name    string
	Arg     Value
}

// DecodeRequest decodes a Channel RPC payload (frame payload, not frame header).
func DecodeRequest(payload []byte) (Request, error) {
	header, body, err := decodePair(payload)
	if err != nil {
		return Request{}, err
	}

	arr, ok := header.([]any)
	if !ok || len(arr) < 2 {
		return Request{}, fmt.Errorf("%w: request header is not an array with at least 2 elements", ErrMalformedPayload)
	}

	typ, ok := asInt(arr[0])
	if !ok {
		return Request{}, fmt.Errorf("%w: request type %T is not an integer", ErrMalformedPayload, arr[0])
	}
	id, ok := asInt(arr[1])
	if !ok || id < 0 {
		return Request{}, fmt.Errorf("%w: request id is not a non-negative integer", ErrMalformedPayload)
	}

	req := Request{Type: RequestType(typ), ID: uint32(id), Arg: body}

	switch req.Type {
	case RequestPromise, RequestEventListen:
		if len(arr) < 4 {
			return Request{}, fmt.Errorf("%w: %s header needs 4 elements, got %d", ErrMalformedPayload, req.Type, len(arr))
		}
		channel, ok := arr[2].(string)
		if !ok {
			return Request{}, fmt.Errorf("%w: channel name is not a string", ErrMalformedPayload)
		}
		name, ok := arr[3].(string)
		if !ok {
			return Request{}, fmt.Errorf("%w: method name is not a string", ErrMalformedPayload)
		}
		req.Channel = channel
		req.Name = name
	case RequestPromiseCancel, RequestEventDispose:
		// No channel/name; the body is the undefined placeholder.
	default:
		return Request{}, fmt.Errorf("%w: unknown request type %d", ErrMalformedPayload, typ)
	}

	return req, nil
}

// String implements fmt.Stringer.
func (t RequestType) String() string {
	switch t {
	case RequestPromise:
		return "Promise"
	case RequestPromiseCancel:
		return "PromiseCancel"
	case RequestEventListen:
		return "EventListen"
	case RequestEventDispose:
		return "EventDispose"
	default:
		return "RequestType(" + strconv.FormatInt(int64(t), 10) + ")"
	}
}

// String implements fmt.Stringer.
func (t ResponseType) String() string {
	switch t {
	case ResponseInitialize:
		return "Initialize"
	case ResponsePromiseSuccess:
		return "PromiseSuccess"
	case ResponsePromiseError:
		return "PromiseError"
	case ResponsePromiseErrorObj:
		return "PromiseErrorObj"
	case ResponseEventFire:
		return "EventFire"
	default:
		return "ResponseType(" + strconv.FormatInt(int64(t), 10) + ")"
	}
}

// EncodeRequest builds a client payload exactly as ChannelClient.sendRequest
// does (packages/rpc/src/channelClient.ts:178-193).
func EncodeRequest(reqType RequestType, id uint32, channel, name string, arg Value) ([]byte, error) {
	switch reqType {
	case RequestPromise, RequestEventListen:
	case RequestPromiseCancel, RequestEventDispose:
		if channel != "" || name != "" {
			return nil, fmt.Errorf("%w: %s carries no channel or method", ErrMalformedPayload, reqType)
		}
	default:
		return nil, fmt.Errorf("%w: unknown request type %d", ErrMalformedPayload, reqType)
	}

	header := []any{int64(reqType), int64(id)}
	if reqType == RequestPromise || reqType == RequestEventListen {
		header = append(header, channel, name)
	}
	return encodePair(header, arg)
}

// EncodePromiseCancel builds the cancellation payload sent on promise cancel.
func EncodePromiseCancel(id uint32) ([]byte, error) {
	return EncodeRequest(RequestPromiseCancel, id, "", "", Undefined{})
}

// EncodeEventDispose builds the payload sent when the last event listener is
// removed (packages/rpc/src/channelClient.ts:164-168, 195-207).
func EncodeEventDispose(id uint32) ([]byte, error) {
	return EncodeRequest(RequestEventDispose, id, "", "", Undefined{})
}

// Response is a decoded server-to-client channel message.
//
// Header layouts written by ChannelServer.sendResponse
// (packages/rpc/src/channelServer.ts:43-55):
//   - Initialize (200):       [type] + body = undefined
//   - PromiseSuccess (201):   [type, id] + body = value
//   - PromiseError (202):     [type, id] + body = {message, name, stack?}
//   - PromiseErrorObj (203):  [type, id] + body = error object
//   - EventFire (204):        [type, id] + body = event value
type Response struct {
	Type ResponseType
	ID   uint32
	Body Value
}

// DecodeResponse decodes a server-to-client Channel RPC payload.
func DecodeResponse(payload []byte) (Response, error) {
	header, body, err := decodePair(payload)
	if err != nil {
		return Response{}, err
	}

	arr, ok := header.([]any)
	if !ok || len(arr) < 1 {
		return Response{}, fmt.Errorf("%w: response header is not a non-empty array", ErrMalformedPayload)
	}
	typ, ok := asInt(arr[0])
	if !ok {
		return Response{}, fmt.Errorf("%w: response type %T is not an integer", ErrMalformedPayload, arr[0])
	}
	resp := Response{Type: ResponseType(typ), Body: body}
	if resp.Type == ResponseInitialize {
		return resp, nil
	}
	if len(arr) < 2 {
		return Response{}, fmt.Errorf("%w: %s header needs an id", ErrMalformedPayload, resp.Type)
	}
	id, ok := asInt(arr[1])
	if !ok || id < 0 {
		return Response{}, fmt.Errorf("%w: response id is not a non-negative integer", ErrMalformedPayload)
	}
	resp.ID = uint32(id)
	return resp, nil
}

// EncodeInitialize builds the initialize payload ChannelServer sends on
// connect (packages/rpc/src/channelServer.ts:29-31, 45-47).
func EncodeInitialize() ([]byte, error) {
	return encodePair([]any{int64(ResponseInitialize)}, Undefined{})
}

// EncodePromiseSuccess builds a PromiseSuccess response.
func EncodePromiseSuccess(id uint32, value Value) ([]byte, error) {
	return encodePair([]any{int64(ResponsePromiseSuccess), int64(id)}, value)
}

// EncodePromiseError builds a PromiseError response.
//
// The body is a JSON object with TypeScript's insertion order — message, name,
// stack — because the payload is produced by JSON.stringify on the TS side
// (packages/rpc/src/channelServer.ts:135-171). An empty stack omits the field,
// matching `stack: undefined`. stack is split on "\n" exactly like
// `error.stack.split("\n")`, whose first element is JS's "Error: message" line.
func EncodePromiseError(id uint32, message, name, stack string) ([]byte, error) {
	var payload []byte
	payload = append(payload, '{')
	payload = appendJSKey(payload, "message")
	payload = appendJSString(payload, message)
	payload = append(payload, ',')
	payload = appendJSKey(payload, "name")
	payload = appendJSString(payload, name)
	if stack != "" {
		payload = append(payload, ',')
		payload = appendJSKey(payload, "stack")
		payload = appendJSStringArray(payload, splitJSStack(stack))
	}
	payload = append(payload, '}')
	return encodePair([]any{int64(ResponsePromiseError), int64(id)}, json.RawMessage(payload))
}

// EncodePromiseErrorObj builds a PromiseErrorObj response for non-Error
// rejection values (packages/rpc/src/channelServer.ts:180-184).
func EncodePromiseErrorObj(id uint32, value Value) ([]byte, error) {
	return encodePair([]any{int64(ResponsePromiseErrorObj), int64(id)}, value)
}

// EncodeEventFire builds an EventFire response.
func EncodeEventFire(id uint32, value Value) ([]byte, error) {
	return encodePair([]any{int64(ResponseEventFire), int64(id)}, value)
}

// EncodeResponse rebuilds a server payload from a decoded Response. It is the
// generic counterpart of DecodeResponse and is used to prove byte-identical
// round trips against the TypeScript goldens.
func EncodeResponse(resp Response) ([]byte, error) {
	if resp.Type == ResponseInitialize {
		return encodePair([]any{int64(resp.Type)}, resp.Body)
	}
	return encodePair([]any{int64(resp.Type), int64(resp.ID)}, resp.Body)
}

// encodePair serializes header then body into one Channel payload.
func encodePair(header, body Value) ([]byte, error) {
	out, err := EncodeValue(header)
	if err != nil {
		return nil, fmt.Errorf("encoding header: %w", err)
	}
	out, err = appendValue(out, body)
	if err != nil {
		return nil, fmt.Errorf("encoding body: %w", err)
	}
	return out, nil
}

// decodePair splits a payload into its header and body values. Both values
// must be present; ChannelClient always writes the undefined body.
func decodePair(payload []byte) (Value, Value, error) {
	header, rest, err := DecodeValuePrefix(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("decoding header: %w", err)
	}
	body, err := DecodeValue(rest)
	if err != nil {
		return nil, nil, fmt.Errorf("decoding body: %w", err)
	}
	return header, body, nil
}

func asInt(v Value) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case int:
		return int64(x), true
	case uint32:
		return int64(x), true
	case json.RawMessage:
		// Large integers fall back to the JSON encoding branch.
		n, err := strconv.ParseInt(string(x), 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// splitJSStack mirrors "stack".split("\n"): an empty string yields one empty
// element, and a trailing newline yields a trailing empty element.
func splitJSStack(stack string) []string {
	out := []string{""}
	for i := 0; i < len(stack); i++ {
		if stack[i] == '\n' {
			out = append(out, "")
			continue
		}
		out[len(out)-1] += string(stack[i])
	}
	return out
}

func appendJSKey(dst []byte, key string) []byte {
	dst = appendJSString(dst, key)
	return append(dst, ':')
}

func appendJSStringArray(dst []byte, values []string) []byte {
	dst = append(dst, '[')
	for i, s := range values {
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = appendJSString(dst, s)
	}
	return append(dst, ']')
}

// appendJSString quotes s the way ECMAScript JSON.stringify does for strings:
// only quote, backslash and C0 controls are escaped, and non-ASCII text is
// emitted as UTF-8. Go's encoding/json additionally escapes HTML characters
// and U+2028/U+2029, which would break byte equality with the TS side.
func appendJSString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch c {
			case '"', '\\':
				dst = append(dst, '\\', c)
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\f':
				dst = append(dst, '\\', 'f')
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			default:
				if c < 0x20 {
					dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
				} else {
					dst = append(dst, c)
				}
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// JSON has no representation for invalid UTF-8. Emit U+FFFD, which
			// is what TextDecoder produces when the TypeScript side reads the
			// same bytes, instead of writing a malformed JSON document.
			dst = append(dst, "\uFFFD"...)
			i++
			continue
		}
		dst = append(dst, s[i:i+size]...)
		i += size
	}
	return append(dst, '"')
}

const hexDigits = "0123456789abcdef"
