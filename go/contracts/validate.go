package contracts

import (
	"fmt"
	"math"
	"unicode/utf8"
)

// MaxSafeInteger is JavaScript's Number.MAX_SAFE_INTEGER (2^53 - 1).
const MaxSafeInteger = float64(1<<53 - 1)

// Validate checks a JSON-decoded value against a surface node and returns a
// path-qualified error on the first mismatch, or nil when the value is accepted.
//
// The value must be what `encoding/json` produces: nil, bool, string, float64, or a
// []any / map[string]any. Numbers keep float64 precision exactly as JavaScript does.
func Validate(node *Node, value any) error {
	return validateAt(node, value, "$")
}

func validateAt(node *Node, value any, path string) error {
	if node == nil {
		return nil
	}
	// A refinement runs only after the structural check for the same node succeeds,
	// mirroring zod (which parses first, then applies `.refine`/`.superRefine`).
	if node.Refine != "" {
		if err := validateStructural(node, value, path); err != nil {
			return err
		}
		return applyRefinement(node.Refine, value, path)
	}
	return validateStructural(node, value, path)
}

func validateStructural(node *Node, value any, path string) error {
	switch node.Kind {
	case KindAny:
		return nil
	case KindNever:
		return fail(path, "no value is allowed")
	case KindUnsupported:
		return fail(path, "unsupported schema node %q (generator gap)", node.Unsupported)
	case KindNull:
		if value != nil {
			return fail(path, "expected null")
		}
		return nil
	case KindUndefined:
		// A JSON-decoded value is never undefined.
		return fail(path, "undefined is not representable")
	case KindBoolean:
		if _, ok := value.(bool); !ok {
			return fail(path, "expected boolean")
		}
		return nil
	case KindString:
		return validateString(node, value, path)
	case KindNumber:
		return validateNumber(node, value, path)
	case KindBigint:
		return fail(path, "bigint is not representable in JSON")
	case KindNaN:
		return fail(path, "NaN is not representable in JSON")
	case KindLiteral:
		return validateLiteral(node, value, path)
	case KindEnum:
		return validateEnum(node, value, path)
	case KindArray:
		return validateArray(node, value, path)
	case KindTuple:
		return validateTuple(node, value, path)
	case KindRecord:
		return validateRecord(node, value, path)
	case KindObject:
		return validateObject(node, value, path)
	case KindUnion:
		return validateUnion(node, value, path)
	case KindIntersection:
		if err := validateAt(node.Left, value, path); err != nil {
			return err
		}
		return validateAt(node.Right, value, path)
	case KindOptional:
		// `z.optional()` means "may be absent as an object property", not "may be null".
		// As a standalone value it validates against the inner schema (null is rejected
		// unless the inner schema itself allows null).
		return validateAt(node.Inner, value, path)
	case KindDefault:
		// A default only fills in an absent property; a present value still validates.
		return validateAt(node.Inner, value, path)
	case KindNullable:
		if value == nil {
			return nil
		}
		return validateAt(node.Inner, value, path)
	case KindReadonly:
		return validateAt(node.Inner, value, path)
	case KindNonoptional:
		if value == nil {
			return fail(path, "expected a value")
		}
		return validateAt(node.Inner, value, path)
	case KindName:
		return validateAt(node.Inner, value, path)
	case KindCustom:
		// Refinement (`.refine`/`.superRefine`) that cannot be expressed structurally.
		// The corpus in G0.6 reports any instance where this matters; here we accept so
		// the structural part still runs.
		return nil
	default:
		return fail(path, "unknown node kind %q", node.Kind)
	}
}

func validateString(node *Node, value any, path string) error {
	s, ok := value.(string)
	if !ok {
		return fail(path, "expected string")
	}
	// zod counts Unicode code points, not UTF-16 units or bytes.
	n := int64(utf8.RuneCountInString(s))
	if node.MinLength != nil && n < *node.MinLength {
		return fail(path, "string is shorter than %d characters", *node.MinLength)
	}
	if node.MaxLength != nil && n > *node.MaxLength {
		return fail(path, "string is longer than %d characters", *node.MaxLength)
	}
	if node.Pattern != "" {
		re, err := compileJSRegExp(node.Pattern, node.Flags)
		if err != nil {
			return fail(path, "invalid pattern in schema: %v", err)
		}
		if !re.MatchString(s) {
			return fail(path, "string does not match pattern")
		}
	}
	for _, format := range node.Formats {
		if err := checkFormat(format, s); err != nil {
			return fail(path, "%v", err)
		}
	}
	return nil
}

func validateNumber(node *Node, value any, path string) error {
	num, ok := value.(float64)
	if !ok {
		return fail(path, "expected number")
	}
	if math.IsNaN(num) || math.IsInf(num, 0) {
		return fail(path, "expected a finite number")
	}
	// zod `int()` accepts any finite number whose truncation equals it (1.0 is fine);
	// `safeint` (from `.int()` in zod 4.6) additionally requires |n| <= 2^53-1.
	if node.Int || node.SafeInt {
		if num != math.Trunc(num) {
			return fail(path, "expected an integer")
		}
	}
	if node.SafeInt && (num > MaxSafeInteger || num < -MaxSafeInteger) {
		return fail(path, "number is outside the safe integer range")
	}
	if node.Int && !node.SafeInt && (num > MaxSafeInteger || num < -MaxSafeInteger) {
		// Plain `.int()` without safeint is impossible in zod 4.6 (int implies safeint);
		// keep the check for completeness.
		return fail(path, "expected an integer in range")
	}
	if node.GTE != nil && num < *node.GTE {
		return fail(path, "number is less than %v", *node.GTE)
	}
	if node.GT != nil && num <= *node.GT {
		return fail(path, "number is not greater than %v", *node.GT)
	}
	if node.LTE != nil && num > *node.LTE {
		return fail(path, "number is greater than %v", *node.LTE)
	}
	if node.LT != nil && num >= *node.LT {
		return fail(path, "number is not less than %v", *node.LT)
	}
	if node.MultipleOf != nil && *node.MultipleOf != 0 {
		q := num / *node.MultipleOf
		if math.Abs(q-math.Round(q)) > 1e-9 {
			return fail(path, "number is not a multiple of %v", *node.MultipleOf)
		}
	}
	return nil
}

