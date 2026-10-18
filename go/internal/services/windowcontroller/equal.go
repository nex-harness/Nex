package windowcontroller

import (
	"bytes"
	"encoding/json"
	"reflect"

	"github.com/tylinux/nex/go/internal/adapters/taskindex"
)

// equal.go ports the two equality primitives the delta computation relies on.
// The TS uses isDeepStrictEqual on a whole row; here the row is a struct, so a
// field-wise comparison over the wire-exposed fields is the equivalent, and it
// must agree with what the consumer sees: a change to any serialized field is an
// upsert, an unexported bookkeeping difference (the meta's hasTarget flag) is not.

// metaEqual compares two metas over the fields the wire serializes. It uses
// reflect.DeepEqual on the exported shape, which is exactly the JSON value the
// consumer receives; the meta's private hasTarget bookkeeping is not part of
// that value and so does not affect equality.
func metaEqual(a, b *taskindex.TaskMeta) bool {
	if a == nil || b == nil {
		return a == b
	}
	return reflect.DeepEqual(*a, *b)
}

// jsonEqual compares two optional raw JSON blocks. It is the wire-level
// deep-equality: absent and null are treated as DIFFERENT here, because the
// producer distinguishes them (an absent pendingInteraction vs an explicit null
// are different row values), and byte equality is a sound (if conservative)
// stand-in for structural equality on already-normalized JSON.
func jsonEqual(a, b json.RawMessage) bool {
	return bytes.Equal(a, b)
}
