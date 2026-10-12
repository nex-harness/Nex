package tools

import (
	"encoding/json"
	"testing"
)

// The runtime decodes tool input with json.Decoder.UseNumber, so numeric fields
// arrive as json.Number. A Read with offset/limit used to fail with
// "offset must be an integer" for exactly that reason (found by replaying a
// Read with offset and limit against the Node agent).
func TestOptIntFieldAcceptsJSONNumber(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  int
		bad   bool
	}{
		{"json.Number int", json.Number("3"), 3, false},
		{"json.Number integral float text", json.Number("3.0"), 3, false},
		{"json.Number exponent", json.Number("1e2"), 100, false},
		{"float64", float64(7), 7, false},
		{"int", 9, 9, false},
		{"json.Number fractional", json.Number("2.5"), 0, true},
		{"string", "3", 0, true},
	}
	for _, c := range cases {
		got, err := optIntField(map[string]any{"offset": c.value}, "offset")
		if c.bad {
			if err == nil {
				t.Errorf("%s: want error, got %v", c.name, *got)
			}
			continue
		}
		if err != nil || got == nil || *got != c.want {
			t.Errorf("%s: got %v, %v want %d", c.name, got, err, c.want)
		}
	}
}
