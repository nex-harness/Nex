// Package contracts holds the generated description of the protocol surface the Go
// agent speaks, plus a validator that accepts exactly what the TypeScript zod schemas
// accept. The description is emitted by scripts/go-rewrite/gen-contracts.mts (step 0.5)
// as a data tree; the interpreter that walks it lives here so it can be reviewed and
// tested by hand.
//
// The generated files (surface.gen.go, names.gen.go, methods.gen.go) are the only
// mechanical part; everything else in this package is written and tested normally.
package contracts

// Kind enumerates the node kinds the emitter can produce. It mirrors Node in
// scripts/go-rewrite/zod-walk.mts. A kind the interpreter does not know is a bug in the
// generator, not a runtime condition: `Validate` reports it as a hard error.
type Kind string

const (
	KindAny          Kind = "any"
	KindNever        Kind = "never"
	KindNull         Kind = "null"
	KindUndefined    Kind = "undefined"
	KindBoolean      Kind = "boolean"
	KindString       Kind = "string"
	KindNumber       Kind = "number"
	KindBigint       Kind = "bigint"
	KindNaN          Kind = "nan"
	KindLiteral      Kind = "literal"
	KindEnum         Kind = "enum"
	KindArray        Kind = "array"
	KindTuple        Kind = "tuple"
	KindRecord       Kind = "record"
	KindObject       Kind = "object"
	KindUnion        Kind = "union"
	KindIntersection Kind = "intersection"
	KindOptional     Kind = "optional"
	KindNullable     Kind = "nullable"
	KindDefault      Kind = "default"
	KindReadonly     Kind = "readonly"
	KindNonoptional  Kind = "nonoptional"
	KindName         Kind = "name"
	KindCustom       Kind = "custom"
	KindUnsupported  Kind = "unsupported"
)

// Prop is one object property: its key, its value node, and whether it may be absent.
type Prop struct {
	Key      string
	Node     *Node
	Optional bool
}

// Node is one node of the compiled schema tree. Fields are only meaningful for the
// kinds noted in their comments; unused fields stay nil.
type Node struct {
	Kind Kind

	// string
	MinLength *int64
	MaxLength *int64
	Pattern   string // JS regexp source, or "" when absent
	Flags     string // JS regexp flags
	Formats   []string

	// number / bigint
	Int        bool
	SafeInt    bool
	GTE        *float64
	GT         *float64
	LTE        *float64
	LT         *float64
	MultipleOf *float64

	// literal / enum
	Literals []Literal

	// array / tuple
	Element  *Node
	Items    []*Node
	Rest     *Node
	MinItems *int64
	MaxItems *int64

	// record
	Value *Node

	// object
	Properties []Prop
	Catchall   *Node // nil unless a non-unknown catchall is present
	Strict     bool

	// union
	Options       []*Node
	Exclusive     bool
	Discriminator string

	// intersection
	Left  *Node
	Right *Node

	// wrappers
	Inner *Node

	// name reference (the emitter may inline Inner; Name is for diagnostics only)
	Name string

	// Refine names a hand-written refinement applied after the structural check.
	// Set on a root node whose zod schema carries `.refine`/`.superRefine` checks that
	// JSON Schema cannot express. See refinements.go.
	Refine string

	// unsupported
	Unsupported string
}

// Literal is a JSON scalar a `literal` node allows.
type Literal struct {
	Kind string // "string" | "number" | "boolean" | "null"
	Str  string
	Num  float64
	Bool bool
}

// Surface is the whole exported protocol surface: every schema by id, or by its
// exported constant name.
type Surface struct {
	ByID   map[string]*Node
	ByName map[string]*Node
}

// Lookup returns the node for a schema id or exported name, in that order.
func (s *Surface) Lookup(key string) (*Node, bool) {
	if n, ok := s.ByID[key]; ok {
		return n, true
	}
	if n, ok := s.ByName[key]; ok {
		return n, true
	}
	return nil, false
}
