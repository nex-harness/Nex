package modelclient

import "encoding/json"

func (d *Decoder) consumeToolCallDeltas(deltas []decoderToolDelta, out []Event) ([]Event, error) {
	for _, tcd := range deltas {
		index := len(d.calls)
		if tcd.Index != nil {
			index = *tcd.Index
		}
		if index > d.maxIndex {
			d.maxIndex = index
		}
		if d.calls[index] == nil {
			if tcd.Index != nil {
				p := d.pending[index]
				if p == nil {
					p = &decPending{}
					d.pending[index] = p
				}
				if tcd.ID != nil {
					if !p.hasID || p.id == "" {
						p.id = *tcd.ID
						p.hasID = true
					}
				}
				if sig := thoughtSignature(tcd); sig != nil && !p.hasSig {
					p.signature = *sig
					p.hasSig = true
				}
				if tcd.Function != nil && tcd.Function.Arguments != nil {
					p.arguments += *tcd.Function.Arguments
				}
				var name *string
				if tcd.Function != nil {
					name = tcd.Function.Name
				}
				if name == nil {
					continue
				}
				delete(d.pending, index)
				if !p.hasID || p.id == "" {
					return out, &DecodeError{
						Kind:    ErrInvalidResponse,
						Message: "Expected 'id' to be a string.",
						Data:    json.RawMessage(mustJSON(tcd)),
					}
				}
				out = append(out, Event{Kind: EventToolInputStart, ID: p.id, ToolName: *name})
				d.calls[index] = &decToolCall{
					id:        p.id,
					name:      *name,
					arguments: p.arguments,
					signature: p.signature,
					hasSig:    p.hasSig,
				}
			} else {
				if tcd.ID == nil {
					return out, &DecodeError{
						Kind:    ErrInvalidResponse,
						Message: "Expected 'id' to be a string.",
						Data:    json.RawMessage(mustJSON(tcd)),
					}
				}
				var name *string
				if tcd.Function != nil {
					name = tcd.Function.Name
				}
				if name == nil {
					return out, &DecodeError{
						Kind:    ErrInvalidResponse,
						Message: "Expected 'function.name' to be a string.",
						Data:    json.RawMessage(mustJSON(tcd)),
					}
				}
				out = append(out, Event{Kind: EventToolInputStart, ID: *tcd.ID, ToolName: *name})
				args := ""
				if tcd.Function != nil && tcd.Function.Arguments != nil {
					args = *tcd.Function.Arguments
				}
				d.calls[index] = &decToolCall{
					id:        *tcd.ID,
					name:      *name,
					arguments: args,
					signature: stringOr(thoughtSignature(tcd)),
					hasSig:    thoughtSignature(tcd) != nil,
				}
			}
			tc := d.calls[index]
			if tc != nil && tc.name != "" && len(tc.arguments) > 0 {
				out = append(out, Event{Kind: EventToolInputDelta, ID: tc.id, Text: tc.arguments})
			}
			continue
		}

		tc := d.calls[index]
		if tc.hasFinished {
			continue
		}
		if tcd.Function != nil && tcd.Function.Arguments != nil {
			tc.arguments += *tcd.Function.Arguments
		}
		delta := ""
		if tcd.Function != nil && tcd.Function.Arguments != nil {
			delta = *tcd.Function.Arguments
		}
		out = append(out, Event{Kind: EventToolInputDelta, ID: tc.id, Text: delta})
	}
	return out, nil
}
