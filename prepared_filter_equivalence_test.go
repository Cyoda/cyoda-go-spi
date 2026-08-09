package spi

// The merge gate for the prepare/execute split.
//
// frozenMatchFilter below is a verbatim copy of the pre-split evaluator —
// MatchFilter, evalFilter, evalLeafFilter, EvalLeafString and evalLeafFast —
// taken before any of them were deleted. It is a COPY on purpose: it must keep
// answering the old way after the originals are gone, or the gate stops
// guarding anything the moment the deletion lands.
//
// What is frozen: the tree walk, the stored-value resolution, the fused
// expand-and-evaluate call, and the fast path — everything this change
// deleted from the live tree. Do not make this delegate to Prepare/Match;
// that would make the gate compare live code to itself.
//
// What is deliberately NOT frozen: ExpandLeaf and EvalLeaf, plus the shared
// helpers (OperandString, valuesToStrings, metaGjsonResult, cmpResult,
// ParseDecimal) below it in the call graph. Both the frozen side and Prepare
// call the same live copies of these — freezing them would mean freezing the
// ~500-line kernel this change does not touch.
//
// What the agreement proves: the hoist. The frozen side calls ExpandLeaf once
// per row; Prepare calls it once per query. If the two still answer alike
// across the corpus, the hoist changed no answers. It does NOT guard the
// kernel itself — a change to ExpandLeaf or EvalLeaf moves both sides
// identically and this gate stays green regardless.

import (
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

// --- frozen reference ------------------------------------------------------

func frozenMatchFilter(f Filter, data []byte, meta EntityMeta) bool {
	if f.Op == "" {
		return true
	}
	return frozenEvalFilter(f, data, meta)
}

func frozenEvalFilter(f Filter, data []byte, meta EntityMeta) bool {
	switch f.Op {
	case FilterAnd:
		for _, c := range f.Children {
			if !frozenEvalFilter(c, data, meta) {
				return false
			}
		}
		return true
	case FilterOr:
		for _, c := range f.Children {
			if frozenEvalFilter(c, data, meta) {
				return true
			}
		}
		return false
	}
	return frozenEvalLeafFilter(f, data, meta)
}

func frozenEvalLeafFilter(f Filter, data []byte, meta EntityMeta) bool {
	stored := frozenStoredResult(f, data, meta)
	matched, err := frozenEvalLeafString(f.Op, OperandString(f.Value), valuesToStrings(f.Values), f.Declared, stored)
	return matched && err == nil
}

func frozenStoredResult(f Filter, data []byte, meta EntityMeta) gjson.Result {
	if f.Source == SourceMeta {
		r, _ := metaGjsonResult(f.Path, meta)
		return r
	}
	return gjson.GetBytes(data, f.Path)
}

func frozenEvalLeafString(op FilterOp, operand string, values []string, declared []DataType, stored gjson.Result) (bool, error) {
	if matched, handled := frozenEvalLeafFast(op, operand, declared, stored); handled {
		return matched, nil
	}
	exp, err := ExpandLeaf(op, operand, values, declared)
	if err != nil {
		return false, err
	}
	return EvalLeaf(exp, stored), nil
}

func frozenEvalLeafFast(op FilterOp, operand string, declared []DataType, stored gjson.Result) (matched, handled bool) {
	if len(declared) != 1 {
		return false, false
	}
	switch op {
	case FilterEq, FilterNe, FilterGt, FilterGte, FilterLt, FilterLte:
	default:
		return false, false
	}
	nullish := !stored.Exists() || stored.Type == gjson.Null

	switch declared[0] {
	case String:
		if nullish {
			return false, true
		}
		if stored.Type != gjson.String {
			return false, true
		}
		return cmpResult(strings.Compare(stored.String(), operand), op), true

	case UnboundDecimal:
		opDec, err := ParseDecimal(operand)
		if err != nil {
			return false, false
		}
		if nullish {
			return false, true
		}
		if stored.Type != gjson.Number {
			return false, true
		}
		storedDec, err := ParseDecimal(stored.Raw)
		if err != nil {
			return false, true
		}
		return cmpResult(storedDec.Cmp(opDec), op), true
	}
	return false, false
}

// --- generated corpus ------------------------------------------------------

// The generator emits only WELL-FORMED filters — the shapes ConditionToFilter
// actually produces. Spec §5's changed cases are malformed by construction and
// are covered by hand-written tables elsewhere (Tasks 9 and 14), not here.

var genOps = []FilterOp{
	FilterEq, FilterNe, FilterGt, FilterGte, FilterLt, FilterLte,
	FilterContains, FilterStartsWith, FilterEndsWith, FilterLike, FilterMatchesRegex,
	FilterNotContains, FilterNotStartsWith, FilterNotEndsWith,
	FilterIEq, FilterINe, FilterIContains, FilterINotContains,
	FilterIStartsWith, FilterINotStartsWith, FilterIEndsWith, FilterINotEndsWith,
	FilterIsNull, FilterNotNull,
	FilterBetween, FilterBetweenInclusive,
}

var genDeclared = [][]DataType{
	{String},
	{Integer},
	{Long},
	{UnboundDecimal},
	{Double},
	{Boolean},
	{UUIDType},
	{ZonedDateTime},
	{LocalDate},
	{Integer, String},
	{Double, Integer},
	{ZonedDateTime, String},
	nil,
}

var genDataPaths = []string{"name", "qty", "price", "flag", "uid", "when", "missing", "nested.inner"}

var genMetaPaths = []string{"state", "id", "creationDate", "lastUpdateTime", "transactionId", "version", "change_type"}

var genOperands = []string{
	"Alice", "alice", "ALICE", "Bob", "", "A%", "A.*", "a.*e", "%ice",
	"30", "30.0", "12.78", "-5", "0", "9223372036854775807", "1e3",
	"true", "false",
	"6ba7b810-9dad-11d1-80b4-00c04fd430c8",
	"2024-01-01T00:00:00Z", "2024-06-01", "2024", "2024-01-01T00:00:00+02:00",
	"não", "日本", "\\%literal",
}

var genDocs = []string{
	`{"name":"Alice","qty":30,"price":12.78,"flag":true,"uid":"6ba7b810-9dad-11d1-80b4-00c04fd430c8","when":"2024-01-01T00:00:00Z","nested":{"inner":"deep"}}`,
	`{"name":"alice","qty":31,"price":-5,"flag":false,"when":"2024-06-01"}`,
	`{"name":"","qty":0,"price":0.0,"when":"2024"}`,
	`{"name":"não","qty":9223372036854775807,"when":"2024-01-01T00:00:00+02:00"}`,
	`{"name":null,"qty":null}`,
	`{}`,
	`{"name":30,"qty":"30"}`,
	`{"name":["Alice","Bob"],"qty":[1,2]}`,
}

func genLeaf(r *rand.Rand) Filter {
	if r.Intn(20) == 0 {
		return Filter{} // zero-Op child: a leaf that never matches
	}
	f := Filter{Op: genOps[r.Intn(len(genOps))]}
	if r.Intn(4) == 0 {
		f.Source = SourceMeta
		f.Path = genMetaPaths[r.Intn(len(genMetaPaths))]
	} else {
		f.Source = SourceData
		f.Path = genDataPaths[r.Intn(len(genDataPaths))]
	}
	f.Declared = genDeclared[r.Intn(len(genDeclared))]
	if f.Op == FilterBetween || f.Op == FilterBetweenInclusive {
		// Deliberately also emit the wrong arity sometimes: ExpandLeaf's arity
		// error is a per-row non-match in both implementations and must stay so.
		switch r.Intn(4) {
		case 0:
			f.Values = []any{genOperands[r.Intn(len(genOperands))]}
		default:
			f.Values = []any{
				genOperands[r.Intn(len(genOperands))],
				genOperands[r.Intn(len(genOperands))],
			}
		}
		return f
	}
	f.Value = genOperands[r.Intn(len(genOperands))]
	return f
}

func genFilter(r *rand.Rand, depth int) Filter {
	if depth <= 0 || r.Intn(3) == 0 {
		return genLeaf(r)
	}
	op := FilterAnd
	if r.Intn(2) == 0 {
		op = FilterOr
	}
	n := r.Intn(4) // 0..3 children — zero children exercises the group identities
	f := Filter{Op: op}
	for i := 0; i < n; i++ {
		f.Children = append(f.Children, genFilter(r, depth-1))
	}
	return f
}

func genMeta(r *rand.Rand) EntityMeta {
	metas := []EntityMeta{
		{ID: "ent-1", State: "active", Version: 7,
			CreationDate: mustTime("2024-01-01T00:00:00Z"), LastModifiedDate: mustTime("2024-06-01T00:00:00Z"),
			ChangeType: "UPDATED", TransactionID: "tx-1", TransitionForLatestSave: "approve"},
		{ID: "ent-2", State: "", Version: 0},
		{},
	}
	return metas[r.Intn(len(metas))]
}

// Corpus size and seed are overridable so a one-off widened exploration is
// reproducible. The committed defaults ARE the standing gate; -count alone
// widens nothing, because a fixed seed regenerates the same corpus.
func equivCases() int  { return envInt("SPI_EQUIV_CASES", 200000) }
func equivSeed() int64 { return int64(envIntFrom(0, "SPI_EQUIV_SEED", 0x30C0DE)) }

func envInt(key string, def int) int {
	return envIntFrom(1, key, def)
}

// envIntFrom parses key as a base-10 int, accepting any value >= min. Passing
// min=0 lets a caller distinguish "unset" from "explicitly zero" — needed for
// SPI_EQUIV_SEED, where 0 is a valid seed someone might set to reproduce a
// specific run, not a sentinel for "use the default".
func envIntFrom(min int, key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= min {
			return n
		}
	}
	return def
}

