package spi

import (
	"math"
	"math/big"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

// ---------------------------------------------------------------------------
// StripTrailingZeros: the digit-at-a-time oracle.
// ---------------------------------------------------------------------------

// stripByDivision is the previous StripTrailingZeros implementation, kept as
// the oracle: one full-width QuoRem per trailing zero digit. Correct, and
// quadratic in the number of zeros — which is why it is only ever run here,
// on inputs small enough to afford it.
func stripByDivision(d Decimal) Decimal {
	if d.unscaled == nil || d.unscaled.Sign() == 0 {
		return Decimal{unscaled: new(big.Int), scale: 0}
	}
	u := new(big.Int).Set(d.unscaled)
	scale := d.scale
	ten := big.NewInt(10)
	zero := big.NewInt(0)
	q := new(big.Int)
	r := new(big.Int)
	for {
		q.QuoRem(u, ten, r)
		if r.Cmp(zero) != 0 {
			break
		}
		if scale == math.MinInt32 {
			break
		}
		u.Set(q)
		scale--
	}
	return Decimal{unscaled: u, scale: scale}
}

func TestStripTrailingZeros_AgreesWithDivisionOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 20000; i++ {
		var sb strings.Builder
		if rng.Intn(2) == 0 {
			sb.WriteByte('-')
		}
		// A leading significant digit, a body of arbitrary digits, then a
		// run of up to 50 trailing zeros — so the oracle's per-zero loop
		// stays affordable while every zero-run length is exercised.
		sb.WriteByte(byte('1' + rng.Intn(9)))
		for n := rng.Intn(20); n > 0; n-- {
			sb.WriteByte(byte('0' + rng.Intn(10)))
		}
		for n := rng.Intn(51); n > 0; n-- {
			sb.WriteByte('0')
		}
		u, ok := new(big.Int).SetString(sb.String(), 10)
		if !ok {
			t.Fatalf("bad generated coefficient %q", sb.String())
		}
		// Scales spanning both signs, plus the int32 extremes so the
		// underflow guard is inside the population rather than beside it.
		var scale int32
		switch rng.Intn(10) {
		case 0:
			scale = math.MinInt32
		case 1:
			scale = math.MinInt32 + int32(rng.Intn(30))
		case 2:
			scale = math.MaxInt32
		default:
			scale = int32(rng.Intn(2001) - 1000)
		}
		d := Decimal{unscaled: u, scale: scale}
		got, want := d.StripTrailingZeros(), stripByDivision(d)
		if got.Scale() != want.Scale() || got.Unscaled().Cmp(want.Unscaled()) != 0 {
			t.Fatalf("StripTrailingZeros({%s, scale %d}) = {%s, scale %d}, oracle says {%s, scale %d}",
				u, scale, got.Unscaled(), got.Scale(), want.Unscaled(), want.Scale())
		}
	}
}

// A million trailing zeros arrive in a 1,000,002-byte operand — well inside
// the request body cap — and the digit-at-a-time loop spends minutes on them.
// Stripping must cost the value's own size, not its zero count.
func TestStripTrailingZeros_MillionZerosIsBounded(t *testing.T) {
	lit := "1" + strings.Repeat("0", 1000000)
	cases := []struct {
		name      string
		operand   string
		wantScale int32
	}{
		{"integer literal", lit, -1000000},
		{"scientific literal", lit + "e-500000", -500000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := ParseDecimal(tc.operand)
			if err != nil {
				t.Fatalf("ParseDecimal: %v", err)
			}
			done := make(chan Decimal, 1)
			go func() { done <- d.StripTrailingZeros() }()
			select {
			case got := <-done:
				if got.Unscaled().Cmp(big.NewInt(1)) != 0 {
					t.Errorf("unscaled: got %s, want 1", got.Unscaled())
				}
				if got.Scale() != tc.wantScale {
					t.Errorf("scale: got %d, want %d", got.Scale(), tc.wantScale)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("StripTrailingZeros did not return within 2s — the zero run was walked one digit at a time")
			}
		})
	}
}

