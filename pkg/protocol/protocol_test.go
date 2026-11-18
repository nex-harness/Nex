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
