package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type wireVectors struct {
	Limits struct {
		MaxFrameBytes                    int `json:"maxFrameBytes"`
		LogicalFrameAssemblyMaxBytes     int `json:"logicalFrameAssemblyMaxBytes"`
		LogicalFrameAssemblyMaxFragments int `json:"logicalFrameAssemblyMaxFragments"`
		TransportEnvelopeIDMaxChars      int `json:"transportEnvelopeIdMaxChars"`
	} `json:"limits"`
	CRCBase64 []struct {
		Hex    string `json:"hex"`
		CRC32  string `json:"crc32"`
		Base64 string `json:"base64"`
	} `json:"crcBase64"`
	BadBase64 []struct {
		Input   string  `json:"input"`
		Decoded *string `json:"decoded"`
	} `json:"badBase64"`
	EncodeCases []struct {
		Name                  string  `json:"name"`
		LogicalFrameID        string  `json:"logicalFrameId"`
		LogicalFrameOrdinal   int64   `json:"logicalFrameOrdinal"`
		MaxPhysicalFrameBytes *int    `json:"maxPhysicalFrameBytes"`
		FrameJSON             *string `json:"frameJson"`
		Gen                   *struct {
			RepeatBytes int    `json:"repeatBytes"`
			Text        string `json:"text"`
		} `json:"gen"`
		Error      *string  `json:"error"`
		Wires      []string `json:"wires"`
		WireSHA256 []string `json:"wireSha256"`
		WireLens   []int    `json:"wireLengths"`
		Measured   []int    `json:"measured"`
	} `json:"encodeCases"`
	AssemblyCases []assemblyCase `json:"assemblyCases"`
}

func loadWireVectors(t testing.TB) *wireVectors {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/vectors/wire.json")
	if err != nil {
		t.Fatal(err)
	}
	var v wireVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return &v
}

func TestLimitsMatchTypeScript(t *testing.T) {
	v := loadWireVectors(t)
	if v.Limits.MaxFrameBytes != MaxFrameBytes ||
		v.Limits.LogicalFrameAssemblyMaxBytes != LogicalFrameAssemblyMaxBytes ||
		v.Limits.LogicalFrameAssemblyMaxFragments != LogicalFrameAssemblyMaxFragments ||
		v.Limits.TransportEnvelopeIDMaxChars != TransportEnvelopeIDMaxChars {
		t.Fatalf("limits drifted: ts=%+v", v.Limits)
	}
}

func TestCRC32AndBase64AgreeWithTS(t *testing.T) {
	v := loadWireVectors(t)
	for _, c := range v.CRCBase64 {
		b, _ := hex.DecodeString(c.Hex)
		if got := CRC32Hex(b); got != c.CRC32 {
			t.Errorf("crc32(%s) = %s, want %s", c.Hex, got, c.CRC32)
		}
		if len(b) > 0 && EncodeBase64(b) != c.Base64 {
			t.Errorf("base64(%s) = %s, want %s", c.Hex, EncodeBase64(b), c.Base64)
		}
	}
	// the classic check value for CRC-32/IEEE
	if got := CRC32Hex([]byte("123456789")); got != "cbf43926" {
		t.Errorf("crc32 check value: %s", got)
	}
	for _, c := range v.BadBase64 {
		got, ok := DecodeBase64(c.Input)
		if (c.Decoded != nil) != ok {
			t.Errorf("decode(%q): ok=%v, ts accepts=%v", c.Input, ok, c.Decoded != nil)
			continue
		}
		if ok && hex.EncodeToString(got) != *c.Decoded {
			t.Errorf("decode(%q) = %x, want %s", c.Input, got, *c.Decoded)
		}
	}
}

