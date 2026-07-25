package spi

import (
	"sort"
	"testing"
)

// dec parses a decimal literal for oracle rows, failing the test on error.
func dec(t *testing.T, s string) Decimal {
	t.Helper()
	d, err := ParseDecimal(s)
	if err != nil {
		t.Fatalf("ParseDecimal(%q): %v", s, err)
	}
	return d
}

// sortSubs orders sub-conditions deterministically for multiset comparison.
func sortSubs(s []NumericSubCondition) []NumericSubCondition {
	out := make([]NumericSubCondition, len(s))
	copy(out, s)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		if out[i].Op != out[j].Op {
			return out[i].Op < out[j].Op
		}
		if out[i].NotNull != out[j].NotNull {
			return !out[i].NotNull
		}
		return out[i].Value.Cmp(out[j].Value) < 0
	})
	return out
}

// subEqual compares two sub-conditions; NotNull rows ignore Value.
func subEqual(a, b NumericSubCondition) bool {
	if a.Type != b.Type || a.Op != b.Op || a.NotNull != b.NotNull {
		return false
	}
	if a.NotNull {
		return true // Value is meaningless for a NOT_NULL branch
	}
	return a.Value.Cmp(b.Value) == 0
}

func assertSubs(t *testing.T, label string, got, want []NumericSubCondition) {
	t.Helper()
	gs := sortSubs(got)
	ws := sortSubs(want)
	if len(gs) != len(ws) {
		t.Errorf("%s: got %d sub-conditions %v, want %d %v", label, len(gs), gs, len(ws), ws)
		return
	}
	for i := range gs {
		if !subEqual(gs[i], ws[i]) {
			t.Errorf("%s: sub[%d] got %+v, want %+v (full got=%v want=%v)", label, i, gs[i], ws[i], gs, ws)
		}
	}
}

// notNull builds an expected NOT_NULL branch for a type.
func notNull(dt DataType) NumericSubCondition {
	return NumericSubCondition{Type: dt, Op: FilterNotNull, NotNull: true}
}

// inRange builds an expected in-range branch.
func inRange(t *testing.T, dt DataType, value string, op FilterOp) NumericSubCondition {
	return NumericSubCondition{Type: dt, Value: dec(t, value), Op: op}
}