// The int32 scale floor still stops the strip, and still stops it in bounded
// time: a million zeros with only one scale step of headroom must strip
// exactly one of them and leave the rest.
func TestStripTrailingZeros_ScaleFloorTerminatesBounded(t *testing.T) {
	u, ok := new(big.Int).SetString("1"+strings.Repeat("0", 1000000), 10)
	if !ok {
		t.Fatal("bad coefficient")
	}
	d := Decimal{unscaled: u, scale: math.MinInt32 + 1}
	done := make(chan Decimal, 1)
	go func() { done <- d.StripTrailingZeros() }()
	select {
	case got := <-done:
		if got.Scale() != math.MinInt32 {
			t.Errorf("scale: got %d, want %d (one step of headroom, one zero stripped)", got.Scale(), int32(math.MinInt32))
		}
		want, _ := new(big.Int).SetString("1"+strings.Repeat("0", 999999), 10)
		if got.Unscaled().Cmp(want) != 0 {
			t.Errorf("unscaled has %d digits, want %d", len(got.Unscaled().String()), 1000000)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StripTrailingZeros did not return within 2s at the scale floor")
	}
}

// ---------------------------------------------------------------------------
// roundToScale: the unguarded oracle.
// ---------------------------------------------------------------------------

// roundToScaleByExp is the previous roundToScale body: it always materialises
// 10^(scale-newScale). Kept as the oracle for scale gaps small enough to
// afford it.
func roundToScaleByExp(d Decimal, newScale int32, mode roundingMode) Decimal {
	if d.unscaled == nil {
		return Decimal{unscaled: new(big.Int), scale: newScale}
	}
	if newScale >= d.scale {
		r, err := d.SetScale(newScale)
		if err != nil {
			panic(err)
		}
		return r
	}
	factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(d.scale)-int64(newScale)), nil)
	q := new(big.Int)
	rem := new(big.Int)
	q.QuoRem(d.unscaled, factor, rem)
	if rem.Sign() != 0 {
		switch mode {
		case roundCeiling:
			if rem.Sign() > 0 {
				q.Add(q, big.NewInt(1))
			}
		case roundFloor:
			if rem.Sign() < 0 {
				q.Sub(q, big.NewInt(1))
			}
		}
	}
	return Decimal{unscaled: q, scale: newScale}
}

func TestRoundToScale_AgreesWithUnguardedOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	modes := []roundingMode{roundCeiling, roundFloor}
	for i := 0; i < 20000; i++ {
		var sb strings.Builder
		if rng.Intn(2) == 0 {
			sb.WriteByte('-')
		}
		digits := rng.Intn(40) + 1
		sb.WriteByte(byte('1' + rng.Intn(9)))
		for j := 1; j < digits; j++ {
			sb.WriteByte(byte('0' + rng.Intn(10)))
		}
		u, _ := new(big.Int).SetString(sb.String(), 10)
		if rng.Intn(25) == 0 {
			u = new(big.Int) // zero must take the same branch on both sides
		}
		scale := int32(rng.Intn(201) - 100)
		d := Decimal{unscaled: u, scale: scale}
		p := d.Precision()
		// Straddle the guard threshold: gaps from -10 (upward rescale) to
		// p+10 (well past the point where the whole value is dropped).
		gap := rng.Intn(p+21) - 10
		newScale := int32(int64(scale) - int64(gap))
		mode := modes[rng.Intn(len(modes))]
		got := d.roundToScale(newScale, mode)
		want := roundToScaleByExp(d, newScale, mode)
		if got.Scale() != want.Scale() || got.Unscaled().Cmp(want.Unscaled()) != 0 {
			t.Fatalf("roundToScale({%s, scale %d}, %d, mode %d) = {%s, scale %d}, oracle says {%s, scale %d}",
				u, scale, newScale, mode, got.Unscaled(), got.Scale(), want.Unscaled(), want.Scale())
		}
	}
}

// A 13-byte operand with an extreme negative exponent reaches roundToScale
// through both foldToInt (comparing ops, scale 0) and roundDoubleImprecise
// (scale 292). Materialising 10^2000000000 costs minutes and ~900 MB.
func TestExpandLeaf_ExtremeNegativeExponentIsBounded(t *testing.T) {
	cases := []struct {
		name     string
		op       FilterOp
		operand  string
		declared []DataType
		// wantSub is the single sub-condition the fold must produce.
		wantType  DataType
		wantValue string
		wantOp    FilterOp
	}{
		// GT rounds FLOOR (roundingModeFor): a tiny negative value floors
		// to -1, so "x > -1e-2000000000" becomes "x > -1" on an int bucket —
		// which is exactly the set of integers greater than a number just
		// below zero.
		{"gt tiny negative, long", FilterGt, "-1e-2000000000", []DataType{Long}, Long, "-1", FilterGt},
		// GTE rounds CEILING: a tiny positive value ceils to 1.
		{"gte tiny positive, integer", FilterGte, "1e-2000000000", []DataType{Integer}, Integer, "1", FilterGte},
		// LT rounds CEILING: a tiny positive value ceils to 1, so
		// "x < 0.1e-2000000000" becomes "x < 1" on an int bucket.
		{"lt tiny positive, unbound integer", FilterLt, "0.1e-2000000000", []DataType{UnboundInteger}, UnboundInteger, "1", FilterLt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			type result struct {
				e   Expansion
				err error
			}
			done := make(chan result, 1)
			go func() {
				e, err := ExpandLeaf(tc.op, tc.operand, nil, tc.declared)
				done <- result{e, err}
			}()
			select {
			case r := <-done:
				if r.err != nil {
					t.Fatalf("ExpandLeaf: %v", r.err)
				}
				if len(r.e.numeric) != 1 {
					t.Fatalf("got %d numeric sub-conditions, want 1: %+v", len(r.e.numeric), r.e.numeric)
				}
				sub := r.e.numeric[0]
				if sub.Type != tc.wantType || sub.Op != tc.wantOp {
					t.Errorf("sub = {%v, %v}, want {%v, %v}", sub.Type, sub.Op, tc.wantType, tc.wantOp)
				}
				if got := sub.Value.Canonical(); got != tc.wantValue {
					t.Errorf("folded value = %s, want %s", got, tc.wantValue)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("ExpandLeaf did not return within 2s — the scale gap was materialised")
			}
		})
	}
}

