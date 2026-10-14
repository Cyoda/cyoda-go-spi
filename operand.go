package spi

import "fmt"

// ValidateOperand reports whether value is an operand the leaf operator op
// can use. It is the one rule for operand shape: [ConditionToFilter] applies
// it to every leaf it translates and [Prepare] to every leaf it prepares, so
// no translator or evaluator in this module sees an operand it refuses. A
// caller that validates conditions at its own boundary calls it to refuse the
// same set, and maps the sentinels to its own wording.
//
// The rules, checked in this order:
//
//   - nil (a missing or JSON-null operand) is refused with
//     [ErrOperandMissing] for every operator except [FilterIsNull] and
//     [FilterNotNull], which take no operand. Accepting it would compare it
//     as the empty string: EQUALS would match "", and CONTAINS or
//     STARTS_WITH every string.
//   - map[string]any is refused with [ErrOperandObject] for every operator,
//     IS_NULL and NOT_NULL included.
//   - []any is refused with [ErrOperandArray] for every operator except
//     [FilterBetween] and [FilterBetweenInclusive].
//   - Under FilterBetween and FilterBetweenInclusive the operand must be a
//     []any of exactly two bounds, each non-nil and neither a map nor an
//     array; otherwise it is refused with an error wrapping
//     [ErrBetweenBounds].
//
// A scalar operand under IS_NULL or NOT_NULL is accepted and ignored. The
// tree operators (and, or, not) carry no operand and are not leaves; do not
// call this for them.
//
// Every refusal wraps [ErrInvalidOperand] and names op. The operand itself is
// not echoed, only its Go type or bound count.
func ValidateOperand(op FilterOp, value any) error {
	switch v := value.(type) {
	case nil:
		if op != FilterIsNull && op != FilterNotNull {
			return fmt.Errorf("operator %q: %w; only %q and %q take none", op, ErrOperandMissing, FilterIsNull, FilterNotNull)
		}
		return nil
	case map[string]any:
		return fmt.Errorf("operator %q: %w", op, ErrOperandObject)
	case []any:
		if op != FilterBetween && op != FilterBetweenInclusive {
			return fmt.Errorf("operator %q: %w; only %q and %q take a [low, high] pair", op, ErrOperandArray, FilterBetween, FilterBetweenInclusive)
		}
		if len(v) != 2 {
			return fmt.Errorf("operator %q: %w: got %d", op, ErrBetweenCount, len(v))
		}
		for _, b := range v {
			switch b.(type) {
			case nil:
				return fmt.Errorf("operator %q: %w", op, ErrBetweenNullBound)
			case map[string]any, []any:
				return fmt.Errorf("operator %q: %w", op, ErrBetweenNonScalarBound)
			}
		}
		return nil
	}
	if op == FilterBetween || op == FilterBetweenInclusive {
		return fmt.Errorf("operator %q: %w: got %T", op, ErrBetweenNotArray, value)
	}
	return nil
}