func TestExpandNumericOperand(t *testing.T) {
	cases := []struct {
		name  string
		value string
		types []DataType
		op    FilterOp
		want  []NumericSubCondition
	}{
		// --- int-family CEILING/FLOOR rounding (fold once) -------------------
		{
			name:  ">=12.78 on [INTEGER] → CEILING → INTEGER >= 13",
			value: "12.78", types: []DataType{Integer}, op: FilterGte,
			want: []NumericSubCondition{inRange(t, Integer, "13", FilterGte)},
		},
		{
			name:  "<12.78 on [INTEGER] → CEILING → INTEGER < 13",
			value: "12.78", types: []DataType{Integer}, op: FilterLt,
			want: []NumericSubCondition{inRange(t, Integer, "13", FilterLt)},
		},
		{
			name:  "<=12.78 on [INTEGER] → FLOOR → INTEGER <= 12",
			value: "12.78", types: []DataType{Integer}, op: FilterLte,
			want: []NumericSubCondition{inRange(t, Integer, "12", FilterLte)},
		},
		{
			name:  ">12.78 on [INTEGER] → FLOOR → INTEGER > 12",
			value: "12.78", types: []DataType{Integer}, op: FilterGt,
			want: []NumericSubCondition{inRange(t, Integer, "12", FilterGt)},
		},
		// --- int-family ABOVE / BELOW → NOT_NULL or drop ---------------------
		{
			name:  "<2^40 on [INTEGER] → ABOVE + less → INTEGER NOT_NULL",
			value: "1099511627776", types: []DataType{Integer}, op: FilterLt,
			want: []NumericSubCondition{notNull(Integer)},
		},
		{
			name:  ">2^40 on [INTEGER] → ABOVE + great → dropped",
			value: "1099511627776", types: []DataType{Integer}, op: FilterGt,
			want: nil,
		},
		{
			name:  ">-2^40 on [INTEGER] → BELOW + great → INTEGER NOT_NULL",
			value: "-1099511627776", types: []DataType{Integer}, op: FilterGt,
			want: []NumericSubCondition{notNull(Integer)},
		},
		{
			name:  "<-2^40 on [INTEGER] → BELOW + less → dropped",
			value: "-1099511627776", types: []DataType{Integer}, op: FilterLt,
			want: nil,
		},
		// --- fractional EQUALS drops the whole int family --------------------
		{
			name:  "=12.5 on [INTEGER] → fractional EQUALS → void",
			value: "12.5", types: []DataType{Integer}, op: FilterEq,
			want: nil,
		},
		// --- whole EQUALS keeps the value + sink -----------------------------
		{
			name:  "=5 on [LONG, UNBOUND_INTEGER] → LONG =5 + UNBOUND sink",
			value: "5", types: []DataType{Long, UnboundInteger}, op: FilterEq,
			want: []NumericSubCondition{
				inRange(t, Long, "5", FilterEq),
				inRange(t, UnboundInteger, "5", FilterEq),
			},
		},
		// --- fold-once shared by sink and concrete bucket --------------------
		{
			name:  ">=12.78 on [INTEGER, UNBOUND_INTEGER] → both see folded 13",
			value: "12.78", types: []DataType{Integer, UnboundInteger}, op: FilterGte,
			want: []NumericSubCondition{
				inRange(t, Integer, "13", FilterGte),
				inRange(t, UnboundInteger, "13", FilterGte),
			},
		},
		// --- UNBOUND_DECIMAL sink emits verbatim (no rounding) ---------------
		{
			name:  ">=12.78 on [UNBOUND_DECIMAL] → verbatim 12.78",
			value: "12.78", types: []DataType{UnboundDecimal}, op: FilterGte,
			want: []NumericSubCondition{inRange(t, UnboundDecimal, "12.78", FilterGte)},
		},
		// --- DOUBLE keeps precise value verbatim while INTEGER folds ---------
		{
			name:  ">=12.78 on [INTEGER, DOUBLE] → DOUBLE 12.78, INTEGER 13",
			value: "12.78", types: []DataType{Integer, Double}, op: FilterGte,
			want: []NumericSubCondition{
				inRange(t, Double, "12.78", FilterGte),
				inRange(t, Integer, "13", FilterGte),
			},
		},
		// --- DOUBLE imprecise value rounds to 15 sig digits ------------------
		{
			name:  ">=17-digit on [DOUBLE] → CEILING to 15 sig digits",
			value: "1.2345678901234567", types: []DataType{Double}, op: FilterGte,
			want: []NumericSubCondition{inRange(t, Double, "1.23456789012346", FilterGte)},
		},
		{
			name:  ">17-digit on [DOUBLE] → FLOOR to 15 sig digits",
			value: "1.2345678901234567", types: []DataType{Double}, op: FilterGt,
			want: []NumericSubCondition{inRange(t, Double, "1.23456789012345", FilterGt)},
		},
		// --- DOUBLE imprecise + EQUALS drops the bucket ----------------------
		{
			name:  "=17-digit on [DOUBLE] → imprecise EQUALS → dropped",
			value: "1.2345678901234567", types: []DataType{Double}, op: FilterEq,
			want: nil,
		},
		// --- BIG_DECIMAL magnitude-only asymmetry (scale>18 still emitted) ---
		{
			name:  ">=1e-30 on [BIG_DECIMAL] → in-magnitude, emitted verbatim (scale>18)",
			value: "0.000000000000000000000000000001", types: []DataType{BigDecimal}, op: FilterGte,
			want: []NumericSubCondition{inRange(t, BigDecimal, "0.000000000000000000000000000001", FilterGte)},
		},
		// --- decimal-family ABOVE/BELOW → NOT_NULL / drop --------------------
		{
			name:  "<1e300 on [DOUBLE] → ABOVE + less → DOUBLE NOT_NULL",
			value: "1e300", types: []DataType{Double}, op: FilterLt,
			want: []NumericSubCondition{notNull(Double)},
		},
		{
			name:  ">1e300 on [DOUBLE] → ABOVE + great → dropped",
			value: "1e300", types: []DataType{Double}, op: FilterGt,
			want: nil,
		},
		// --- cross-family with sinks on both sides ---------------------------
		{
			name:  ">=12.78 on [INTEGER, UNBOUND_INTEGER, DOUBLE, UNBOUND_DECIMAL]",
			value: "12.78",
			types: []DataType{Integer, UnboundInteger, Double, UnboundDecimal}, op: FilterGte,
			want: []NumericSubCondition{
				inRange(t, Integer, "13", FilterGte),
				inRange(t, UnboundInteger, "13", FilterGte),
				inRange(t, Double, "12.78", FilterGte),
				inRange(t, UnboundDecimal, "12.78", FilterGte),
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ExpandNumericOperand(dec(t, c.value), c.types, c.op)
			assertSubs(t, c.name, got, c.want)
		})
	}
}

// TestExpandNumericOperand_Property checks that every emitted sub-condition's
// Type is one of the declared numeric types (or the family sink, which is only
// emitted when declared). No branch escapes the declared set.
func TestExpandNumericOperand_Property(t *testing.T) {
	values := []string{"12.78", "5", "1099511627776", "-1099511627776", "1e300",
		"1.2345678901234567", "0.000000000000000000000000000001"}
	ops := []FilterOp{FilterGt, FilterGte, FilterLt, FilterLte, FilterEq}
	typeSets := [][]DataType{
		{Integer}, {Long}, {BigInteger}, {UnboundInteger},
		{Double}, {BigDecimal}, {UnboundDecimal},
		{Integer, Long, BigInteger, UnboundInteger},
		{Double, BigDecimal, UnboundDecimal},
		{Integer, UnboundInteger, Double, UnboundDecimal},
	}
	for _, vs := range values {
		for _, op := range ops {
			for _, ts := range typeSets {
				declared := make(map[DataType]bool, len(ts))
				for _, dt := range ts {
					declared[dt] = true
				}
				got := ExpandNumericOperand(dec(t, vs), ts, op)
				for _, sub := range got {
					if !declared[sub.Type] {
						t.Errorf("value=%s op=%s types=%v: emitted undeclared type %s",
							vs, op, ts, sub.Type)
					}
				}
			}
		}
	}
}
