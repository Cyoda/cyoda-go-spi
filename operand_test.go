package spi_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	spi "github.com/cyoda-platform/cyoda-go-spi"
	"github.com/cyoda-platform/cyoda-go-spi/predicate"
)

// operandOps is every leaf operator, taken from the operator table itself so
// each rule below covers an operator added to it.
var operandOps = func() []spi.FilterOp {
	names := spi.OperatorNames()
	ops := make([]spi.FilterOp, 0, len(names))
	for _, n := range names {
		ops = append(ops, spi.MapOperator(n))
	}
	return ops
}()

func TestOperandOps_CoversTheTable(t *testing.T) {
	if len(operandOps) == 0 {
		t.Fatal("OperatorNames is empty")
	}
	for i, op := range operandOps {
		if op == "" {
			t.Errorf("OperatorNames()[%d] maps to no FilterOp", i)
		}
	}
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

// TestPrepare_OperandRefusalNamesTheLeaf pins that Prepare's operand refusal
// says which leaf failed, and that a range leaf with its pair in Value only
// is told the pair is read from Values.
func TestPrepare_OperandRefusalNamesTheLeaf(t *testing.T) {
	str := []spi.DataType{spi.String}
	nested := spi.Filter{Op: spi.FilterAnd, Children: []spi.Filter{
		{Op: spi.FilterEq, Source: spi.SourceData, Path: "ok", Value: "x", Declared: str},
		{Op: spi.FilterContains, Source: spi.SourceData, Path: "deep.name", Declared: str},
	}}
	_, err := spi.Prepare(nested)
	if !errors.Is(err, spi.ErrOperandMissing) || !strings.Contains(err.Error(), `"deep.name"`) {
		t.Errorf("nested nil operand: got %v, want ErrOperandMissing naming path \"deep.name\"", err)
	}

	misplaced := spi.Filter{Op: spi.FilterBetween, Source: spi.SourceData, Path: "a", Value: []any{"a", "z"}, Declared: str}
	_, err = spi.Prepare(misplaced)
	if !errors.Is(err, spi.ErrBetweenCount) || !strings.Contains(err.Error(), "read from Values") {
		t.Errorf("range pair in Value only: got %v, want ErrBetweenCount saying the pair is read from Values", err)
	}
	empty := spi.Filter{Op: spi.FilterBetween, Source: spi.SourceData, Path: "a", Declared: str}
	_, err = spi.Prepare(empty)
	if err == nil || strings.Contains(err.Error(), "read from Values") {
		t.Errorf("range leaf with no operand at all: got %v, want a refusal without the Values hint", err)
	}
}

// TestConditionToFilter_OperandRefusalSpeaksWireNames pins that a
// ConditionToFilter refusal, which a caller maps to a client error, names the
// operator the caller wrote rather than the kernel's FilterOp spelling.
func TestConditionToFilter_OperandRefusalSpeaksWireNames(t *testing.T) {
	conds := map[string]predicate.Condition{
		"simple":    &predicate.SimpleCondition{JsonPath: "$.name", OperatorType: "CONTAINS"},
		"lifecycle": &predicate.LifecycleCondition{Field: "state", OperatorType: "CONTAINS"},
	}
	for kind, c := range conds {
		_, err := spi.ConditionToFilter(c, nil)
		if !errors.Is(err, spi.ErrOperandMissing) {
			t.Fatalf("%s: got %v, want ErrOperandMissing", kind, err)
		}
		msg := err.Error()
		if !strings.Contains(msg, `operatorType "CONTAINS"`) {
			t.Errorf("%s: %q does not name the wire operator", kind, msg)
		}
		for _, kernel := range []string{`"contains"`, `"is_null"`, `"not_null"`} {
			if strings.Contains(msg, kernel) {
				t.Errorf("%s: %q carries the kernel name %s", kind, msg, kernel)
			}
		}
	}
}

type (
	namedInt   int
	namedUint  uint16
	namedFloat float64
	namedStr   string
	namedBool  bool
)

// TestValidateOperand_AcceptedTypes pins that ValidateOperand accepts a
// closed set of operand types — string, bool, json.Number and
// every int, uint and float kind — and refuses every other Go value, rather
// than letting it be compared as its printed form. Maps are objects, slices
// and arrays are arrays, and everything else is not a scalar.
func TestValidateOperand_AcceptedTypes(t *testing.T) {
	var nilStr *string
	var nilMap map[string]string
	s := "x"
	ts := time.Unix(0, 0)
	cases := []struct {
		name  string
		op    spi.FilterOp
		value any
		want  error // nil means accepted
	}{
		// Accepted scalars.
		{"string", spi.FilterEq, "x", nil},
		{"bool", spi.FilterEq, true, nil},
		{"json.Number", spi.FilterGt, json.Number("1.5"), nil},
		{"int", spi.FilterEq, 7, nil},
		{"int8", spi.FilterEq, int8(7), nil},
		{"int64", spi.FilterEq, int64(7), nil},
		{"uint", spi.FilterEq, uint(7), nil},
		{"uint8", spi.FilterEq, uint8(7), nil},
		{"uint64", spi.FilterEq, uint64(7), nil},
		{"float32", spi.FilterLt, float32(1.5), nil},
		{"float64", spi.FilterLt, 1.5, nil},
		{"named int", spi.FilterEq, namedInt(7), nil},
		{"named uint", spi.FilterEq, namedUint(7), nil},
		{"named float", spi.FilterEq, namedFloat(1.5), nil},
		{"scalar under IS_NULL", spi.FilterIsNull, "x", nil},
		{"BETWEEN named-int pair", spi.FilterBetweenInclusive, []any{namedInt(1), uint8(2)}, nil},

		// Maps are objects, slices and arrays are arrays.
		{"[]string", spi.FilterEq, []string{"a"}, spi.ErrOperandArray},
		{"[]int", spi.FilterContains, []int{1}, spi.ErrOperandArray},
		{"[2]any", spi.FilterEq, [2]any{"a", "b"}, spi.ErrOperandArray},
		{"[]byte", spi.FilterEq, []byte("a"), spi.ErrOperandArray},
		{"map[string]string", spi.FilterEq, map[string]string{}, spi.ErrOperandObject},
		{"nil map[string]string", spi.FilterEq, nilMap, spi.ErrOperandObject},
		{"map[int]any", spi.FilterIsNull, map[int]any{}, spi.ErrOperandObject},
		{"[]string under IS_NULL", spi.FilterNotNull, []string{}, spi.ErrOperandArray},

		// Everything else is not a scalar, under every operator.
		{"nil *string", spi.FilterEq, nilStr, spi.ErrOperandNotScalar},
		{"nil *string IS_NULL", spi.FilterIsNull, nilStr, spi.ErrOperandNotScalar},
		{"*string", spi.FilterEq, &s, spi.ErrOperandNotScalar},
		{"*time.Time", spi.FilterEq, &ts, spi.ErrOperandNotScalar},
		// time.Time is not in the set: its printed form is Go's, not
		// ISO-8601, so it would compare the wrong text.
		{"time.Time", spi.FilterEq, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), spi.ErrOperandNotScalar},
		{"time.Time under NOT_NULL", spi.FilterNotNull, ts, spi.ErrOperandNotScalar},
		{"BETWEEN time.Time", spi.FilterBetween, ts, spi.ErrOperandNotScalar},
		{"BETWEEN time.Time bound", spi.FilterBetween, []any{"2026-01-01", ts}, spi.ErrBetweenNonScalarBound},
		{"struct", spi.FilterEq, struct{ A int }{1}, spi.ErrOperandNotScalar},
		{"func", spi.FilterEq, func() {}, spi.ErrOperandNotScalar},
		{"chan", spi.FilterNotNull, make(chan int), spi.ErrOperandNotScalar},
		{"complex128", spi.FilterEq, complex(1, 2), spi.ErrOperandNotScalar},
		{"uintptr", spi.FilterEq, uintptr(1), spi.ErrOperandNotScalar},
		{"named string", spi.FilterEq, namedStr("x"), spi.ErrOperandNotScalar},
		{"named bool", spi.FilterEq, namedBool(true), spi.ErrOperandNotScalar},

		// Under a range operator: the shared refusals first, then the pair.
		{"BETWEEN []string pair", spi.FilterBetween, []string{"a", "b"}, spi.ErrBetweenNotArray},
		{"BETWEEN [2]any pair", spi.FilterBetweenInclusive, [2]any{"a", "b"}, spi.ErrBetweenNotArray},
		{"BETWEEN map[string]string", spi.FilterBetween, map[string]string{}, spi.ErrOperandObject},
		{"BETWEEN nil *string", spi.FilterBetween, nilStr, spi.ErrOperandNotScalar},
		{"BETWEEN struct", spi.FilterBetween, struct{}{}, spi.ErrOperandNotScalar},
		{"BETWEEN typed-nil bound", spi.FilterBetween, []any{nilStr, "z"}, spi.ErrBetweenNonScalarBound},
		{"BETWEEN pointer bound", spi.FilterBetween, []any{"a", &s}, spi.ErrBetweenNonScalarBound},
		{"BETWEEN struct bound", spi.FilterBetween, []any{struct{}{}, "z"}, spi.ErrBetweenNonScalarBound},
		{"BETWEEN named-string bound", spi.FilterBetween, []any{"a", namedStr("z")}, spi.ErrBetweenNonScalarBound},
		{"BETWEEN []string bound", spi.FilterBetween, []any{"a", []string{"z"}}, spi.ErrBetweenNonScalarBound},
		{"BETWEEN map[string]string bound", spi.FilterBetween, []any{map[string]string{}, "z"}, spi.ErrBetweenNonScalarBound},
	}
	if !errors.Is(spi.ErrOperandNotScalar, spi.ErrInvalidOperand) || errors.Is(spi.ErrOperandNotScalar, spi.ErrBetweenBounds) {
		t.Errorf("ErrOperandNotScalar must wrap ErrInvalidOperand and not ErrBetweenBounds")
	}
	for _, tc := range cases {
		err := spi.ValidateOperand(tc.op, tc.value)
		if tc.want == nil {
			if err != nil {
				t.Errorf("%s: got %v, want nil", tc.name, err)
			}
			continue
		}
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, err, tc.want)
		}
	}
}

