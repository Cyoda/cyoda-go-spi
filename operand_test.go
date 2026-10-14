package spi_test

import (
	"errors"
	"strings"
	"testing"

	spi "github.com/cyoda-platform/cyoda-go-spi"
	"github.com/cyoda-platform/cyoda-go-spi/predicate"
)

// operandOps is every leaf operator, so each rule below is checked against
// all of them rather than a sample.
var operandOps = []spi.FilterOp{
	spi.FilterEq, spi.FilterNe, spi.FilterGt, spi.FilterGte, spi.FilterLt, spi.FilterLte,
	spi.FilterContains, spi.FilterNotContains,
	spi.FilterStartsWith, spi.FilterNotStartsWith,
	spi.FilterEndsWith, spi.FilterNotEndsWith,
	spi.FilterLike, spi.FilterMatchesRegex,
	spi.FilterIEq, spi.FilterINe,
	spi.FilterIContains, spi.FilterINotContains,
	spi.FilterIStartsWith, spi.FilterINotStartsWith,
	spi.FilterIEndsWith, spi.FilterINotEndsWith,
	spi.FilterIsNull, spi.FilterNotNull,
	spi.FilterBetween, spi.FilterBetweenInclusive,
}

func isUnary(op spi.FilterOp) bool { return op == spi.FilterIsNull || op == spi.FilterNotNull }
func isRange(op spi.FilterOp) bool {
	return op == spi.FilterBetween || op == spi.FilterBetweenInclusive
}

func TestValidateOperand_Missing(t *testing.T) {
	for _, op := range operandOps {
		err := spi.ValidateOperand(op, nil)
		if isUnary(op) {
			if err != nil {
				t.Errorf("%s with no operand: got %v, want nil", op, err)
			}
			continue
		}
		if !errors.Is(err, spi.ErrOperandMissing) {
			t.Errorf("%s with no operand: got %v, want ErrOperandMissing", op, err)
		}
		if !errors.Is(err, spi.ErrInvalidOperand) {
			t.Errorf("%s with no operand: got %v, want it to wrap ErrInvalidOperand", op, err)
		}
	}
}

func TestValidateOperand_ObjectRefusedForEveryOperator(t *testing.T) {
	for _, op := range operandOps {
		err := spi.ValidateOperand(op, map[string]any{"a": 1})
		if !errors.Is(err, spi.ErrOperandObject) || !errors.Is(err, spi.ErrInvalidOperand) {
			t.Errorf("%s with an object operand: got %v, want ErrOperandObject", op, err)
		}
	}
}

func TestValidateOperand_ArrayOnlyForRange(t *testing.T) {
	for _, op := range operandOps {
		err := spi.ValidateOperand(op, []any{"a", "b"})
		if isRange(op) {
			if err != nil {
				t.Errorf("%s with a pair: got %v, want nil", op, err)
			}
			continue
		}
		if !errors.Is(err, spi.ErrOperandArray) || !errors.Is(err, spi.ErrInvalidOperand) {
			t.Errorf("%s with an array operand: got %v, want ErrOperandArray", op, err)
		}
	}
}

func TestValidateOperand_ScalarAccepted(t *testing.T) {
	for _, op := range operandOps {
		if isRange(op) {
			continue
		}
		for _, v := range []any{"x", "", float64(1), true, false} {
			if err := spi.ValidateOperand(op, v); err != nil {
				t.Errorf("%s with scalar %#v: got %v, want nil", op, v, err)
			}
		}
	}
}

