package protocol

import (
	"bytes"
	"testing"
)

func TestBinaryPTYFrameRoundTrip(t *testing.T) {
	id := "b9777673-535b-4d67-a726-b390c39ed46d"
	payload := []byte("\x1b[32mhello ghostty\x1b[0m\r\n")

	raw := EncodePTYBinaryFrame(PTYBinaryStdout, id, payload)
	op, gotID, gotData, err := DecodePTYBinaryFrame(raw)
	if err != nil {
		t.Fatalf("DecodePTYBinaryFrame: %v", err)
	}
	if op != PTYBinaryStdout || gotID != id || !bytes.Equal(gotData, payload) {
		t.Fatalf("mismatch: op=%x id=%q data=%q", op, gotID, string(gotData))
	}

	if _, _, _, err := DecodePTYBinaryFrame([]byte{0x01, 0x02}); err == nil {
		t.Fatalf("expected short frame error")
	}
}

func TestBinaryBrowserScreencastRoundTrip(t *testing.T) {
	tabID := "tab_0192837465"
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10}

	raw := EncodeBrowserBinaryFrame(tabID, jpeg)
	op, gotID, gotData, err := DecodePTYBinaryFrame(raw)
	if err != nil {
		t.Fatalf("DecodePTYBinaryFrame: %v", err)
	}
	if op != BrowserBinaryScreencast || gotID != tabID || !bytes.Equal(gotData, jpeg) {
		t.Fatalf("browser frame mismatch: op=%x id=%q len=%d", op, gotID, len(gotData))
	}
}
