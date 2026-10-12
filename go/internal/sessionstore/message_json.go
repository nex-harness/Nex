package sessionstore

import (
	"bytes"
	"encoding/json"
)

// Message is a transcript message. Field order in the encoded object must match
// the Node agent's JSON.stringify output, because message.data is compared
// byte-for-byte. Node spreads the input object (minus id/sessionID) and appends
// a legacy `model` object for user messages last.
//
//	{"role","time","agent","modelSelection","contextSnapshot","semantics","anchor","tools","metadata","model"}
type Message struct {
	ID              string
	SessionID       string
	Role            string
	TimeCreated     int64
	TimeCompleted   *int64
	Agent           string
	ModelSelection  json.RawMessage
	ContextSnapshot json.RawMessage
	Semantics       json.RawMessage
	Anchor          json.RawMessage
	Tools           json.RawMessage
	// LegacyModel is the `model` snapshot Node re-derives from modelSelection for
	// user messages ({"providerID","modelID"[,"variant"]}).
	LegacyModel json.RawMessage
	ParentID    *string
	ModelID     *string
	ProviderID  *string
	Mode        *string
	PlanEnabled *bool
	Path        json.RawMessage
	Cost        *float64
	Tokens      json.RawMessage
	Finish      *string
	Metadata    json.RawMessage
}

// rawObject preserves ordering when marshalling a list of key/value pairs. Go's
// map encoding sorts keys, which would not match Node's insertion order.
type rawObject []rawField

type rawField struct {
	key   string
	value json.RawMessage
}

func (o rawObject) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, f := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, err := json.Marshal(f.key)
		if err != nil {
			return nil, err
		}
		buf.Write(k)
		buf.WriteByte(':')
		if len(f.value) == 0 {
			buf.WriteString("null")
		} else {
			buf.Write(f.value)
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func rawString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

func rawBool(v bool) json.RawMessage {
	if v {
		return json.RawMessage("true")
	}
	return json.RawMessage("false")
}

func rawInt(v int64) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// EncodeMessageData builds the message.data JSON value with Node's key order.
func EncodeMessageData(m Message) ([]byte, error) {
	time := rawObject{{key: "created", value: rawInt(m.TimeCreated)}}
	if m.TimeCompleted != nil {
		time = append(time, rawField{key: "completed", value: rawInt(*m.TimeCompleted)})
	}

	var fields rawObject
	switch m.Role {
	case "user":
		fields = append(fields, rawField{key: "role", value: rawString("user")})
		fields = append(fields, rawField{key: "time", value: mustJSON(time)})
		fields = append(fields, rawField{key: "agent", value: rawString(m.Agent)})
		fields = append(fields, rawField{key: "modelSelection", value: orNull(m.ModelSelection)})
		fields = append(fields, rawField{key: "contextSnapshot", value: orEmptyObject(m.ContextSnapshot)})
		fields = append(fields, rawField{key: "semantics", value: orNull(m.Semantics)})
		fields = append(fields, rawField{key: "anchor", value: orNull(m.Anchor)})
		fields = append(fields, rawField{key: "tools", value: orNull(m.Tools)})
		if len(m.Metadata) > 0 {
			fields = append(fields, rawField{key: "metadata", value: m.Metadata})
		}
		// Legacy `model` is always present for user messages, even when empty.
		fields = append(fields, rawField{key: "model", value: orEmptyObject(m.LegacyModel)})
	case "assistant":
		fields = append(fields, rawField{key: "role", value: rawString("assistant")})
		if m.ParentID != nil {
			fields = append(fields, rawField{key: "parentID", value: rawString(*m.ParentID)})
		}
		if m.ModelID != nil {
			fields = append(fields, rawField{key: "modelId", value: rawString(*m.ModelID)})
		}
		if m.ProviderID != nil {
			fields = append(fields, rawField{key: "providerId", value: rawString(*m.ProviderID)})
		}
		if m.Mode != nil {
			fields = append(fields, rawField{key: "mode", value: rawString(*m.Mode)})
		}
		if m.PlanEnabled != nil {
			fields = append(fields, rawField{key: "planEnabled", value: rawBool(*m.PlanEnabled)})
		}
		fields = append(fields, rawField{key: "agent", value: rawString(m.Agent)})
		if len(m.Path) > 0 {
			fields = append(fields, rawField{key: "path", value: m.Path})
		}
		fields = append(fields, rawField{key: "time", value: mustJSON(time)})
		if m.Cost != nil {
			fields = append(fields, rawField{key: "cost", value: mustJSON(*m.Cost)})
		}
		if len(m.Tokens) > 0 {
			fields = append(fields, rawField{key: "tokens", value: m.Tokens})
		}
		if m.Finish != nil {
			fields = append(fields, rawField{key: "finish", value: rawString(*m.Finish)})
		}
		fields = append(fields, rawField{key: "semantics", value: orNull(m.Semantics)})
		fields = append(fields, rawField{key: "anchor", value: orNull(m.Anchor)})
	default:
		fields = append(fields, rawField{key: "role", value: rawString(m.Role)})
	}
	return fields.MarshalJSON()
}

func orNull(v json.RawMessage) json.RawMessage {
	if len(v) == 0 {
		return json.RawMessage("null")
	}
	return v
}

func orEmptyObject(v json.RawMessage) json.RawMessage {
	if len(v) == 0 {
		return json.RawMessage("{}")
	}
	return v
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
