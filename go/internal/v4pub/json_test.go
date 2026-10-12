package v4pub

import "testing"

// TestJSONNumberFormattingMatchesECMAScript pins appendJSFloat/appendJSInt to the
// ECMAScript Number::toString algorithm. The values were checked against
// `node -e "console.log(JSON.stringify(x))"`. This is the one place the encoder
// could silently drift, so it is exercised with the boundaries: the exponent
// thresholds at 1e21 / 1e-7 and the integer path below 2^53.
func TestJSONNumberFormattingMatchesECMAScript(t *testing.T) {
	cases := []struct {
		in   JSONValue
		want string
	}{
		{Int(0), "0"},
		{Int(2), "2"},
		{Int(9007199254740991), "9007199254740991"},
		{Int(-7), "-7"},
		{Float(1234.5), "1234.5"},
		{Float(0.1), "0.1"},
		{Float(0.0001), "0.0001"},
		{Float(1e-7), "1e-7"},
		{Float(1e-6), "0.000001"},
		{Float(1e20), "100000000000000000000"},
		{Float(1e21), "1e+21"},
		{Float(1.5e-10), "1.5e-10"},
		{Float(1.7976931348623157e308), "1.7976931348623157e+308"},
		{Float(12345678901234567890.0), "12345678901234567000"},
		{Float(1e-323), "1e-323"},
	}
	for _, c := range cases {
		got, err := MarshalJSONString(c.in)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("number %v: go=%s want=%s", c.in, got, c.want)
		}
	}
}

// TestJSONStringEscapingMatchesECMAScript pins appendJSString: quote, backslash,
// the named control escapes, \u00xx for the remaining control characters, and
// pass-through of <, >, &, U+2028 and U+2029 (Go's default HTML escaping is
// wrong here).
func TestJSONStringEscapingMatchesECMAScript(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", `""`},
		{"a\"b", `"a\"b"`},
		{"a\\b", `"a\\b"`},
		{"a\nb\tc\rd\be\ff", `"a\nb\tc\rd\be\ff"`},
		{"\u0001\u001f", `"\u0001\u001f"`},
		{"<b>&amp;</b>", `"<b>&amp;</b>"`},
		{">", `">"`},
		{"x\u2028y\u2029z", "\"x\u2028y\u2029z\""},
		{"中文😀", `"中文😀"`},
	}
	for _, c := range cases {
		got, err := MarshalJSONString(Str(c.in))
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("string %q: go=%s want=%s", c.in, got, c.want)
		}
	}
}