func validateLiteral(node *Node, value any, path string) error {
	for _, lit := range node.Literals {
		if literalMatches(lit, value) {
			return nil
		}
	}
	return fail(path, "value is not an allowed literal")
}

func validateEnum(node *Node, value any, path string) error {
	switch v := value.(type) {
	case string:
		for _, lit := range node.Literals {
			if lit.Kind == "string" && lit.Str == v {
				return nil
			}
		}
	case float64:
		for _, lit := range node.Literals {
			if lit.Kind == "number" && lit.Num == v {
				return nil
			}
		}
	}
	return fail(path, "value is not one of the allowed options")
}

func validateArray(node *Node, value any, path string) error {
	items, ok := value.([]any)
	if !ok {
		return fail(path, "expected array")
	}
	n := int64(len(items))
	if node.MinItems != nil && n < *node.MinItems {
		return fail(path, "array is shorter than %d items", *node.MinItems)
	}
	if node.MaxItems != nil && n > *node.MaxItems {
		return fail(path, "array is longer than %d items", *node.MaxItems)
	}
	for i, item := range items {
		if err := validateAt(node.Element, item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
			return err
		}
	}
	return nil
}

func validateTuple(node *Node, value any, path string) error {
	items, ok := value.([]any)
	if !ok {
		return fail(path, "expected array")
	}
	if len(items) < len(node.Items) {
		return fail(path, "tuple has fewer than %d items", len(node.Items))
	}
	if node.Rest == nil && len(items) > len(node.Items) {
		return fail(path, "tuple has more than %d items", len(node.Items))
	}
	for i := range node.Items {
		if err := validateAt(node.Items[i], items[i], fmt.Sprintf("%s[%d]", path, i)); err != nil {
			return err
		}
	}
	if node.Rest != nil {
		for i := len(node.Items); i < len(items); i++ {
			if err := validateAt(node.Rest, items[i], fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateRecord(node *Node, value any, path string) error {
	obj, ok := value.(map[string]any)
	if !ok {
		return fail(path, "expected object")
	}
	for key, v := range obj {
		if err := validateAt(node.Value, v, fieldPath(path, key)); err != nil {
			return err
		}
	}
	return nil
}

func validateObject(node *Node, value any, path string) error {
	obj, ok := value.(map[string]any)
	if !ok {
		return fail(path, "expected object")
	}
	known := make(map[string]bool, len(node.Properties))
	for _, prop := range node.Properties {
		known[prop.Key] = true
		field, present := obj[prop.Key]
		if !present {
			if prop.Optional {
				continue
			}
			return fail(fieldPath(path, prop.Key), "required property is missing")
		}
		if err := validateAt(prop.Node, field, fieldPath(path, prop.Key)); err != nil {
			return err
		}
	}
	if node.Strict {
		for key := range obj {
			if !known[key] {
				return fail(fieldPath(path, key), "unknown property")
			}
		}
		return nil
	}
	if node.Catchall != nil {
		for key, v := range obj {
			if known[key] {
				continue
			}
			if err := validateAt(node.Catchall, v, fieldPath(path, key)); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateUnion(node *Node, value any, path string) error {
	matched := 0
	var firstErr error
	if node.Discriminator != "" {
		return validateTaggedUnion(node, value, path)
	}
	for _, option := range node.Options {
		if err := validateAt(option, value, path); err == nil {
			matched++
			if !node.Exclusive {
				return nil
			}
		} else if firstErr == nil {
			firstErr = err
		}
	}
	if matched == 0 {
		if firstErr != nil {
			return firstErr
		}
		return fail(path, "value matches no union member")
	}
	if node.Exclusive && matched > 1 {
		return fail(path, "value matches more than one union member")
	}
	return nil
}

func validateTaggedUnion(node *Node, value any, path string) error {
	obj, ok := value.(map[string]any)
	if !ok {
		return fail(path, "expected object with discriminator")
	}
	tagValue, ok := obj[node.Discriminator]
	if !ok {
		return fail(fieldPath(path, node.Discriminator), "discriminator is missing")
	}
	for _, option := range node.Options {
		tagNode := objectTagNode(option, node.Discriminator)
		if tagNode == nil {
			continue
		}
		if validateAt(tagNode, tagValue, fieldPath(path, node.Discriminator)) == nil {
			return validateAt(option, value, path)
		}
	}
	return fail(fieldPath(path, node.Discriminator), "no variant matches the discriminator")
}

// objectTagNode returns the node under key on an object option (unwrapping any
// optional/name wrapper), or nil when the option is not such an object.
func objectTagNode(option *Node, key string) *Node {
	n := option
	for n != nil && (n.Kind == KindName || n.Kind == KindOptional || n.Kind == KindReadonly) {
		n = n.Inner
	}
	if n == nil || n.Kind != KindObject {
		return nil
	}
	for _, prop := range n.Properties {
		if prop.Key == key {
			return prop.Node
		}
	}
	return nil
}