// TestValidateOperand_DecodedJSONKeepsItsClass pins that the accepted set
// covers every value a JSON decode produces, with and without UseNumber:
// scalars accepted, null missing, an object an object, an array an array
// (and a range pair under BETWEEN). Protobuf structpb decoding yields the
// same Go types as plain Unmarshal (float64, string, bool, nil, []any,
// map[string]any).
func TestValidateOperand_DecodedJSONKeepsItsClass(t *testing.T) {
	const doc = `{"null":null,"bool":true,"string":"s","int":1,"float":1.5,"big":12345678901234567890,"exp":-2e300,"object":{"a":1},"array":[1,"z"]}`
	decoders := map[string]func() (map[string]any, error){
		"Unmarshal": func() (map[string]any, error) {
			var m map[string]any
			return m, json.Unmarshal([]byte(doc), &m)
		},
		"UseNumber": func() (map[string]any, error) {
			var m map[string]any
			dec := json.NewDecoder(strings.NewReader(doc))
			dec.UseNumber()
			return m, dec.Decode(&m)
		},
	}
	for name, decode := range decoders {
		m, err := decode()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, k := range []string{"bool", "string", "int", "float", "big", "exp"} {
			if err := spi.ValidateOperand(spi.FilterEq, m[k]); err != nil {
				t.Errorf("%s %s (%T): got %v, want accepted", name, k, m[k], err)
			}
		}
		if err := spi.ValidateOperand(spi.FilterEq, m["null"]); !errors.Is(err, spi.ErrOperandMissing) {
			t.Errorf("%s null: got %v, want ErrOperandMissing", name, err)
		}
		if err := spi.ValidateOperand(spi.FilterIsNull, m["null"]); err != nil {
			t.Errorf("%s null under IS_NULL: got %v, want accepted", name, err)
		}
		if err := spi.ValidateOperand(spi.FilterEq, m["object"]); !errors.Is(err, spi.ErrOperandObject) {
			t.Errorf("%s object: got %v, want ErrOperandObject", name, err)
		}
		if err := spi.ValidateOperand(spi.FilterEq, m["array"]); !errors.Is(err, spi.ErrOperandArray) {
			t.Errorf("%s array: got %v, want ErrOperandArray", name, err)
		}
		if err := spi.ValidateOperand(spi.FilterBetween, m["array"]); err != nil {
			t.Errorf("%s array under BETWEEN: got %v, want accepted", name, err)
		}
	}
}