// The DOUBLE bucket reaches roundToScale through roundDoubleImprecise
// (scale 292), not foldToInt, so it needs its own bound. A tiny negative is
// comfortably inside the DOUBLE magnitude window, so the bucket rounds it
// rather than dropping the branch: FLOOR at scale 292 lands one ulp below
// zero.
func TestExpandLeaf_ExtremeNegativeExponentDoubleBucketIsBounded(t *testing.T) {
	type result struct {
		e   Expansion
		err error
	}
	done := make(chan result, 1)
	go func() {
		e, err := ExpandLeaf(FilterGt, "-1e-2000000000", nil, []DataType{Double})
		done <- result{e, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("ExpandLeaf: %v", r.err)
		}
		if len(r.e.numeric) != 1 {
			t.Fatalf("got %d numeric sub-conditions, want 1: %+v", len(r.e.numeric), r.e.numeric)
		}
		sub := r.e.numeric[0]
		if sub.Type != Double || sub.Op != FilterGt {
			t.Errorf("sub = {%v, %v}, want {DOUBLE, GT}", sub.Type, sub.Op)
		}
		// FLOOR to scale 292: -1e-2000000000 is negative and non-zero, so
		// the whole value is dropped and the result steps down one ulp.
		if got, want := sub.Value.Scale(), int32(292); got != want {
			t.Errorf("rounded scale = %d, want %d", got, want)
		}
		if got, want := sub.Value.Unscaled().String(), "-1"; got != want {
			t.Errorf("rounded unscaled = %s, want %s", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ExpandLeaf did not return within 2s — the scale gap was materialised")
	}
}

// The folded polarity has to be right, not merely cheap: evaluate the
// expansion against stored integers straddling the operand.
func TestEvalLeaf_ExtremeNegativeExponentPolarity(t *testing.T) {
	cases := []struct {
		name    string
		op      FilterOp
		operand string
		stored  string
		want    bool
	}{
		{"0 > -tiny", FilterGt, "-1e-2000000000", "0", true},
		{"1 > -tiny", FilterGt, "-1e-2000000000", "1", true},
		{"-1 > -tiny", FilterGt, "-1e-2000000000", "-1", false},
		{"0 >= +tiny", FilterGte, "1e-2000000000", "0", false},
		{"1 >= +tiny", FilterGte, "1e-2000000000", "1", true},
		{"-1 >= +tiny", FilterGte, "1e-2000000000", "-1", false},
		{"0 < +tiny", FilterLt, "1e-2000000000", "0", true},
		{"1 < +tiny", FilterLt, "1e-2000000000", "1", false},
		{"-1 < +tiny", FilterLt, "1e-2000000000", "-1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ExpandLeaf(tc.op, tc.operand, nil, []DataType{UnboundInteger})
			if err != nil {
				t.Fatalf("ExpandLeaf: %v", err)
			}
			if got := EvalLeaf(e, gjson.Parse(tc.stored)); got != tc.want {
				t.Errorf("EvalLeaf(stored %s %s %s) = %v, want %v", tc.stored, tc.op, tc.operand, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Cmp: magnitude decided without a string conversion.
// ---------------------------------------------------------------------------

// A large-coefficient UNBOUND sink value compared against an ordinary one is
// per-row work in evalCompare/evalBetween. Two Precision() calls — two full
// big.Int-to-string conversions — per row is not a cost the row loop can
// carry.
func TestDecimalCmp_LargeCoefficientIsBounded(t *testing.T) {
	digits := strings.Repeat("9", 1000000)
	u, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		t.Fatal("bad coefficient")
	}
	big1 := Decimal{unscaled: u, scale: 5}
	one := Decimal{unscaled: big.NewInt(1), scale: 0}
	done := make(chan int, 1)
	go func() { done <- big1.Cmp(one) }()
	select {
	case got := <-done:
		if got != 1 {
			t.Errorf("Cmp = %d, want 1", got)
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("Cmp did not return within 50ms — the coefficients were rendered to decimal")
	}
	done2 := make(chan int, 1)
	go func() { done2 <- one.Cmp(big1) }()
	select {
	case got := <-done2:
		if got != -1 {
			t.Errorf("Cmp = %d, want -1", got)
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("Cmp did not return within 50ms — the coefficients were rendered to decimal")
	}
}
