# LIKE glob matcher + exported pattern contract — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the LIKE→regex translation with a direct glob matcher, export one pattern-validation contract both the kernel and consumers share, and delete `ErrScanBudgetExhausted`.

**Architecture:** `LIKE` stops being a regex. Its operand is parsed once at `Prepare` time into a token slice (literal / one-char / any-run) and matched by a greedy scan with a single backtrack point — no compile step, no failure mode except a trailing unpaired escape. `MATCHES_PATTERN` keeps `regexp`, but now requires the operand to compile **bare as well as anchored**, which closes an anchor-escape hole. Both hide behind one unexported `patternMatcher` interface so `compileLeafPattern` is the single derivation, exported for validators as `ValidateLeafPattern` / `ValidateConditionPatterns`.

**Tech Stack:** Go 1.26.1, `github.com/cyoda-platform/cyoda-go-spi`, stdlib only (`regexp`, `regexp/syntax`, `unicode/utf8`), `testify/require` in tests.

**Spec:** `docs/superpowers/specs/2026-08-24-pattern-derivation-export-design.md` (commit `fdd8636`)

## Global Constraints

- **Repo/branch:** `cyoda-go-spi`, branch `feat/38-anchor-export`. This is a git worktree at `.worktrees/feat-38-anchor-export`.
- **No `t.Parallel()` in any test in package `spi`** — `compileRegex` is a package var that `prepared_filter_internal_test.go:15-16` swaps; parallel tests race it.
- **No new test files** except `spitest`'s and `like_pattern_test.go` (Task 1). The twelve-row grammar corpus stays in `eval_leaf_test.go`.
- **The matcher is immutable after `Prepare`.** Plan trees are evaluated from multiple goroutines. No scratch buffers, no per-row `[]rune(subject)`.
- **Errors never leak internals.** No `\A(?:`, no `syntax.Error.Expr`, no raw operand — see Task 2.
- **This module has no endpoints and no wire format**, so no HTTP/gRPC error-code table applies. Consumer-visible status codes belong to cyoda-go#475 and #479.
- **Do not tag.** `v0.8.4` is cut at milestone-end once spi#32 has merged. cyoda-go stays on a pseudo-version.
- **Run after every task:** `go build ./... && go vet ./... && go test ./...` from the worktree root.

## File Structure

**Create:**
- `like_pattern.go` — the glob matcher: `patternMatcher`, `likePattern`, `parseLikePattern`. One responsibility; keeps `eval_leaf.go` (already ~640 lines) from growing.
- `like_pattern_test.go` — matcher internals: tokenisation, UTF-8, pathological inputs, concurrency.
- `spitest/pattern.go` — the conformance case.

**Modify:**
- `errors.go` — add `ErrInvalidPattern`, delete `ErrScanBudgetExhausted:110-113`.
- `eval_leaf.go` — `Expansion.strRegex`→`strMatch` (`:103`), `ExpandLeaf`'s two compile arms (`:140-147`), `evalStringOp` (`:497`), add `compileMatchesPattern` + `compileLeafPattern` (Task 2, additive); **delete** the LIKE-grammar block (`:577-637`: `regexpSpecialChars`, `likeToRegex`, `hasEscapeRune`) in Task 3, together with the rewiring that stops calling it.
- `condition_filter.go` — add `ValidateConditionPatterns` + `validatePatternsAtDepth`; update `MaxConditionDepth` godoc (`:581-586`).
- `eval_leaf_test.go` — rename `TestLikeToRegex_Grammar`→`TestLike_Grammar` (body unchanged), add grammar and validator tests.
- `condition_filter_test.go` — walker tests.
- `prepared_filter_internal_test.go` — drop `FilterLike` from the compile-once loop (`:18`) and its branch (`:29`).
- `spitest/searcher.go` — register the new subtests.
- `CHANGELOG.md` — `[Unreleased]`.

