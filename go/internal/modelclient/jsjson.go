package modelclient

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
)

// marshalJS serialises a value the way JSON.stringify does for the values that
// can come out of NormalizeToolInput: null, bool, string, json.Number/float64,
// []any and map[string]any. Differences from encoding/json that matter here:
//
//   - no HTML escaping (<, >, & stay literal), matching JSON.stringify;
//   - U+2028/U+2029 stay raw;
//   - map keys are emitted in Go's sorted order, which is NOT JS insertion order.
//
// The last point means this helper is only safe where key order is not compared.
// The per-key order the Node code actually preserves comes from the parsed JSON
// text, so callers that need byte-exact input use the raw Arguments string.
func marshalJS(value any) (string, error) {
	var buf bytes.Buffer
	if err := writeJSValue(&buf, value); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func writeJSValue(buf *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if v {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		writeJSString(buf, v)
	case json.Number:
		buf.WriteString(v.String())
	case float64:
		if math.IsInf(v, 0) || math.IsNaN(v) {
			buf.WriteString("null")
			return nil
		}
		buf.WriteString(strconv.FormatFloat(v, 'g', -1, 64))
	case []any:
		buf.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeJSValue(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		buf.WriteByte('{')
		first := true
		for _, k := range sortedKeys(v) {
			if !first {
				buf.WriteByte(',')
			}
			first = false
			writeJSString(buf, k)
			buf.WriteByte(':')
			if err := writeJSValue(buf, v[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		// fall back to encoding/json with HTML escaping disabled
		enc := json.NewEncoder(buf)
		enc.SetEscapeHTML(false)
		return enc.Encode(value)
	}
	return nil
}

func writeJSString(buf *bytes.Buffer, s string) {
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	var tmp bytes.Buffer
	e := json.NewEncoder(&tmp)
	e.SetEscapeHTML(false)
	_ = enc
	_ = e.Encode(s)
	out := bytes.TrimSuffix(tmp.Bytes(), []byte("\n"))
	out = bytes.ReplaceAll(out, []byte(`\u2028`), []byte("\u2028"))
	out = bytes.ReplaceAll(out, []byte(`\u2029`), []byte("\u2029"))
	buf.Write(out)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}
