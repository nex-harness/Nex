package rpcwire

import (
	"bytes"
	"testing"
)

// A hostile tag-4 array claiming 2^31 elements must be rejected without
// pre-allocating ~16 GiB. The decoder only trusts the count after comparing it
// with the remaining bytes, and grows the slice instead of reserving it.
func TestDecodeArrayDoesNotReserveDeclaredLength(t *testing.T) {
	var payload bytes.Buffer
	payload.WriteByte(tagArray)
	payload.Write(appendVQL(nil, 0x7fffffff))
	// No elements follow: the count check must reject before allocating.
	if _, err := DecodeValue(payload.Bytes()); err == nil {
		t.Fatal("oversized array length was accepted")
	}
}

// A valid large array still decodes, proving the growth cap is not a limit on
// element count.
func TestDecodeLargeArray(t *testing.T) {
	const count = 5000
	var payload bytes.Buffer
	payload.WriteByte(tagArray)
	payload.Write(appendVQL(nil, count))
	for i := 0; i < count; i++ {
		payload.WriteByte(tagInt)
		payload.WriteByte(0)
	}
	value, err := DecodeValue(payload.Bytes())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	array, ok := value.([]any)
	if !ok || len(array) != count {
		t.Fatalf("value = %T of len %d, want %d elements", value, len(array), count)
	}
}

// A frame full of cheap tag-0 values must not decode into unbounded interface
// slots; the node budget rejects it.
func TestDecodeNodeBudget(t *testing.T) {
	var payload bytes.Buffer
	payload.WriteByte(tagArray)
	payload.Write(appendVQL(nil, maxValueNodes+1))
	for i := 0; i <= maxValueNodes; i++ {
		payload.WriteByte(tagUndefined)
	}
	if _, err := DecodeValue(payload.Bytes()); err == nil {
		t.Fatal("frame above the node budget was accepted")
	}
}