func TestValidateOperand_BetweenBounds(t *testing.T) {
	cases := []struct {
		name   string
		value  any
		want   error // nil means accepted
		detail string
	}{
		{"pair", []any{float64(1), float64(2)}, nil, ""},
		{"string pair", []any{"a", "z"}, nil, ""},
		{"scalar", "5", spi.ErrBetweenNotArray, "string"},
		{"empty", []any{}, spi.ErrBetweenCount, "got 0"},
		{"one bound", []any{float64(1)}, spi.ErrBetweenCount, "got 1"},
		{"three bounds", []any{float64(1), float64(2), float64(3)}, spi.ErrBetweenCount, "got 3"},
		{"null low", []any{nil, float64(2)}, spi.ErrBetweenNullBound, ""},
		{"null high", []any{float64(1), nil}, spi.ErrBetweenNullBound, ""},
		{"object bound", []any{map[string]any{}, float64(2)}, spi.ErrBetweenNonScalarBound, ""},
		{"array bound", []any{float64(1), []any{float64(2)}}, spi.ErrBetweenNonScalarBound, ""},
		// The checks every operator shares come first, as at the search boundary.
		{"missing", nil, spi.ErrOperandMissing, ""},
		{"object", map[string]any{"a": 1}, spi.ErrOperandObject, ""},
	}
	for _, op := range []spi.FilterOp{spi.FilterBetween, spi.FilterBetweenInclusive} {
		for _, tc := range cases {
			err := spi.ValidateOperand(op, tc.value)
			if tc.want == nil {
				if err != nil {
					t.Errorf("%s %s: got %v, want nil", op, tc.name, err)
				}
				continue
			}
			if !errors.Is(err, tc.want) || !errors.Is(err, spi.ErrInvalidOperand) {
				t.Errorf("%s %s: got %v, want %v", op, tc.name, err, tc.want)
				continue
			}
			if tc.detail != "" && !strings.Contains(err.Error(), tc.detail) {
				t.Errorf("%s %s: error %q does not carry %q", op, tc.name, err, tc.detail)
			}
		}
	}
	// Every BETWEEN-specific refusal is a bounds refusal; the shared ones are not.
	for _, s := range []error{spi.ErrBetweenNotArray, spi.ErrBetweenCount, spi.ErrBetweenNullBound, spi.ErrBetweenNonScalarBound} {
		if !errors.Is(s, spi.ErrBetweenBounds) {
			t.Errorf("%v does not wrap ErrBetweenBounds", s)
		}
	}
	for _, s := range []error{spi.ErrOperandMissing, spi.ErrOperandObject, spi.ErrOperandArray} {
		if errors.Is(s, spi.ErrBetweenBounds) {
			t.Errorf("%v wraps ErrBetweenBounds", s)
		}
	}
}

// badOperands is every operand shape ConditionToFilter refuses, keyed by the
// operator it is refused under and the sentinel it is refused with.
var badOperands = []struct {
	name  string
	op    string
	value any
	want  error
}{
	{"missing EQUALS", "EQUALS", nil, spi.ErrOperandMissing},
	{"missing CONTAINS", "CONTAINS", nil, spi.ErrOperandMissing},
	{"object EQUALS", "EQUALS", map[string]any{"a": 1}, spi.ErrOperandObject},
	{"object IS_NULL", "IS_NULL", map[string]any{"a": 1}, spi.ErrOperandObject},
	{"array EQUALS", "EQUALS", []any{"a", "b"}, spi.ErrOperandArray},
	{"array NOT_NULL", "NOT_NULL", []any{"a"}, spi.ErrOperandArray},
	{"between scalar", "BETWEEN", "5", spi.ErrBetweenNotArray},
	{"between one bound", "BETWEEN_INCLUSIVE", []any{"a"}, spi.ErrBetweenCount},
	{"between null bound", "BETWEEN", []any{nil, "b"}, spi.ErrBetweenNullBound},
	{"between object bound", "BETWEEN", []any{"a", map[string]any{}}, spi.ErrBetweenNonScalarBound},
	{"missing BETWEEN", "BETWEEN", nil, spi.ErrOperandMissing},
}

func TestConditionToFilter_RefusesBadOperand(t *testing.T) {
	leaves := map[string]func(op string, v any) predicate.Condition{
		"simple": func(op string, v any) predicate.Condition {
			return &predicate.SimpleCondition{JsonPath: "$.name", OperatorType: op, Value: v}
		},
		"lifecycle": func(op string, v any) predicate.Condition {
			return &predicate.LifecycleCondition{Field: "state", OperatorType: op, Value: v}
		},
	}
	for kind, leaf := range leaves {
		for _, tc := range badOperands {
			ok := &predicate.SimpleCondition{JsonPath: "$.ok", OperatorType: "EQUALS", Value: "x"}
			shapes := map[string]predicate.Condition{
				"top level": leaf(tc.op, tc.value),
				"nested": &predicate.GroupCondition{Operator: "OR", Conditions: []predicate.Condition{
					ok,
					&predicate.GroupCondition{Operator: "NOT", Conditions: []predicate.Condition{leaf(tc.op, tc.value)}},
				}},
			}
			for shape, cond := range shapes {
				_, err := spi.ConditionToFilter(cond, nil)
				if !errors.Is(err, tc.want) || !errors.Is(err, spi.ErrInvalidOperand) {
					t.Errorf("%s %s %s: got %v, want %v", kind, shape, tc.name, err, tc.want)
				}
			}
		}
	}
}

