package rpc

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"testing"
)

// vector mirrors one entry of go/testdata/rpc-vectors.json, produced by the REAL
// TypeScript serialize()/deserialize() (scripts/go-rewrite/gen-rpc-vectors.mts).
type vector struct {
	Name    string          `json:"name"`
	Kind    string          `json:"kind"`
	Value   json.RawMessage `json:"value"`
	Hex     string          `json:"hex"`
	Decoded json.RawMessage `json:"decoded"`
	Bytes   []int           `json:"bytes"`
}

type vectorFile struct {
	Vectors []vector `json:"vectors"`
}

func loadVectors(t *testing.T) []vector {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/rpc-vectors.json")
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var file vectorFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("decode vectors: %v", err)
	}
	if len(file.Vectors) < 30 {
		t.Fatalf("vector file looks truncated: %d", len(file.Vectors))
	}
	return file.Vectors
}

// goValueFor converts a vector's JSON-described input into the Go shape the codec takes.
func goValueFor(t *testing.T, v vector) any {
	t.Helper()
	switch v.Kind {
	case "undefined":
		return Undefined
	case "binary":
		data := make([]byte, len(v.Bytes))
		for i, b := range v.Bytes {
			data[i] = byte(b)
		}
		return data
	default:
		// The input JSON text preserves object key order; parse it into ordered Objects so
		// the Go writer can reproduce the exact same bytes as JSON.stringify.
		return parseOrderedInput(t, v.Value)
	}
}

// parseOrderedInput reads the vector's *input* JSON into codec value shapes. Numbers in
// int32 range become int32 so the writer uses the Int tag, matching JS `(data|0)===data`.
func parseOrderedInput(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	value, err := decodeOrdered(bytes.NewReader(raw), true)
	if err != nil {
		t.Fatalf("parse input %s: %v", raw, err)
	}
	return value
}

// parseOrderedDecoded reads the vector's *decoded* JSON into the shapes the Go reader
// produces: Int-tag values are int32 (handled by normalizeDecodedScalar on the reader
// side), JSON numbers are float64. Object order is preserved.
func parseOrderedDecoded(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	value, err := decodeOrdered(bytes.NewReader(raw), false)
	if err != nil {
		t.Fatalf("parse decoded %s: %v", raw, err)
	}
	return value
}

func decodeOrdered(reader *bytes.Reader, inputMode bool) (any, error) {
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	return decodeOrderedValue(decoder, inputMode)
}

func decodeOrderedValue(decoder *json.Decoder, inputMode bool) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return normalizeTestScalar(token, inputMode), nil
	}
	switch delim {
	case '{':
		obj := Object{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			value, err := decodeOrderedValue(decoder, inputMode)
			if err != nil {
				return nil, err
			}
			obj.Keys = append(obj.Keys, keyToken.(string))
			obj.Values = append(obj.Values, value)
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		if data, ok := markerFromOrdered(obj, inputMode); ok {
			return data, nil
		}
		return obj, nil
	case '[':
		out := []any{}
		for decoder.More() {
			value, err := decodeOrderedValue(decoder, inputMode)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return out, nil
	}
	return nil, nil
}

func normalizeTestScalar(token any, inputMode bool) any {
	switch v := token.(type) {
	case json.Number:
		if inputMode {
			if i, err := v.Int64(); err == nil && i >= -2147483648 && i <= 2147483647 {
				return int32(i)
			}
			f, _ := v.Float64()
			return f
		}
		f, _ := v.Float64()
		return f
	default:
		return token
	}
}

// markerFromOrdered turns the test-side markers into their real values:
// {__bytes:[...]} -> []byte, {__undefined:true} -> Undefined sentinel.
func markerFromOrdered(obj Object, inputMode bool) (any, bool) {
	if obj.Len() == 1 {
		if _, ok := obj.Get("__undefined"); ok {
			if inputMode {
				return Undefined, true
			}
			// The TS decoder returns undefined for the Undefined tag; the Go reader returns
			// nil, so compare against nil.
			return nil, true
		}
		if _, ok := obj.Get("__nan"); ok {
			return math.NaN(), true
		}
		if raw, ok := obj.Get("__inf"); ok {
			if s, ok := raw.(string); ok && s == "-Infinity" {
				return math.Inf(-1), true
			}
			return math.Inf(1), true
		}
		if raw, ok := obj.Get("__bytes"); ok {
			list, ok := raw.([]any)
			if !ok {
				return nil, false
			}
			out := make([]byte, len(list))
			for i, b := range list {
				out[i] = byte(int32Value(b))
			}
			return out, true
		}
	}
	return nil, false
}

func int32Value(value any) int64 {
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

// TestSerializeMatchesTypeScript checks that the Go writer produces byte-identical output
// to the real TypeScript serialize() for every recorded vector.
func TestSerializeMatchesTypeScript(t *testing.T) {
	for _, v := range loadVectors(t) {
		w := NewWriter()
		if err := w.Serialize(goValueFor(t, v)); err != nil {
			t.Errorf("%s: serialize: %v", v.Name, err)
			continue
		}
		got := hex.EncodeToString(w.Bytes())
		if got != v.Hex {
			t.Errorf("%s: hex mismatch\n got %s\nwant %s", v.Name, got, v.Hex)
		}
	}
}

// TestDeserializeMatchesTypeScript checks that the Go reader reproduces exactly what the
// TypeScript deserialize() returned for each vector's bytes.
func TestDeserializeMatchesTypeScript(t *testing.T) {
	for _, v := range loadVectors(t) {
		raw, err := hex.DecodeString(v.Hex)
		if err != nil {
			t.Fatalf("%s: bad hex in vector: %v", v.Name, err)
		}
		got, err := NewReader(raw).Deserialize()
		if err != nil {
			t.Errorf("%s: deserialize: %v", v.Name, err)
			continue
		}
		want := parseOrderedDecoded(t, v.Decoded)
		if !valuesMatch(got, want) {
			t.Errorf("%s: decoded value mismatch\n got %#v\nwant %#v", v.Name, got, want)
		}
	}
}

// valuesMatch compares decoded values. Numbers are compared numerically (the TS decoder
// yields one JS number type; Go distinguishes int32 for the Int tag from float64 for JSON
// numbers, and both must be accepted). Objects/slices compare structurally with order.
func valuesMatch(a, b any) bool {
	if an, aok := numericValue(a); aok {
		bn, bok := numericValue(b)
		return bok && an == bn
	}
	switch av := a.(type) {
	case []byte:
		bv, ok := b.([]byte)
		return ok && bytes.Equal(av, bv)
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !valuesMatch(av[i], bv[i]) {
				return false
			}
		}
		return true
	case Object:
		bv, ok := b.(Object)
		if !ok || av.Len() != bv.Len() {
			return false
		}
		for i := range av.Keys {
			if av.Keys[i] != bv.Keys[i] {
				return false
			}
			if !valuesMatch(av.Values[i], bv.Values[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(a, b)
	}
}

func numericValue(value any) (float64, bool) {
	switch v := value.(type) {
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case float64:
		return v, true
	}
	return 0, false
}
