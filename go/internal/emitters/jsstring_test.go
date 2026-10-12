package emitters

import (
	"testing"

	"github.com/tylinux/nex/go/internal/v4pub"
)

// TestJSLenCountsUTF16CodeUnits proves jsLen matches String.length: an
// astral-plane character counts as two code units.
func TestJSLenCountsUTF16CodeUnits(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"", 0},
		{"abc", 3},
		{"héllo", 5},
		{"😀", 2},            // one astral char = 2 code units
		{"a😀b", 4},          // 1 + 2 + 1
		{"中文", 2},           // BMP chars are 1 unit each
		{"\u2028\u2029", 2}, // line separators are 1 unit
	}
	for _, c := range cases {
		if got := jsLen(c.in); got != c.want {
			t.Errorf("jsLen(%q) = %d, want %d", c.in, got, c.want)
		}
		if got := utf16Len(c.in); got != int(c.want) {
			t.Errorf("utf16Len(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestKeyOrderPreserved proves the ordered JSON builder keeps insertion order,
// which is what makes the frames byte-identical.
func TestKeyOrderPreserved(t *testing.T) {
	o := newObj()
	o.set("b", v4pub.Str("1"))
	o.set("a", v4pub.Str("2"))
	if got, want := string(o.raw()), `{"b":"1","a":"2"}`; got != want {
		t.Fatalf("order mismatch: got %s, want %s", got, want)
	}
}