func TestEncoderAgreesWithTypeScript(t *testing.T) {
	v := loadWireVectors(t)
	for _, c := range v.EncodeCases {
		var frame []byte
		switch {
		case c.FrameJSON != nil:
			frame = []byte(*c.FrameJSON)
		case c.Gen != nil:
			frame = []byte(`{"topic":"conversation/s1","subscriptionId":"sub-1","seq":1,"data":"` +
				strings.Repeat(c.Gen.Text, c.Gen.RepeatBytes) + `"}`)
		default:
			t.Fatalf("%s: no frame", c.Name)
		}
		opts := EncodeOptions{
			DeliveryKind: DeliveryOnline, Topic: "conversation/s1", SubscriptionID: "sub-1",
			LogicalFrameID: c.LogicalFrameID, LogicalFrameOrdinal: c.LogicalFrameOrdinal,
		}
		if c.MaxPhysicalFrameBytes != nil {
			opts.MaxPhysicalFrameBytes = *c.MaxPhysicalFrameBytes
		}
		wires, err := EncodeTopicWireFrames(frame, opts)
		if c.Error != nil {
			ee, ok := err.(*EncodeError)
			if !ok || ee.Reason != *c.Error {
				t.Errorf("%s: want error %q, got %v", c.Name, *c.Error, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		if len(wires) != len(c.WireSHA256) {
			t.Errorf("%s: %d wires, ts produced %d", c.Name, len(wires), len(c.WireSHA256))
			continue
		}
		for i, w := range wires {
			sum := sha256.Sum256(w.JSON)
			if len(w.JSON) != c.WireLens[i] || hex.EncodeToString(sum[:]) != c.WireSHA256[i] {
				if c.Wires != nil {
					t.Errorf("%s[%d]:\n go: %s\n ts: %s", c.Name, i, w.JSON, c.Wires[i])
				} else {
					t.Errorf("%s[%d]: length %d (ts %d) or sha256 differs", c.Name, i, len(w.JSON), c.WireLens[i])
				}
			}
			if got := MeasureEnvelope(len(w.JSON)).MaxBytes; got != c.Measured[i] {
				t.Errorf("%s[%d]: measured %d, ts %d", c.Name, i, got, c.Measured[i])
			}
		}
	}
}

func TestEncoderRejectsBadLimits(t *testing.T) {
	_, err := EncodeTopicWireFrames([]byte(`{}`), EncodeOptions{MaxPhysicalFrameBytes: -1})
	if ee, ok := err.(*EncodeError); !ok || ee.Reason != "proto.invalidLimit.maxPhysicalFrameBytes" {
		t.Errorf("got %v", err)
	}
	_, err = EncodeTopicWireFrames([]byte(`{}`), EncodeOptions{MaxAssemblyBytes: -5})
	if ee, ok := err.(*EncodeError); !ok || ee.Reason != "proto.invalidLimit.maxAssemblyBytes" {
		t.Errorf("got %v", err)
	}
	// A frame bigger than the assembly cap is refused before anything is built.
	_, err = EncodeTopicWireFrames([]byte(strings.Repeat("a", 200)), EncodeOptions{MaxAssemblyBytes: 100})
	if ee, ok := err.(*EncodeError); !ok || ee.Reason != ReasonFrameAssemblyTooLarge {
		t.Errorf("got %v", err)
	}
}

func TestAppendJSStringMatchesJSONStringify(t *testing.T) {
	// Expected values are what JSON.stringify produces for these strings.
	cases := map[string]string{
		"plain":            `"plain"`,
		"a\"b\\c":          `"a\"b\\c"`,
		"\b\f\n\r\t":       `"\b\f\n\r\t"`,
		"\x00\x01\x1f\x7f": "\"\\u0000\\u0001\\u001f\x7f\"",
		"\u2028\u2029":     "\"\u2028\u2029\"",
		"<>&":              `"<>&"`,
		"中😀é":              `"中😀é"`,
	}
	for in, want := range cases {
		if got := string(appendJSString(nil, in)); got != want {
			t.Errorf("%q: got %s want %s", in, got, want)
		}
	}
}
