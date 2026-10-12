package rpc

import "fmt"

// Request and response type codes mirror packages/rpc/src/channels.shared.ts.
const (
	RequestPromise       = 100
	RequestPromiseCancel = 101
	RequestEventListen   = 102
	RequestEventDispose  = 103

	ResponseInitialize      = 200
	ResponsePromiseSuccess  = 201
	ResponsePromiseError    = 202
	ResponsePromiseErrorObj = 203
	ResponseEventFire       = 204
)

// RequestHeader is the first serialized value of a channel request:
//
//	[type, id, channelName, name]
//
// The second value on the wire is the argument (body), which is undefined for the
// cancel/dispose variants. Mirrors `sendRequest`/`sendCancelOrDispose` in channelClient.ts.
type RequestHeader struct {
	Type        int
	ID          int
	ChannelName string
	Name        string
}

// EncodeRequest serializes a channel request exactly as ChannelClient does: header first,
// then the body.
func EncodeRequest(header RequestHeader, body any) ([]byte, error) {
	w := NewWriter()
	payload := []any{int64(header.Type), int64(header.ID), header.ChannelName, header.Name}
	if err := w.Serialize(payload); err != nil {
		return nil, err
	}
	if err := serializeBody(w, body); err != nil {
		return nil, err
	}
	return w.Bytes(), nil
}

// EncodeCancelOrDispose serializes `[type, id]` followed by an undefined body.
func EncodeCancelOrDispose(kind int, id int) ([]byte, error) {
	w := NewWriter()
	if err := w.Serialize([]Value{int64(kind), int64(id)}); err != nil {
		return nil, err
	}
	if err := w.Serialize(Undefined); err != nil {
		return nil, err
	}
	return w.Bytes(), nil
}

// ResponseHeader is the first serialized value of a channel response: `[type, id]` for
// everything except Initialize, which is just `[type]` (see `sendResponse`).
type ResponseHeader struct {
	Type int
	ID   int
}

// EncodeResponse serializes a channel response exactly as ChannelServer does. Note that
// `send(header, body = undefined)` always serializes a body, so Initialize also carries an
// Undefined-tagged body.
func EncodeResponse(header ResponseHeader, body any) ([]byte, error) {
	w := NewWriter()
	if header.Type == ResponseInitialize {
		if err := w.Serialize([]Value{int64(header.Type)}); err != nil {
			return nil, err
		}
	} else {
		if err := w.Serialize([]Value{int64(header.Type), int64(header.ID)}); err != nil {
			return nil, err
		}
	}
	if err := w.Serialize(bodyOrUndefined(body)); err != nil {
		return nil, err
	}
	return w.Bytes(), nil
}

// bodyOrUndefined maps a nil body to the Undefined tag, matching TS `arg?: any`.
func bodyOrUndefined(body any) any {
	if body == nil {
		return Undefined
	}
	return body
}

// DecodeRequest reads a request header and its body from a serialized message.
func DecodeRequest(data []byte) (RequestHeader, any, error) {
	r := NewReader(data)
	headerValue, err := r.Deserialize()
	if err != nil {
		return RequestHeader{}, nil, err
	}
	fields, ok := headerValue.([]any)
	if !ok || len(fields) < 2 {
		return RequestHeader{}, nil, fmt.Errorf("rpc: request header must be an array")
	}
	header := RequestHeader{Type: int(toInt(fields[0])), ID: int(toInt(fields[1]))}
	if len(fields) >= 4 {
		header.ChannelName = toString(fields[2])
		header.Name = toString(fields[3])
	}
	body, err := r.Deserialize()
	if err != nil {
		return header, nil, err
	}
	return header, body, nil
}

// DecodeResponse reads a response header and its body.
func DecodeResponse(data []byte) (ResponseHeader, any, error) {
	r := NewReader(data)
	headerValue, err := r.Deserialize()
	if err != nil {
		return ResponseHeader{}, nil, err
	}
	fields, ok := headerValue.([]any)
	if !ok || len(fields) < 1 {
		return ResponseHeader{}, nil, fmt.Errorf("rpc: response header must be an array")
	}
	header := ResponseHeader{Type: int(toInt(fields[0]))}
	if len(fields) >= 2 {
		header.ID = int(toInt(fields[1]))
	}
	body, err := r.Deserialize()
	if err != nil {
		return header, nil, err
	}
	return header, body, nil
}

// DecodeRequestHeader reads ONLY the request header of a serialized message and stops there.
//
// The gateway needs the channel name (header[2]) to route a frame, and the request id (header[1])
// to answer a later cancel — and nothing else. The body after the header can be a megabyte of
// attachment bytes, so decoding it to make a routing decision would make the gateway the most
// expensive thing in the request path for no benefit. The bytes themselves are never re-encoded
// on the way upstream; only these few scalars are read.
//
// It returns the same RequestHeader DecodeRequest would for the header half, so a caller that needs
// the body must still call DecodeRequest — this function cannot be used to recover it.
func DecodeRequestHeader(data []byte) (RequestHeader, error) {
	headerValue, err := NewReader(data).Deserialize()
	if err != nil {
		return RequestHeader{}, err
	}
	fields, ok := headerValue.([]any)
	if !ok || len(fields) < 2 {
		return RequestHeader{}, fmt.Errorf("rpc: request header must be an array")
	}
	header := RequestHeader{Type: int(toInt(fields[0])), ID: int(toInt(fields[1]))}
	if len(fields) >= 4 {
		header.ChannelName = toString(fields[2])
		header.Name = toString(fields[3])
	}
	return header, nil
}

// DecodeResponseHeader reads ONLY the response header. The Initialize filter in the gateway uses
// it to recognize the 200 it must swallow, without decoding the body behind it.
func DecodeResponseHeader(data []byte) (ResponseHeader, error) {
	headerValue, err := NewReader(data).Deserialize()
	if err != nil {
		return ResponseHeader{}, err
	}
	fields, ok := headerValue.([]any)
	if !ok || len(fields) < 1 {
		return ResponseHeader{}, fmt.Errorf("rpc: response header must be an array")
	}
	header := ResponseHeader{Type: int(toInt(fields[0]))}
	if len(fields) >= 2 {
		header.ID = int(toInt(fields[1]))
	}
	return header, nil
}

// serializeBody writes the body value; `undefined` becomes the Undefined tag, and a nil
// body (no argument) also becomes Undefined to match the TS `arg?: any` default.
func serializeBody(w *Writer, body any) error {
	if body == nil {
		return w.Serialize(Undefined)
	}
	return w.Serialize(body)
}

func toInt(value any) int64 {
	switch v := value.(type) {
	case int32:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return 0
}

func toString(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}
