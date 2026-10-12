package sessionstore

import "encoding/json"

// Part is a transcript part. Only the fields needed to encode the data JSON in
// Node's key order are modelled explicitly; the rest is carried in Data.
//
// The type union Node writes for a text turn:
//
//	{"type":"text","text":...,"time":{"start","end"}}
//	{"type":"step-start"}
//	{"type":"step-finish","reason","cost","tokens"}
//	{"type":"tool","callID","declarationIndex","tool","state",...}
type Part struct {
	ID        string
	MessageID string
	SessionID string
	Type      string

	Text string
	// TextTime carries time.start / time.end for text/reasoning/compaction.
	TimeStart *int64
	TimeEnd   *int64

	// step-finish
	Reason json.RawMessage
	Cost   *float64
	Tokens json.RawMessage

	// tool parts
	CallID           *string
	DeclarationIndex *int
	Tool             *string
	State            json.RawMessage

	// Sequence, when non-nil, is an explicit sequence to write. Otherwise the
	// adapter allocates max(sequence)+1 within the message.
	Sequence *int64

	// TimeCreated is the value stored in part.time_created. When nil the adapter
	// computes partCreatedAt from the data (see PartCreatedAt).
	TimeCreated *int64

	// RawData is stored verbatim for part types the encoder does not model.
	RawData []byte
}

// PartCreatedAt mirrors partCreatedAt in Node codecs.ts: text/reasoning/
// compaction use time.start; tool uses state.time.start; retry uses
// time.created; anything else falls back to `fallback` (Date.now()).
func PartCreatedAt(p Part, fallback int64) int64 {
	switch p.Type {
	case "text", "reasoning", "compaction", "timeline":
		if p.TimeStart != nil {
			return *p.TimeStart
		}
		return fallback
	case "tool":
		if start := toolStateStart(p.State); start != nil {
			return *start
		}
		return fallback
	case "retry":
		if p.TimeStart != nil {
			return *p.TimeStart
		}
		return fallback
	default:
		return fallback
	}
}

func toolStateStart(state json.RawMessage) *int64 {
	if len(state) == 0 {
		return nil
	}
	var decoded struct {
		Time struct {
			Start *int64 `json:"start"`
		} `json:"time"`
	}
	if err := json.Unmarshal(state, &decoded); err != nil {
		return nil
	}
	return decoded.Time.Start
}

// EncodePartData builds part.data with Node's key order for the supported
// part types. Unknown types return their raw Data unchanged when set.
func EncodePartData(p Part) ([]byte, error) {
	switch p.Type {
	case "text":
		fields := rawObject{{key: "type", value: rawString("text")}, {key: "text", value: rawString(p.Text)}}
		if p.TimeStart != nil {
			time := rawObject{{key: "start", value: rawInt(*p.TimeStart)}}
			if p.TimeEnd != nil {
				time = append(time, rawField{key: "end", value: rawInt(*p.TimeEnd)})
			}
			fields = append(fields, rawField{key: "time", value: mustJSON(time)})
		}
		return fields.MarshalJSON()
	case "step-start":
		return []byte(`{"type":"step-start"}`), nil
	case "step-finish":
		fields := rawObject{{key: "type", value: rawString("step-finish")}}
		if len(p.Reason) > 0 {
			fields = append(fields, rawField{key: "reason", value: p.Reason})
		}
		if p.Cost != nil {
			fields = append(fields, rawField{key: "cost", value: mustJSON(*p.Cost)})
		}
		if len(p.Tokens) > 0 {
			fields = append(fields, rawField{key: "tokens", value: p.Tokens})
		}
		return fields.MarshalJSON()
	case "tool":
		fields := rawObject{{key: "type", value: rawString("tool")}}
		if p.CallID != nil {
			fields = append(fields, rawField{key: "callID", value: rawString(*p.CallID)})
		}
		if p.DeclarationIndex != nil {
			fields = append(fields, rawField{key: "declarationIndex", value: rawInt(int64(*p.DeclarationIndex))})
		}
		if p.Tool != nil {
			fields = append(fields, rawField{key: "tool", value: rawString(*p.Tool)})
		}
		if len(p.State) > 0 {
			fields = append(fields, rawField{key: "state", value: p.State})
		}
		return fields.MarshalJSON()
	default:
		return nil, nil
	}
}
