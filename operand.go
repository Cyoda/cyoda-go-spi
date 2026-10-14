package spi

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// ValidateOperand reports whether value is an operand the leaf operator op
// can use. It is the one rule for operand shape: [ConditionToFilter] applies
// it to every leaf it translates and [Prepare] to every leaf it prepares, so
// no translator or evaluator in this module sees an operand it refuses. A
// caller that validates conditions at its own boundary calls it to refuse the
// same set, and maps the sentinels to its own wording.
//
// It accepts a closed set and refuses everything else. A scalar operand has
// one of these types: string, bool, json.Number, int, int8, int16, int32,
// int64, uint, uint8, uint16, uint32, uint64, float32, float64. JSON
// decoding, with or without UseNumber, produces only these, nil, []any and
// map[string]any.
//
// The rules, checked in this order:
//
//   - nil (a missing or JSON-null operand) is refused with
//     [ErrOperandMissing] for every operator except [FilterIsNull] and
//     [FilterNotNull], which take no operand. Accepting it would compare it
//     as the empty string: EQUALS would match "", and CONTAINS or
//     STARTS_WITH every string.
//   - A map of an unnamed type (a decoded JSON object is a map[string]any)
//     is refused with [ErrOperandObject] for every operator, IS_NULL and
//     NOT_NULL included.
//   - A slice or array of an unnamed type is refused with [ErrOperandArray]
//     for every operator except [FilterBetween] and [FilterBetweenInclusive].
//   - Any other value outside the scalar set is refused with
//     [ErrOperandNotScalar] for every operator: a pointer (nil or not), a
//     struct (time.Time included: its printed form is not ISO-8601, so it
//     would compare the wrong text), a func, a channel, a complex number, a
//     uintptr, and every named type other than json.Number, whatever its
//     kind (a String() method would make it compare as that text).
//   - Under FilterBetween and FilterBetweenInclusive the operand must be a
//     []any (a decoded JSON array) of exactly two bounds, each a scalar;
//     otherwise it is refused with an error wrapping [ErrBetweenBounds].
//
// A scalar under IS_NULL or NOT_NULL is accepted and ignored. The tree
// operators (and, or, not) carry no operand and are not leaves; do not call
// this for them.
//
// Every refusal wraps [ErrInvalidOperand]. The error does not name op or the
// leaf: the caller does, in its own vocabulary. The operand itself is not
// echoed, only its Go type or bound count.
func ValidateOperand(op FilterOp, value any) error {
	isRange := op == FilterBetween || op == FilterBetweenInclusive
	switch operandShapeOf(value) {
	case shapeMissing:
		if op == FilterIsNull || op == FilterNotNull {
			return nil
		}
		return ErrOperandMissing
	case shapeObject:
		return ErrOperandObject
	case shapeOther:
		return ErrOperandNotScalar
	case shapeArray:
		if !isRange {
			return ErrOperandArray
		}
		pair, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%w: got %T", ErrBetweenNotArray, value)
		}
		if len(pair) != 2 {
			return fmt.Errorf("%w: got %d", ErrBetweenCount, len(pair))
		}
		for _, b := range pair {
			switch operandShapeOf(b) {
			case shapeScalar:
			case shapeMissing:
				return ErrBetweenNullBound
			default:
				return ErrBetweenNonScalarBound
			}
		}
		return nil
	}
	if isRange {
		return fmt.Errorf("%w: got %T", ErrBetweenNotArray, value)
	}
	return nil
}

// operandShape is the shape class [ValidateOperand] judges an operand by.
type operandShape int

const (
	shapeScalar  operandShape = iota // in the accepted scalar set
	shapeMissing                     // nil
	shapeObject                      // an unnamed map type
	shapeArray                       // an unnamed slice or array type
	shapeOther                       // anything else
)

// operandShapeOf classifies v. The scalar set is named, not inferred: a value
// is a scalar only if its type is one of the accepted types, so a Go type the
// rule does not know is refused rather than compared as its printed form. A
// named type other than json.Number is never a scalar, whatever its kind: a
// String() method would make it print as that text.
func operandShapeOf(v any) operandShape {
	switch v.(type) {
	case nil:
		return shapeMissing
	case string, bool, json.Number,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return shapeScalar
	}
	t := reflect.TypeOf(v)
	if t.Name() != "" {
		return shapeOther
	}
	switch t.Kind() {
	case reflect.Map:
		return shapeObject
	case reflect.Slice, reflect.Array:
		return shapeArray
	}
	return shapeOther
}
