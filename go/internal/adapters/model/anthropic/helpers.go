package anthropic

// Pure helpers pulled out of decoder.go so the decoder stays under the
// 400-line archcheck limit. Everything here is a one-way translation from a
// provider value to a modelclient value with no stream state.

import (
	"encoding/json"

	"github.com/tylinux/nex/go/internal/modelclient"
)

func findToolInputEnd(events []modelclient.Event, id string) (modelclient.Event, bool) {
	for _, ev := range events {
		if ev.Kind == modelclient.EventToolInputEnd && ev.ID == id {
			return ev, true
		}
	}
	return modelclient.Event{}, false
}

func (d *Decoder) sortedBlockIndexes() []int64 {
	idx := make([]int64, 0, len(d.blocks))
	for k := range d.blocks {
		idx = append(idx, k)
	}
	for i := 1; i < len(idx); i++ {
		for j := i; j > 0 && idx[j-1] > idx[j]; j-- {
			idx[j-1], idx[j] = idx[j], idx[j-1]
		}
	}
	return idx
}

func textBlockID(index int64) string      { return "txt-" + itoa(index) }
func reasoningBlockID(index int64) string { return "reasoning-" + itoa(index) }

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// providerError builds a stream error from an Anthropic `error` event body. The
// SDK's apierror.Error models HTTP-level failures; an in-band error arrives inside
// a 200 response, so it is surfaced as modelclient's StreamError with the raw body
// kept for the business-code classifier.
func (d *Decoder) providerError(body string) modelclient.Event {
	var payload struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal([]byte(body), &payload)
	message := payload.Error.Message
	if message == "" {
		message = body
	}
	return modelclient.Event{
		Kind: modelclient.EventError,
		Err:  &modelclient.StreamError{Message: message, Raw: json.RawMessage(body)},
	}
}

// parseError turns an undecodable event body into an invalid-response error event.
func (d *Decoder) parseError(body string, cause error) modelclient.Event {
	d.errored = true
	return modelclient.Event{
		Kind: modelclient.EventError,
		Err: &modelclient.DecodeError{
			Kind:    modelclient.ErrInvalidResponse,
			Message: "JSON parsing failed: " + cause.Error(),
			Cause:   cause,
		},
	}
}