// TestPrepare_EquivalentToFrozenMatchFilter is the merge gate. Exact agreement,
// no carve-outs: the prepare/execute split changes no answers.
func TestPrepare_EquivalentToFrozenMatchFilter(t *testing.T) {
	cases := equivCases()
	r := rand.New(rand.NewSource(equivSeed()))

	for i := 0; i < cases; i++ {
		f := genFilter(r, 3)
		data := []byte(genDocs[r.Intn(len(genDocs))])
		meta := genMeta(r)

		want := frozenMatchFilter(f, data, meta)
		got := Prepare(f).Match(data, meta)

		if got != want {
			t.Fatalf("DIVERGENCE at case %d\n  prepared=%v frozen=%v\n  filter=%#v\n  data=%s\n  meta=%+v",
				i, got, want, f, data, meta)
		}
	}
}

// TestPrepare_MatchIsRepeatable pins that a prepared filter gives the same
// answer on every call — no state is consumed by evaluation.
func TestPrepare_MatchIsRepeatable(t *testing.T) {
	r := rand.New(rand.NewSource(0xBEEFED))
	for i := 0; i < 2000; i++ {
		f := genFilter(r, 3)
		data := []byte(genDocs[r.Intn(len(genDocs))])
		meta := genMeta(r)
		p := Prepare(f)
		first := p.Match(data, meta)
		for k := 0; k < 5; k++ {
			if p.Match(data, meta) != first {
				t.Fatalf("non-repeatable answer at case %d: filter=%#v", i, f)
			}
		}
	}
}

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}