// TestConditionToFilter_UnaryTakesNoOperand pins that IS_NULL and NOT_NULL
// translate with no operand and with a scalar one, as at the search boundary.
func TestConditionToFilter_UnaryTakesNoOperand(t *testing.T) {
	for _, op := range []string{"IS_NULL", "NOT_NULL"} {
		for _, v := range []any{nil, "x"} {
			if _, err := spi.ConditionToFilter(&predicate.SimpleCondition{JsonPath: "$.a", OperatorType: op, Value: v}, nil); err != nil {
				t.Errorf("simple %s value %#v: %v", op, v, err)
			}
			if _, err := spi.ConditionToFilter(&predicate.LifecycleCondition{Field: "state", OperatorType: op, Value: v}, nil); err != nil {
				t.Errorf("lifecycle %s value %#v: %v", op, v, err)
			}
		}
	}
}

// TestConditionToFilter_ArrayClauseObjectEntryRefused pins that an array
// clause's entry is held to the simple clause's rule: a null entry tests no
// position, an object entry is refused.
func TestConditionToFilter_ArrayClauseObjectEntryRefused(t *testing.T) {
	c := &predicate.ArrayCondition{JsonPath: "$.t[*]", OperatorType: "EQUALS", Values: []any{"a", map[string]any{"b": 1}}}
	_, err := spi.ConditionToFilter(c, nil)
	if !errors.Is(err, spi.ErrOperandObject) {
		t.Fatalf("got %v, want ErrOperandObject", err)
	}
}

func TestPrepare_RefusesBadOperand(t *testing.T) {
	str := []spi.DataType{spi.String}
	cases := []struct {
		name string
		f    spi.Filter
		want error
	}{
		{"EQUALS nil", spi.Filter{Op: spi.FilterEq, Source: spi.SourceData, Path: "a", Declared: str}, spi.ErrOperandMissing},
		{"CONTAINS nil", spi.Filter{Op: spi.FilterContains, Source: spi.SourceData, Path: "a", Declared: str}, spi.ErrOperandMissing},
		{"STARTS_WITH nil meta", spi.Filter{Op: spi.FilterStartsWith, Source: spi.SourceMeta, Path: "state", Declared: str}, spi.ErrOperandMissing},
		{"EQUALS object", spi.Filter{Op: spi.FilterEq, Source: spi.SourceData, Path: "a", Value: map[string]any{"x": 1}, Declared: str}, spi.ErrOperandObject},
		{"EQUALS array", spi.Filter{Op: spi.FilterEq, Source: spi.SourceData, Path: "a", Value: []any{"x"}, Declared: str}, spi.ErrOperandArray},
		{"IS_NULL object", spi.Filter{Op: spi.FilterIsNull, Source: spi.SourceData, Path: "a", Value: map[string]any{}}, spi.ErrOperandObject},
		{"BETWEEN no bounds", spi.Filter{Op: spi.FilterBetween, Source: spi.SourceData, Path: "a", Declared: str}, spi.ErrBetweenCount},
		{"BETWEEN null bound", spi.Filter{Op: spi.FilterBetween, Source: spi.SourceData, Path: "a", Values: []any{"a", nil}, Declared: str}, spi.ErrBetweenNullBound},
		{"BETWEEN object bound", spi.Filter{Op: spi.FilterBetweenInclusive, Source: spi.SourceData, Path: "a", Values: []any{[]any{}, "z"}, Declared: str}, spi.ErrBetweenNonScalarBound},
	}
	for _, tc := range cases {
		for shape, f := range map[string]spi.Filter{
			"top level": tc.f,
			"nested":    {Op: spi.FilterOr, Children: []spi.Filter{{Op: spi.FilterNot, Children: []spi.Filter{tc.f}}}},
		} {
			_, err := spi.Prepare(f)
			if !errors.Is(err, spi.ErrUnevaluableLeaf) || !errors.Is(err, tc.want) {
				t.Errorf("%s %s: got %v, want ErrUnevaluableLeaf wrapping %v", shape, tc.name, err, tc.want)
			}
		}
	}
}

// TestPrepare_RangeBoundsReadFromValues pins that a range leaf is checked on
// Values, the field the kernel evaluates, so a directly built pair with no
// Value prepares.
func TestPrepare_RangeBoundsReadFromValues(t *testing.T) {
	f := spi.Filter{Op: spi.FilterBetween, Source: spi.SourceData, Path: "a", Values: []any{"a", "z"}, Declared: []spi.DataType{spi.String}}
	if _, err := spi.Prepare(f); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
}

// TestPrepare_UnaryTakesNoOperand pins that IS_NULL and NOT_NULL prepare with
// no operand.
func TestPrepare_UnaryTakesNoOperand(t *testing.T) {
	for _, op := range []spi.FilterOp{spi.FilterIsNull, spi.FilterNotNull} {
		if _, err := spi.Prepare(spi.Filter{Op: op, Source: spi.SourceData, Path: "a"}); err != nil {
			t.Errorf("%s: %v", op, err)
		}
	}
}