**Deliberately NOT in this plan** (they live in cyoda-go and cannot compile until its pin bump — cyoda-go#516 steps 2/3): `cmd/cyoda/help/content/predicates.md`, the `docs/cloud-parity/` entry, `COMPATIBILITY.md`'s commercial-obligation list.

---

### Task 1: The LIKE glob matcher

**Files:**
- Create: `like_pattern.go`, `like_pattern_test.go`
- Modify: `errors.go` (add `ErrInvalidPattern`)

**Interfaces:**
- Consumes: nothing.
- Produces: `type patternMatcher interface{ matches(s string) bool }`; `func parseLikePattern(operand string) (patternMatcher, error)`; `var ErrInvalidPattern = errors.New("invalid pattern")`. Task 2 and Task 3 both depend on these exact names.

- [ ] **Step 1: Write the failing test**

Add to `like_pattern_test.go` (`package spi`):

```go
package spi

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

func mustLike(t *testing.T, pattern string) patternMatcher {
	t.Helper()
	m, err := parseLikePattern(pattern)
	if err != nil {
		t.Fatalf("parseLikePattern(%q) unexpected error: %v", pattern, err)
	}
	return m
}

func TestParseLikePattern_Grammar(t *testing.T) {
	cases := []struct {
		pattern, in string
		want        bool
	}{
		// %: any run, including empty and newlines
		{"%", "", true},
		{"%", "anything", true},
		{"%", "a\nb", true},
		{"foo%", "foobar", true},
		{"foo%", "foo", true},
		{"foo%", "xfoo", false},
		{"%foo%", "xxfooyy", true},
		// _: exactly one rune, including a newline
		{"a_c", "abc", true},
		{"a_c", "ac", false},
		{"a_b", "a\nb", true},
		{"_", "é", true},
		{"_", "ab", false},
		// \X is literal X, for any X — the SQL rule
		{`\d`, "d", true},
		{`\d`, "7", false},
		{`\d`, `\d`, false},
		{`\%`, "%", true},
		{`\%`, "5000", false},
		{`\_`, "_", true},
		{`\_`, "x", false},
		{`\\`, `\`, true},
		{`a\\b`, `a\b`, true},
		{`\[`, "[", true},
		// everything else is a literal, including regex metacharacters
		{"1.2", "1.2", true},
		{"1.2", "1x2", false},
		{"[a]", "[a]", true},
		{"a<b>c", "a<b>c", true},
		// whole-string anchored, case-sensitive
		{"abc", "xabcx", false},
		{"abc", "ABC", false},
		// empty pattern matches only the empty string
		{"", "", true},
		{"", "a", false},
	}
	for _, c := range cases {
		if got := mustLike(t, c.pattern).matches(c.in); got != c.want {
			t.Errorf("LIKE %q vs %q = %v, want %v", c.pattern, c.in, got, c.want)
		}
	}
}

func TestParseLikePattern_TrailingEscapeRejected(t *testing.T) {
	for _, pattern := range []string{`\`, `a\`, `%\`, `\\\`} {
		m, err := parseLikePattern(pattern)
		if err == nil {
			t.Errorf("parseLikePattern(%q) = nil error, want rejection", pattern)
			continue
		}
		if !errors.Is(err, ErrInvalidPattern) {
			t.Errorf("parseLikePattern(%q) error %v does not wrap ErrInvalidPattern", pattern, err)
		}
		if m != nil {
			t.Errorf("parseLikePattern(%q) returned a non-nil matcher alongside an error", pattern)
		}
		if strings.Contains(err.Error(), pattern) {
			t.Errorf("parseLikePattern(%q) error echoes the operand: %v", pattern, err)
		}
	}
	// An EVEN number of trailing backslashes is a literal, not an unpaired escape.
	if !mustLike(t, `a\\`).matches(`a\`) {
		t.Error(`LIKE "a\\\\" should match "a\\"`)
	}
}

func TestParseLikePattern_InvalidUTF8(t *testing.T) {
	// One invalid byte decodes as one U+FFFD, so "_" consumes exactly one.
	if !mustLike(t, "_").matches("\xff") {
		t.Error(`LIKE "_" should match a single invalid byte`)
	}
	if mustLike(t, "_").matches("\xff\xfe") {
		t.Error(`LIKE "_" should not match two invalid bytes`)
	}
}

func TestParseLikePattern_NoPathologicalBacktracking(t *testing.T) {
	// Would be exponential under a naive recursive matcher. Must finish fast.
	m := mustLike(t, strings.Repeat("%_", 40)+"z")
	if m.matches(strings.Repeat("a", 4000)) {
		t.Error("expected no match")
	}
}

func TestLikePattern_ConcurrentMatchIsRaceFree(t *testing.T) {
	m := mustLike(t, "%foo_bar%")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = m.matches("xxfooZbaryy")
			}
		}()
	}
	wg.Wait()
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./... -run 'TestParseLikePattern|TestLikePattern' 2>&1 | head -20`
Expected: FAIL — `undefined: parseLikePattern`, `undefined: patternMatcher`, `undefined: ErrInvalidPattern`.

- [ ] **Step 3: Add the sentinel**

In `errors.go`, immediately after the `ErrUnknownOperator` declaration:

```go
// ErrInvalidPattern is returned by [ValidateLeafPattern] and
// [ValidateConditionPatterns] for an operand that cannot be used as a pattern:
// a LIKE operand ending in an unpaired escape, or a MATCHES_PATTERN operand
// that does not compile.
//
// The wrapped message names the operator and the failure, and deliberately
// carries NEITHER the operand NOR the anchored form the kernel compiles — a
// caller puts this error into a client-facing 400, and both are internals.
var ErrInvalidPattern = errors.New("invalid pattern")
```

- [ ] **Step 4: Write the matcher**

Create `like_pattern.go`:

```go
package spi

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// patternMatcher answers "does this stored value match the leaf's operand".
// LIKE and MATCHES_PATTERN both produce one; nothing else does.
type patternMatcher interface{ matches(s string) bool }

// LIKE is a glob, not a regex. The grammar, which is PostgreSQL's and
// SQLite's `LIKE ... ESCAPE '\'`:
//
//   - '%'  any sequence of characters, including empty, INCLUDING newlines
//   - '_'  exactly one character (one rune), INCLUDING a newline
//   - '\X' the literal character X, for ANY X — so \%, \_ and \\ are literal
//     '%', '_' and '\', and \d is a literal 'd'
//   - anything else, itself
//
// The match is whole-string and case-sensitive. A trailing unpaired '\' is the
// only malformed pattern; every other operand matches something.
//
// This is deliberately NOT Cloud's Like.prepareSpecialCharacters, which
// translates to a regex and leaks RE2 escapes. cyoda-go leads this contract.
type likeTokKind uint8

const (
	tokLit likeTokKind = iota // literal text, matched bytewise
	tokOne                    // '_'
	tokAny                    // '%'
)

type likeTok struct {
	kind likeTokKind
	lit  string // tokLit only
}

// likePattern is immutable after construction: matches takes no lock and
// writes no field, because a prepared plan tree is evaluated concurrently.
type likePattern struct{ toks []likeTok }

// parseLikePattern tokenises operand. It allocates once, at Prepare time;
// matches allocates nothing.
func parseLikePattern(operand string) (patternMatcher, error) {
	var (
		toks []likeTok
		lit  strings.Builder
	)
	flush := func() {
		if lit.Len() > 0 {
			toks = append(toks, likeTok{kind: tokLit, lit: lit.String()})
			lit.Reset()
		}
	}
	for i := 0; i < len(operand); {
		switch operand[i] {
		case '\\':
			if i+1 >= len(operand) {
				// Byte offset, not the operand: this error reaches a 400.
				return nil, fmt.Errorf("%w: LIKE pattern ends with an unpaired escape at byte %d",
					ErrInvalidPattern, i)
			}
			r, sz := utf8.DecodeRuneInString(operand[i+1:])
			lit.WriteRune(r)
			i += 1 + sz
		case '%':
			flush()
			// Collapse runs of '%': "%%%%" behaves as "%" and cannot make the
			// scan below quadratic in the number of stars.
			if n := len(toks); n == 0 || toks[n-1].kind != tokAny {
				toks = append(toks, likeTok{kind: tokAny})
			}
			i++
		case '_':
			flush()
			toks = append(toks, likeTok{kind: tokOne})
			i++
		default:
			r, sz := utf8.DecodeRuneInString(operand[i:])
			lit.WriteRune(r)
			i += sz
		}
	}
	flush()
	return &likePattern{toks: toks}, nil
}

// matches runs the standard greedy wildcard scan with a single backtrack
// point at the most recent '%'. Linear in len(s) per star, never exponential.
func (p *likePattern) matches(s string) bool {
	starTok, starPos := -1, 0
	i, j := 0, 0
	for j < len(s) {
		if i < len(p.toks) {
			switch t := p.toks[i]; t.kind {
			case tokLit:
				if strings.HasPrefix(s[j:], t.lit) {
					j += len(t.lit)
					i++
					continue
				}
			case tokOne:
				_, sz := utf8.DecodeRuneInString(s[j:])
				j += sz
				i++
				continue
			case tokAny:
				starTok, starPos = i, j
				i++
				continue
			}
		}
		if starTok < 0 {
			return false
		}
		// Give the star one more character and retry from just after it.
		_, sz := utf8.DecodeRuneInString(s[starPos:])
		if sz == 0 {
			return false
		}
		starPos += sz
		i, j = starTok+1, starPos
	}
	for i < len(p.toks) && p.toks[i].kind == tokAny {
		i++
	}
	return i == len(p.toks)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./... -run 'TestParseLikePattern|TestLikePattern' -v 2>&1 | tail -20`
Expected: PASS, all five tests.

Then: `go test -race ./... -run TestLikePattern_ConcurrentMatchIsRaceFree`
Expected: PASS with no race report.

- [ ] **Step 6: Commit**

```bash
git add like_pattern.go like_pattern_test.go errors.go
git commit -m "feat(pattern): LIKE evaluates as a glob, not a regex

Tokenise the operand once at Prepare time and match with a greedy scan
carrying one backtrack point. No compile step, so no uncompilable-operand
failure mode; %/_ mean characters, so newlines are reachable; \\X is the
literal X, matching PostgreSQL and SQLite.

The matcher is immutable after construction — plan trees are evaluated
from multiple goroutines.

Refs: Cyoda/cyoda-go-spi#40"
```

---

### Task 2: `MATCHES_PATTERN` requires a bare compile, and one derivation for both operators

**Files:**
- Modify: `eval_leaf.go` (add `regexMatcher`, `compileMatchesPattern`, `invalidPatternError`, `compileLeafPattern`)
- Test: `eval_leaf_test.go`

**Interfaces:**
- Consumes: `patternMatcher`, `parseLikePattern`, `ErrInvalidPattern` (Task 1).
- Produces: `func compileLeafPattern(op FilterOp, value any) (patternMatcher, error)` — returns `(nil, nil)` for any operator that compiles no pattern. Task 3 wires it into `ExpandLeaf`; Task 4 exports it.

- [ ] **Step 1: Write the failing test**

Add to `eval_leaf_test.go` (`package spi`):

```go
func TestCompileMatchesPattern_AnchorEscapeRejected(t *testing.T) {
	// anchor() is string concatenation, so a body with a net-unmatched ')'
	// escapes the group: ")|(" becomes \A(?:)|()\z, an alternation whose
	// first branch matches the empty string at position 0 — it matches EVERY
	// stored value. These must be rejected, not accepted.
	for _, operand := range []string{`)|(`, `)\z|(?:`, `)$|(`, `)x(`} {
		if _, err := compileLeafPattern(FilterMatchesRegex, operand); err == nil {
			t.Errorf("compileLeafPattern(MATCHES_PATTERN, %q) = nil error, want rejection", operand)
		}
	}
}

func TestCompileMatchesPattern_AcceptSet(t *testing.T) {
	// Rejected: compiles bare, fails anchored (\Q swallows the wrapper's )\z).
	if _, err := compileLeafPattern(FilterMatchesRegex, `\Q`); err == nil {
		t.Error(`compileLeafPattern(MATCHES_PATTERN, "\\Q") = nil error, want rejection`)
	}
	// Accepted: legitimate patterns are unaffected by the bare requirement.
	for _, operand := range []string{`a|b`, `^foo`, `A.*e`, ``} {
		if _, err := compileLeafPattern(FilterMatchesRegex, operand); err != nil {
			t.Errorf("compileLeafPattern(MATCHES_PATTERN, %q) = %v, want accepted", operand, err)
		}
	}
}

func TestCompileMatchesPattern_ErrorIsHonestAndCarriesNoInternals(t *testing.T) {
	_, err := compileLeafPattern(FilterMatchesRegex, `[`)
	if err == nil {
		t.Fatal(`compileLeafPattern(MATCHES_PATTERN, "[") = nil error, want rejection`)
	}
	if !errors.Is(err, ErrInvalidPattern) {
		t.Errorf("error %v does not wrap ErrInvalidPattern", err)
	}
	msg := err.Error()
	// The BARE diagnostic. Anchored, RE2 reports "invalid escape sequence"
	// about a \z the user never wrote.
	if !strings.Contains(msg, "missing closing ]") {
		t.Errorf("want the bare code %q, got %q", "missing closing ]", msg)
	}
	if strings.Contains(msg, `\A(?:`) {
		t.Errorf("error leaks the anchored form: %q", msg)
	}
	if strings.Contains(msg, `[`) {
		t.Errorf("error echoes the operand: %q", msg)
	}
}

func TestCompileLeafPattern_TypedNilHazard(t *testing.T) {
	// A non-pattern operator must yield an UNTYPED nil. A typed nil through
	// the interface is non-nil, and evalStringOp would call a method on it.
	for _, op := range []FilterOp{FilterEq, FilterContains, FilterIsNull, ""} {
		m, err := compileLeafPattern(op, "anything")
		if err != nil {
			t.Errorf("compileLeafPattern(%q) = %v, want nil error", op, err)
		}
		if m != nil {
			t.Errorf("compileLeafPattern(%q) returned a non-nil matcher %#v", op, m)
		}
	}
	// The error paths must do the same.
	if m, _ := compileLeafPattern(FilterLike, `a\`); m != nil {
		t.Errorf("rejected LIKE returned a non-nil matcher %#v", m)
	}
	if m, _ := compileLeafPattern(FilterMatchesRegex, `[`); m != nil {
		t.Errorf("rejected MATCHES_PATTERN returned a non-nil matcher %#v", m)
	}
}

func TestCompileLeafPattern_DerivesOperandLikeTheKernel(t *testing.T) {
	// Takes `any` and applies OperandString itself, so a caller cannot supply
	// a differently-derived operand. A nil operand is "" here, never "<nil>".
	m, err := compileLeafPattern(FilterLike, nil)
	if err != nil {
		t.Fatalf("compileLeafPattern(LIKE, nil) = %v", err)
	}
	if !m.matches("") {
		t.Error("nil operand should derive the empty pattern, which matches only \"\"")
	}
	if m.matches("<nil>") {
		t.Error(`nil operand derived "<nil>" instead of ""`)
	}
}
```

Ensure `eval_leaf_test.go` imports `errors` and `strings`.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./... -run 'TestCompileMatchesPattern|TestCompileLeafPattern' 2>&1 | head -20`
Expected: FAIL — `undefined: compileLeafPattern`.

- [ ] **Step 3: Implement**

**ADD the following to `eval_leaf.go`. Delete nothing in this task.** The old
LIKE-grammar block (`regexpSpecialChars`, `likeToRegex`, `hasEscapeRune`) stays
exactly where it is and keeps compiling — `ExpandLeaf` still calls it, and Task 3
is what rewires `ExpandLeaf` and removes the block, in one commit. Removing it
here would leave the package unbuildable at the end of this task, which the
Global Constraints forbid.

Keep the existing `anchor` function where it is; it now serves
`MATCHES_PATTERN` only. Append one sentence to its godoc recording why the
standalone check below exists:

```go
// It is string concatenation, so it is only sound for a body that parses
// STANDALONE — see compileMatchesPattern.
```

Then add, below `anchor`:

```go
// --- pattern derivation ----------------------------------------------------

type regexMatcher struct{ re *regexp.Regexp }

func (m regexMatcher) matches(s string) bool { return m.re.MatchString(s) }

// invalidPatternError reports a regex failure by its syntax CODE only. It must
// never carry syntax.Error.Expr, which echoes the anchored expression and the
// caller's operand into a client-facing 400.
func invalidPatternError(op FilterOp, err error) error {
	var se *syntax.Error
	if errors.As(err, &se) {
		return fmt.Errorf("%w: %s: %s", ErrInvalidPattern, op, se.Code)
	}
	return fmt.Errorf("%w: %s: operand is not a valid regular expression", ErrInvalidPattern, op)
}

// compileMatchesPattern requires the operand to parse STANDALONE as well as
// compile ANCHORED.
//
// Anchoring is concatenation, so a body with a net-unmatched ')' escapes the
// group: ")|(" becomes \A(?:)|()\z, an alternation whose first branch \A(?:)
// matches the empty string at position 0 — it matches every stored value.
// Requiring a standalone parse makes that family unrepresentable, because RE2
// rejects an unmatched ')' on its own. The two accept-sets then agree in the
// safe direction: nothing is accepted that matches more than it says.
//
// The standalone check is syntax.Parse, NOT a second regexp.Compile.
// regexp.Compile is syntax.Parse plus program construction, so the parse alone
// rejects exactly the same operands — and building a second program we would
// throw away would make Prepare compile twice per query, breaking
// TestPrepare_CompilesRegexExactlyOncePerQuery.
//
// The standalone parse is also the honest diagnostic. For "[", it reports
// "missing closing ]"; the anchored form reports "invalid escape sequence"
// about a \z the caller never wrote.
func compileMatchesPattern(operand string) (patternMatcher, error) {
	if _, err := syntax.Parse(operand, syntax.Perl); err != nil {
		return nil, invalidPatternError(FilterMatchesRegex, err)
	}
	re, err := compileRegex(anchor(operand))
	if err != nil {
		return nil, invalidPatternError(FilterMatchesRegex, err)
	}
	return regexMatcher{re: re}, nil
}

// compileLeafPattern is the SINGLE derivation of what a pattern operand means.
// Both the kernel (via ExpandLeaf) and validators (via ValidateLeafPattern)
// route through it, so a validator cannot accept what the kernel refuses.
//
// It takes `any` and applies OperandString itself: a caller cannot supply a
// differently-derived operand because it does not derive one.
//
// A nil matcher with a nil error means "this operator compiles no pattern".
// Both nils must be UNTYPED — a typed-nil matcher through the interface is
// non-nil and evalStringOp would call a method on it.
func compileLeafPattern(op FilterOp, value any) (patternMatcher, error) {
	switch op {
	case FilterLike:
		return parseLikePattern(OperandString(value))
	case FilterMatchesRegex:
		return compileMatchesPattern(OperandString(value))
	}
	return nil, nil
}
```

Add `"errors"` and `"regexp/syntax"` to `eval_leaf.go`'s imports. Keep
`"strings"` and `"regexp"` — `fold`, the still-present LIKE block and
`compileRegex` all use them.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... -run 'TestCompileMatchesPattern|TestCompileLeafPattern' -v 2>&1 | tail -20`
Expected: PASS, all five tests.

Then the whole module: `go build ./... && go vet ./... && go test ./...` — green.
It builds precisely because nothing was deleted: `ExpandLeaf` still uses the old
LIKE block, and the new functions sit beside it, unused by production code until
Task 3. `go vet` does not flag unused package-level functions, so this is clean.

- [ ] **Step 5: Commit**

```bash
git add eval_leaf.go eval_leaf_test.go
git commit -m "feat(pattern): one derivation; MATCHES_PATTERN must compile bare and anchored

anchor() is concatenation, so a body with a net-unmatched ')' escapes the
group: ')|(' becomes \\A(?:)|()\\z, whose first branch matches the empty
string at position 0 — it matches EVERY stored value. Requiring a standalone
compile makes that family unrepresentable and makes the validator and kernel
accept-sets agree in the safe direction.

The bare parse is also the honest diagnostic: '[' reports 'missing closing ]'
rather than the anchored 'invalid escape sequence' about a \\z nobody wrote.

Refs: Cyoda/cyoda-go-spi#38"
```

---

### Task 3: Wire it into the kernel and delete the translation

**Files:**
- Modify: `eval_leaf.go:103` (`Expansion`), `:140-147` (`ExpandLeaf`), `:497` (`evalStringOp`)
- Modify: `eval_leaf_test.go` (rename the grammar test), `prepared_filter_internal_test.go:18,29`

**Interfaces:**
- Consumes: `compileLeafPattern` (Task 2).
- Produces: `Expansion.strMatch patternMatcher` replacing `strRegex`. Nothing outside the package sees this — `strRegex` appears only in `eval_leaf.go` at `:103,142,146,497`.

- [ ] **Step 1: Write the failing test**

In `eval_leaf_test.go`, rename `TestLikeToRegex_Grammar` to `TestLike_Grammar` — **change nothing else about it**. All twelve rows hold under the new grammar; that is the regression guard proving the four published rules survive.

Then add, in the same file:

```go
// TestLike_SQLParity pins the seven probe rows from the spec's Why table
// against the SQL column. PostgreSQL 17 and SQLite agree on every one; today's
// kernel disagrees with SQL on five.
func TestLike_SQLParity(t *testing.T) {
	cases := []struct {
		pattern, in string
		want        bool // what PostgreSQL and SQLite return
	}{
		{`\d`, "7", false},
		{`\d`, "d", true},
		{`\d`, `\d`, false},
		{`\w`, "q", false},
		{`a\nb`, "a\nb", false}, // \n is a literal 'n', not a newline
		{`a\nb`, "anb", true},
		{"a_b", "a\nb", true}, // _ matches a newline
		{"%", "a\nb", true},   // % matches a newline
	}
	for _, c := range cases {
		exp, err := ExpandLeaf(FilterLike, c.pattern, nil, []DataType{String})
		if err != nil {
			t.Fatalf("ExpandLeaf(like %q) error: %v", c.pattern, err)
		}
		if got := EvalLeaf(exp, jsonStr(c.in)); got != c.want {
			t.Errorf("LIKE %q vs %q = %v, want %v (SQL)", c.pattern, c.in, got, c.want)
		}
	}
}

// TestLike_MalformedOperandNeverMatches pins Prepare's documented contract: a
// leaf whose operand cannot be expanded becomes a leaf that never matches. The
// 400 happens at the request boundary, via ValidateConditionPatterns — not here.
func TestLike_MalformedOperandNeverMatches(t *testing.T) {
	exp, err := ExpandLeaf(FilterLike, `a\`, nil, []DataType{String})
	if err != nil {
		t.Fatalf("ExpandLeaf should not surface the error, got %v", err)
	}
	for _, in := range []string{"a", `a\`, "ab", ""} {
		if EvalLeaf(exp, jsonStr(in)) {
			t.Errorf("malformed LIKE operand matched %q", in)
		}
	}
}

// TestValidatorAgreesWithKernel is the anti-drift guard: for every corpus
// operand, compileLeafPattern erroring must be exactly when the kernel ends up
// with no matcher.
func TestValidatorAgreesWithKernel(t *testing.T) {
	corpus := []struct {
		op      FilterOp
		operand string
	}{
		{FilterLike, `%`}, {FilterLike, `a\`}, {FilterLike, `\`}, {FilterLike, `\d`},
		{FilterLike, `a\\b`}, {FilterLike, ``},
		{FilterMatchesRegex, `A.*e`}, {FilterMatchesRegex, `\Q`}, {FilterMatchesRegex, `)|(`},
		{FilterMatchesRegex, `[`}, {FilterMatchesRegex, `a|b`}, {FilterMatchesRegex, ``},
	}
	for _, c := range corpus {
		_, valErr := compileLeafPattern(c.op, c.operand)
		exp, err := ExpandLeaf(c.op, c.operand, nil, []DataType{String})
		if err != nil {
			t.Fatalf("ExpandLeaf(%s, %q) error: %v", c.op, c.operand, err)
		}
		if (valErr != nil) != (exp.strMatch == nil) {
			t.Errorf("skew for (%s, %q): validator err=%v, kernel matcher nil=%v",
				c.op, c.operand, valErr, exp.strMatch == nil)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./... -run 'TestLike_|TestValidatorAgrees' 2>&1 | head -20`
Expected: FAIL — `exp.strMatch undefined`, and the LIKE rows still evaluate through the old translation.

- [ ] **Step 3: Wire the kernel**

In `eval_leaf.go`, change the `Expansion` field (`:103`):

```go
	// kindStringOp payload.
	strOperand string
	strMatch   patternMatcher // LIKE glob / MATCHES_PATTERN anchored regex; nil ⇒ never matches
```

Replace `ExpandLeaf`'s two compile arms (`:140-147`) with one call:

```go
		e := Expansion{kind: kindStringOp, op: op, strOperand: operand}
		// Swallowed deliberately: Prepare's contract is that a leaf whose
		// operand cannot be expanded becomes a leaf that never matches.
		// Callers wanting a rejection ask ValidateLeafPattern FIRST.
		if m, err := compileLeafPattern(op, operand); err == nil {
			e.strMatch = m
		}
		return e, nil
```

Change `evalStringOp` (`:497`):

```go
	case FilterLike, FilterMatchesRegex:
		return e.strMatch != nil && e.strMatch.matches(s)
```

- [ ] **Step 4: Delete the translation**

Now that `ExpandLeaf` no longer calls it, remove the whole LIKE-grammar block
from `eval_leaf.go`: the `// --- LIKE grammar (Cloud queryable/Like.java
prepareSpecialCharacters) ---` banner, `regexpSpecialChars`, `likeToRegex` and
`hasEscapeRune`. Keep `anchor`, `compileRegex` and `fold`.

- [ ] **Step 5: Fix the compile-once test**

In `prepared_filter_internal_test.go`, drop `FilterLike` from the loop at `:18` — LIKE no longer reaches `compileRegex`:

```go
	for _, op := range []FilterOp{FilterMatchesRegex} {
```

Delete the `if op == FilterLike { ... }` branch at `:29` entirely.

Do **not** weaken its two `calls != 1` assertions. `MATCHES_PATTERN` still
compiles exactly once per query: the standalone check is `syntax.Parse`, which
does not route through the `compileRegex` package var.

- [ ] **Step 6: Run the full package**

Run: `go build ./... && go vet ./... && go test ./... 2>&1 | tail -20`
Expected: PASS. `TestLike_Grammar`'s twelve rows pass **unedited** — if any fails, the matcher is wrong, not the test.

Then confirm the translation is gone:

```bash
grep -rn 'likeToRegex\|hasEscapeRune\|regexpSpecialChars' --include='*.go' . | wc -l   # expect 0
grep -rn 'anchor(' --include='*.go' . | grep -v '_test' | grep -v 'func anchor' | wc -l # expect 1
grep -rn '\\A(?:' --include='*.go' . | wc -l                                            # expect 1
```

- [ ] **Step 7: Commit**

```bash
git add eval_leaf.go eval_leaf_test.go prepared_filter_internal_test.go
git commit -m "refactor(pattern): kernel evaluates through one matcher; delete the LIKE translation

Expansion.strRegex becomes strMatch patternMatcher, ExpandLeaf's two compile
arms become one compileLeafPattern call, and likeToRegex/hasEscapeRune/
regexpSpecialChars are deleted. The twelve-row grammar corpus passes unedited
— it is the guard that the four published LIKE rules survived the rewrite.

LIKE no longer reaches compileRegex, so the compile-once-per-query test now
covers MATCHES_PATTERN only.

Refs: Cyoda/cyoda-go-spi#40, #38"
```

---

### Task 4: Export `ValidateLeafPattern` and `ValidateConditionPatterns`

**Files:**
- Modify: `eval_leaf.go` (add `ValidateLeafPattern`), `condition_filter.go` (add the walker, update `MaxConditionDepth` godoc at `:581-586`)
- Test: `condition_filter_test.go`, `eval_leaf_test.go`

**Interfaces:**
- Consumes: `compileLeafPattern` (Task 2), `MapOperator`, `MaxConditionDepth`, `predicate.Condition`.
- Produces: `func ValidateLeafPattern(op FilterOp, value any) error`; `func ValidateConditionPatterns(cond predicate.Condition) error`. cyoda-go#479 calls the walker.

- [ ] **Step 1: Write the failing test**

Add to `condition_filter_test.go` (`package spi_test`, so import as `spi.`):

```go
func TestValidateConditionPatterns(t *testing.T) {
	bad := &predicate.SimpleCondition{JsonPath: "$.name", OperatorType: "MATCHES_PATTERN", Value: `\Q`}
	good := &predicate.SimpleCondition{JsonPath: "$.name", OperatorType: "MATCHES_PATTERN", Value: `a|b`}
	badLike := &predicate.SimpleCondition{JsonPath: "$.name", OperatorType: "LIKE", Value: `a\`}

	if err := spi.ValidateConditionPatterns(good); err != nil {
		t.Errorf("valid pattern rejected: %v", err)
	}
	for name, cond := range map[string]predicate.Condition{"regex": bad, "like": badLike} {
		err := spi.ValidateConditionPatterns(cond)
		if err == nil {
			t.Errorf("%s: invalid pattern accepted", name)
			continue
		}
		if !errors.Is(err, spi.ErrInvalidPattern) {
			t.Errorf("%s: error %v does not wrap ErrInvalidPattern", name, err)
		}
		// Actionable against a large tree: the leaf is named.
		if !strings.Contains(err.Error(), "$.name") {
			t.Errorf("%s: error does not name the leaf: %v", name, err)
		}
	}

	// Nested: the walker recurses into groups.
	group := &predicate.GroupCondition{Operator: "AND", Conditions: []predicate.Condition{good, bad}}
	if err := spi.ValidateConditionPatterns(group); err == nil {
		t.Error("group containing an invalid pattern accepted")
	}

	// Lifecycle leaves are checked too, and named by Field.
	lc := &predicate.LifecycleCondition{Field: "state", OperatorType: "LIKE", Value: `x\`}
	err := spi.ValidateConditionPatterns(lc)
	if err == nil || !strings.Contains(err.Error(), "state") {
		t.Errorf("lifecycle leaf not checked or not named: %v", err)
	}

	// Arms that carry no operator, and nil.
	for name, cond := range map[string]predicate.Condition{
		"array":    &predicate.ArrayCondition{JsonPath: "$.tags", Values: []any{"a"}},
		"function": &predicate.FunctionCondition{},
	} {
		if err := spi.ValidateConditionPatterns(cond); err != nil {
			t.Errorf("%s condition should pass, got %v", name, err)
		}
	}
	if err := spi.ValidateConditionPatterns(nil); err != nil {
		t.Errorf("nil condition should pass, got %v", err)
	}

	// Non-pattern and unrecognised operators are not this function's job.
	for _, op := range []string{"EQUALS", "NOT_AN_OPERATOR"} {
		c := &predicate.SimpleCondition{JsonPath: "$.a", OperatorType: op, Value: `\Q`}
		if err := spi.ValidateConditionPatterns(c); err != nil {
			t.Errorf("operator %q should pass ValidateConditionPatterns, got %v", op, err)
		}
	}
}

func TestValidateConditionPatterns_DepthGuard(t *testing.T) {
	var cond predicate.Condition = &predicate.SimpleCondition{
		JsonPath: "$.a", OperatorType: "EQUALS", Value: "x",
	}
	for i := 0; i < spi.MaxConditionDepth+1; i++ {
		cond = &predicate.GroupCondition{Operator: "AND", Conditions: []predicate.Condition{cond}}
	}
	if err := spi.ValidateConditionPatterns(cond); err == nil {
		t.Error("depth guard did not fire")
	}
}

func TestValidateLeafPattern(t *testing.T) {
	if err := spi.ValidateLeafPattern(spi.FilterMatchesRegex, `)|(`); err == nil {
		t.Error("anchor-escape operand accepted")
	}
	if err := spi.ValidateLeafPattern(spi.FilterLike, `100%`); err != nil {
		t.Errorf("valid LIKE operand rejected: %v", err)
	}
	if err := spi.ValidateLeafPattern(spi.FilterEq, `\Q`); err != nil {
		t.Errorf("non-pattern operator should pass, got %v", err)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./... -run 'TestValidateConditionPatterns|TestValidateLeafPattern' 2>&1 | head -20`
Expected: FAIL — `undefined: spi.ValidateConditionPatterns`.

- [ ] **Step 3: Implement**

In `eval_leaf.go`, after `compileLeafPattern`:

```go
// ValidateLeafPattern reports whether value is usable as op's pattern operand,
// using the SAME derivation the kernel evaluates with. A validator calling this
// cannot accept an operand the kernel will refuse, or refuse one it accepts.
//
// Returns nil for every operator that carries no pattern, so a caller can pass
// any leaf without switching on the operator first.
//
// It covers pattern VALIDITY only. Passing it is not the same as having
// validated the condition — see [ValidateConditionOperators] for operator
// names. Errors wrap [ErrInvalidPattern] and carry neither the operand nor the
// anchored form.
func ValidateLeafPattern(op FilterOp, value any) error {
	_, err := compileLeafPattern(op, value)
	return err
}
```

In `condition_filter.go`, beside `ValidateConditionOperators`:

```go
// ValidateConditionPatterns walks cond and validates every pattern operand
// against the kernel's own derivation, so a caller can reject the whole request
// at the boundary rather than mid-translation or, worse, discover it as an
// empty result page.
//
// It mirrors [ValidateConditionOperators]'s recursion and shares its depth cap.
// The two are complements and neither implies the other: this one checks
// pattern OPERANDS and passes a misspelled operator (MapOperator returns the
// zero FilterOp, which compiles no pattern); that one checks operator NAMES and
// ignores operands. Call both.
//
// Errors wrap [ErrInvalidPattern] and name the offending leaf by jsonPath (or,
// for a lifecycle leaf, by field), which is what makes them actionable against
// a deep tree. They never carry the operand.
func ValidateConditionPatterns(cond predicate.Condition) error {
	return validatePatternsAtDepth(cond, 0)
}

func validatePatternsAtDepth(cond predicate.Condition, depth int) error {
	if cond == nil {
		return nil
	}
	if depth >= MaxConditionDepth {
		return fmt.Errorf("condition depth exceeded (max %d)", MaxConditionDepth)
	}
	switch c := cond.(type) {
	case *predicate.SimpleCondition:
		return checkPattern(MapOperator(c.OperatorType), c.Value, c.JsonPath)
	case *predicate.LifecycleCondition:
		return checkPattern(MapOperator(c.OperatorType), c.Value, c.Field)
	case *predicate.GroupCondition:
		for _, child := range c.Conditions {
			if err := validatePatternsAtDepth(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	case *predicate.ArrayCondition:
		// Positional values become equality leaves; no pattern operand.
		return nil
	default:
		// Including FunctionCondition, which carries no operator.
		return nil
	}
}

// checkPattern names the leaf without echoing its operand.
func checkPattern(op FilterOp, value any, location string) error {
	if err := ValidateLeafPattern(op, value); err != nil {
		return fmt.Errorf("%s: %w", location, err)
	}
	return nil
}
```

Update the `MaxConditionDepth` godoc (`:581-586`) — it names only one walker:

```go
// MaxConditionDepth caps recursion in [ValidateConditionOperators] and
// [ValidateConditionPatterns] to defend against stack exhaustion from a deeply
```

Finally, add a cross-reference to `ValidateConditionOperators`' godoc, which currently says the operand obligations are "deliberately not folded in":

```go
// Pattern operands are now covered by [ValidateConditionPatterns] — validity
// was blocked on reaching the kernel's derivation, not on the pattern-cost
// bound, which remains unsettled and out of scope.
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... -run 'TestValidate' -v 2>&1 | tail -20`
Expected: PASS.

Then the whole module: `go build ./... && go vet ./... && go test ./...`

- [ ] **Step 5: Commit**

```bash
git add eval_leaf.go condition_filter.go condition_filter_test.go eval_leaf_test.go
git commit -m "feat(pattern): export ValidateLeafPattern and ValidateConditionPatterns

One derivation, reachable. A validator can now ask the kernel what it will
accept instead of re-deriving it — which is how the two consumer validators
came to compile bare while the kernel compiled anchored.

Errors name the offending leaf by jsonPath so they are actionable against a
deep tree, and carry neither the operand nor the anchored form.

Refs: Cyoda/cyoda-go-spi#38, Cyoda/cyoda-go#479"
```

---

### Task 5: Delete `ErrScanBudgetExhausted`

**Files:**
- Modify: `errors.go:110-113`

**Interfaces:**
- Consumes: nothing. Produces: a deliberate compile break in both consumers.

- [ ] **Step 1: Confirm it is inert here**

Run: `grep -rn 'ErrScanBudgetExhausted' --include='*.go' .`
Expected: exactly one hit — the declaration at `errors.go:113`. Nothing returns, wraps or tests it. If anything else appears, stop: the spec's premise is wrong.

- [ ] **Step 2: Delete it**

Remove the declaration and its doc comment from `errors.go`:

```go
// ErrScanBudgetExhausted is returned by a Searcher or streaming aggregator
// whose residual (non-pushdown) scan examined more rows than its configured
// scan budget before completing. The engine maps it to a client-facing 400.
var ErrScanBudgetExhausted = errors.New("scan budget exhausted")
```

Deleted outright, not deprecated. `MAINTAINING.md:69-75` prefers a `// Deprecated:` window "where feasible"; here it is harmful. This is a contract sentinel — once cyoda-go#475 removes the engine mappings, a backend still returning it produces an opaque 500 instead of today's 400. A compile break is the safe failure and names the work precisely.

- [ ] **Step 3: Verify**

Run: `go build ./... && go test ./... 2>&1 | tail -5`
Expected: PASS.

Run: `grep -rn 'ErrScanBudgetExhausted' --include='*.go' . | wc -l`
Expected: `0`.

- [ ] **Step 4: Commit**

```bash
git add errors.go
git commit -m "feat(errors)!: remove ErrScanBudgetExhausted

Server-imposed scan budgets left the Searcher contract with the bounding
policy settled on Cyoda/cyoda-go#475. The sentinel was already inert here —
declared, never returned.

Deleted rather than deprecated on purpose: once the engine mappings go, a
backend still returning it produces an opaque 500 instead of today's 400, so
a compile break is the safe failure.

BREAKING: consumers referencing spi.ErrScanBudgetExhausted will not compile
until they remove their scan-budget paths (Cyoda/cyoda-go#475).

Refs: Cyoda/cyoda-go-spi#39"
```

---

### Task 6: Pin the grammar in `spitest` for every backend

**Files:**
- Create: `spitest/pattern.go`
- Modify: `spitest/searcher.go:84-87` (register the subtests)

**Interfaces:**
- Consumes: `spi.Searcher`, `Harness`, `runSubtest`, `tenantContext`, `withTx`, `seedSearcherEntities` — all existing in `spitest`.
- Produces: subtest keys `Searcher/Pattern/LikeGrammar` and `Searcher/Pattern/MalformedLike`, which a backend may name in `Harness.Skip`.

**Why this task exists:** there is currently **zero** conformance coverage of the LIKE or MATCHES_PATTERN grammar for any backend, which is why the commercial backend's divergent copy went unnoticed. A backend diverging from the others on the same contract is a defect, so the contract needs an artefact that catches it.

- [ ] **Step 1: Write the conformance case**

Create `spitest/pattern.go`:

```go
package spitest

import (
	"context"
	"testing"

	spi "github.com/cyoda-platform/cyoda-go-spi"
	"github.com/stretchr/testify/require"
)

const patternModel = "spitest_pattern"

// patternSeed maps an entity's "name" value to the label used in failures.
// The values are chosen so each grammar rule has both a match and a decoy.
var patternSeed = []string{
	"d",       // \d must match this
	"7",       // \d must NOT match this (it is not a digit class)
	`\d`,      // \d must NOT match this either
	"100%",    // \% matches the literal percent
	`a\b`,     // \\ matches the literal backslash
	"a\nb",    // % and _ must reach a newline
	"abc",
}

// testPatternLikeGrammar pins the LIKE grammar through the Searcher surface.
// LIKE is a glob: '%' is any run INCLUDING newlines, '_' is exactly one rune
// INCLUDING a newline, and '\X' is the literal X for any X. It is NOT a regex,
// and a backend translating it to one will fail these rows.
func testPatternLikeGrammar(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	searcher := seedPatternEntities(t, h, ctx)

	cases := []struct {
		name    string
		op      spi.FilterOp
		operand string
		want    []string // the "name" values that must match, in no order
	}{
		{"EscapedLetterIsLiteral", spi.FilterLike, `\d`, []string{"d"}},
		{"EscapedPercentIsLiteral", spi.FilterLike, `100\%`, []string{"100%"}},
		{"DoubledEscapeIsOneBackslash", spi.FilterLike, `a\\b`, []string{`a\b`}},
		{"AnyRunReachesNewline", spi.FilterLike, `a%b`, []string{"a\nb", `a\b`}},
		{"OneCharReachesNewline", spi.FilterLike, `a_b`, []string{"a\nb", `a\b`}},
		{"AnyRunMatchesEverything", spi.FilterLike, `%`, patternSeed},
		{"WholeStringAnchored", spi.FilterLike, `bc`, nil},
		{"CaseSensitive", spi.FilterLike, `ABC`, nil},
		// MATCHES_PATTERN is a real regex, and is whole-string anchored.
		{"RegexIsAnchored", spi.FilterMatchesRegex, `b`, nil},
		{"RegexWholeString", spi.FilterMatchesRegex, `a.b`, []string{"a\nb", `a\b`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := searchPatternNames(t, ctx, searcher, c.op, c.operand)
			require.ElementsMatch(t, c.want, got,
				"%s %q selected the wrong set", c.op, c.operand)
		})
	}
}

// testPatternMalformedLike pins what a backend does with the ONE malformed
// LIKE operand: a trailing unpaired escape.
//
// The contract is Prepare's: "a leaf whose operand cannot be expanded becomes a
// leaf that never matches". So Search returns NO error and NO rows. Rejecting
// it with a 400 is the request boundary's job, above the Searcher, and failing
// the search here instead is a divergence — that is exactly the split this case
// exists to catch.
func testPatternMalformedLike(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	searcher := seedPatternEntities(t, h, ctx)

	for _, operand := range []string{`a\`, `\`} {
		res, err := searcher.Search(ctx, spi.Filter{
			Op:       spi.FilterLike,
			Source:   spi.SourceData,
			Path:     "name",
			Value:    operand,
			Declared: []spi.DataType{spi.String},
		}, spi.SearchOptions{ModelName: patternModel, ModelVersion: "1", Limit: 1000})
		require.NoError(t, err, "malformed LIKE operand %q must not fail the search", operand)
		require.Empty(t, res, "malformed LIKE operand %q must match nothing", operand)
	}
}

func seedPatternEntities(t *testing.T, h Harness, ctx context.Context) spi.Searcher {
	t.Helper()
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		for _, name := range patternSeed {
			_, err := es.Save(txCtx, newEntity(t, patternModel, newID(), map[string]any{"name": name}))
			require.NoError(t, err)
		}
	})
	es, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	return es.(spi.Searcher)
}

func searchPatternNames(t *testing.T, ctx context.Context, s spi.Searcher, op spi.FilterOp, operand string) []string {
	t.Helper()
	res, err := s.Search(ctx, spi.Filter{
		Op:       op,
		Source:   spi.SourceData,
		Path:     "name",
		Value:    operand,
		Declared: []spi.DataType{spi.String},
	}, spi.SearchOptions{ModelName: patternModel, ModelVersion: "1", Limit: 1000})
	require.NoError(t, err)
	names := make([]string, 0, len(res))
	for _, e := range res {
		names = append(names, entityDataString(t, e, "name"))
	}
	return names
}
```

`spi.Entity.Data` is `[]byte` holding the marshalled payload (`spitest/helpers.go:24-35`), and no suite reads a field back out today, so add the accessor to `spitest/helpers.go`:

```go
// entityDataString reads a top-level string field out of a returned entity's
// marshalled payload.
func entityDataString(t *testing.T, e *spi.Entity, field string) string {
	t.Helper()
	r := gjson.GetBytes(e.Data, field)
	require.True(t, r.Exists(), "entity %s has no %q field", e.Meta.ID, field)
	return r.String()
}
```

Add `"github.com/tidwall/gjson"` to `spitest/helpers.go`'s imports — it is already a direct requirement of the module (`go.mod`).

- [ ] **Step 2: Register the subtests**

In `spitest/searcher.go`, after the existing `runSubtest` calls at `:84-87`:

```go
	runSubtest(t, h, tracker, "Pattern/LikeGrammar", testPatternLikeGrammar)
	runSubtest(t, h, tracker, "Pattern/MalformedLike", testPatternMalformedLike)
```

- [ ] **Step 3: Prove the case has teeth**

The conformance case must fail against the pre-fix grammar, or it pins nothing.
Verify it by reverting the matcher for one run:

```bash
git stash push like_pattern.go eval_leaf.go
go test ./spitest/... 2>&1 | tail -5   # expect: build failure or FAIL
git stash pop
go test ./spitest/... 2>&1 | tail -5   # expect: PASS
```

If the pre-fix run does not fail, the rows are not discriminating — the most
likely cause is that every seeded value happens to satisfy both grammars. Fix
the corpus before moving on.

- [ ] **Step 4: Run the suite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: PASS.

The in-tree backends (memory, sqlite, postgres) evaluate LIKE through the kernel, since `FilterLike` is absent from both query planners' `isPushable`, so all rows resolve residually and must pass. Verify in the cyoda-go worktree once its pin advances — **not now**, it will not compile against this SPI until step 2 of #516.

- [ ] **Step 5: Commit**

```bash
git add spitest/pattern.go spitest/searcher.go spitest/helpers.go
git commit -m "test(spitest): pin the LIKE and MATCHES_PATTERN grammar for every backend

There was zero conformance coverage of either grammar, which is how a second,
divergent LIKE->regex implementation in the commercial backend went unnoticed.
A backend diverging on the same contract is a defect, so the contract needs an
artefact that catches it.

Also pins the one malformed LIKE operand: a trailing unpaired escape returns
no rows and no error, per Prepare's contract. Rejecting it is the request
boundary's job; failing the search is a divergence.

Expect Cyoda/cyoda-go-cassandra#94 to need a Skip entry until it adopts
the kernel.

Refs: Cyoda/cyoda-go-spi#40"
```

---

### Task 7: CHANGELOG and consumer notification

**Files:**
- Modify: `CHANGELOG.md` (`[Unreleased]`)

**Interfaces:** none — documentation only.

- [ ] **Step 1: Write the entries**

In `CHANGELOG.md` under `## [Unreleased]`, into the existing `### Breaking`, `### Fixed` and `### Added` sections (create the latter two if absent, keeping section order consistent with the rest of the file):

```markdown
### Breaking

- **`LIKE`'s `\X` now means the literal `X`, for any `X`.** Previously a
  backslash before anything other than `%`, `_` or `\` was passed into a
  compiled regex verbatim, so `LIKE '\d'` matched any digit and `LIKE '\w'`
  matched any word character. `LIKE` is a glob, not a regex: `\d` now matches
  the single character `d`, which is what PostgreSQL and SQLite return.

  This is a behaviour change on input that is accepted today and stays
  accepted, and **no compile break warns of it**. A caller relying on the
  regex-class behaviour gets different rows.

- **`MATCHES_PATTERN` now requires the operand to compile standalone**, not
  merely when anchored. The kernel wraps the operand as `\A(?:` + operand +
  `)\z`, so an operand with a net-unmatched `)` escaped the group: `)|(`
  became an alternation whose first branch matched the empty string at
  position 0 — it matched every stored value. Such operands are now rejected
  by [`ValidateLeafPattern`] and never match when evaluated.

- **`ErrScanBudgetExhausted` is removed.** Server-imposed scan budgets left
  the `Searcher` contract; time bounding belongs to the caller and memory
  bounding is fixed by streaming. A backend returning it will not compile.
  Remove the scan-budget path rather than substituting another sentinel.

### Fixed

- **`LIKE`'s `%` and `_` now match newlines.** They compiled to `.*?` and `.`,
  which exclude `\n` in RE2, so `LIKE '%'` did not match every string and a
  multi-line value was unreachable. This contradicted the published grammar
  ("any sequence of characters") and both SQL engines.

- **A `LIKE` operand that could not be compiled no longer silently never
  matches.** `LIKE 'a\'`, `LIKE '\'` and friends produced an uncompilable
  regex whose error was swallowed, leaving a leaf that matched nothing and
  reported nothing. `LIKE` no longer compiles anything, and the one malformed
  pattern — a trailing unpaired escape — is reportable via the new validators.

### Added

- **`ValidateLeafPattern(op FilterOp, value any) error`** and
  **`ValidateConditionPatterns(cond predicate.Condition) error`** — validate
  pattern operands against the same derivation the kernel evaluates with, so a
  caller's boundary check cannot drift from what the kernel accepts. Errors
  wrap the new **`ErrInvalidPattern`** and carry neither the operand nor the
  anchored form.
```

- [ ] **Step 2: Verify the sentinel's history is retained**

Run: `grep -c 'ErrScanBudgetExhausted' CHANGELOG.md`
Expected: at least 1 — the removal note above, plus any historical entry. Historical entries are **not** rewritten.

- [ ] **Step 3: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs(changelog): LIKE grammar, narrowed MATCHES_PATTERN accept-set, sentinel removal

Split deliberately: the newline behaviour is a Fixed (today contradicts the
published grammar), while \\X-means-literal-X is Breaking — predicates.md is
silent on \\X for any X outside %, _ and \\, so it is a new rule on accepted
input with no compile break to warn of it.

Refs: Cyoda/cyoda-go-spi#38, #39, #40"
```

- [ ] **Step 4: Notify consumers before merging**

Per `MAINTAINING.md:73-75`, both `KNOWN_CONSUMERS.md` entries get a pre-merge notification linked from the PR:

- **cyoda-platform/cyoda-go** — already sequenced as steps 2 and 3 of Cyoda/cyoda-go#516. Link the PR on that issue.
- **cyoda-platform/cyoda-go-cassandra** — already filed as Cyoda/cyoda-go-cassandra#94. Link the PR there.

Do not tag `v0.8.4`: per `MAINTAINING.md` it is cut once every milestone SPI change has merged, and spi#32 is still outstanding. cyoda-go stays on a pseudo-version pin.

---

## Final verification

- [ ] `go build ./... && go vet ./... && go test ./...` — green
- [ ] `go test -race ./...` — green
- [ ] Consolidation greps, all scoped to `*.go`:

```bash
grep -rn 'likeToRegex\|hasEscapeRune\|regexpSpecialChars' --include='*.go' . | wc -l  # 0
grep -rn 'anchor(' --include='*.go' . | grep -v '_test' | grep -v 'func anchor' | wc -l # 1
grep -rn 'compileRegex(' --include='*.go' . | grep -v '_test' | grep -v 'var compileRegex' | wc -l # 1 (the anchored compile; the standalone check is syntax.Parse)
grep -rn 'A(?:' --include='*.go' . | grep -v '^\S*:[0-9]*:\s*//' | grep -v 'strings.Contains' | wc -l  # 1 CONSTRUCTION site (anchor's body). Bare occurrences are 4: that body, two explanatory comments, and eval_leaf_test.go's assertion that the error does NOT leak the anchored form — the last must NOT be removed to satisfy a count.
grep -rn 'ErrScanBudgetExhausted' --include='*.go' . | wc -l                          # 0
grep -rn 'strRegex' --include='*.go' . | wc -l                                        # 0
```

- [ ] `TestLike_Grammar`'s twelve rows pass **unedited**
- [ ] `MaxConditionDepth`'s godoc names both walkers
- [ ] No test in package `spi` calls `t.Parallel()` — check CALL sites, not substrings: `grep -rn '^\s*t\.Parallel()' --include='*.go' . | wc -l` is 0. A bare `t.Parallel()` grep returns 1, from a prescriptive comment in `prepared_filter_internal_test.go:15`.
- [ ] No tag pushed
