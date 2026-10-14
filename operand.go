package spi

import (
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
// The rules, checked in this order:
//
//   - A missing operand — nil, or a nil pointer — is refused with
//     [ErrOperandMissing] for every operator except [FilterIsNull] and
//     [FilterNotNull], which take no operand. Accepting it would compare it
//     as the empty string: EQUALS would match "", and CONTAINS or
//     STARTS_WITH every string.
//   - A map of any type (a decoded JSON object is a map[string]any) is
//     refused with [ErrOperandObject] for every operator, IS_NULL and
//     NOT_NULL included.
//   - A slice or array of any type is refused with [ErrOperandArray] for
//     every operator except [FilterBetween] and [FilterBetweenInclusive].
//   - Under FilterBetween and FilterBetweenInclusive the operand must be a
//     []any (a decoded JSON array) of exactly two bounds, each neither
//     missing, a map, a slice nor an array; otherwise it is refused with an
//     error wrapping [ErrBetweenBounds].
//
// Every other value is a scalar and is accepted: a string, a bool, any
// numeric kind, json.Number, time.Time. A scalar under IS_NULL
// or NOT_NULL is accepted and ignored. The tree operators (and, or, not)
// carry no operand and are not leaves; do not call this for them.
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
			case shapeMissing:
				return ErrBetweenNullBound
			case shapeObject, shapeArray:
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
	shapeScalar operandShape = iota
	shapeMissing
	shapeObject
	shapeArray
)

// operandShapeOf classifies v by its reflect kind, so a Go container type
// that no JSON decode produces ([]string, map[string]string) is classed with
// its JSON counterpart rather than as a scalar, which would compare as its
// printed form.
func operandShapeOf(v any) operandShape {
	if v == nil {
		return shapeMissing
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer:
		if rv.IsNil() {
			return shapeMissing
		}
	case reflect.Map:
		return shapeObject
	case reflect.Slice, reflect.Array:
		return shapeArray
	}
	return shapeScalar
}
