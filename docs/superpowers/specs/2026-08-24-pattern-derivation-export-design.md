# Pattern derivation: fix the LIKE grammar, export the rule, retire the scan-budget sentinel

Date: 2026-08-24
Repo: `cyoda-go-spi`
Branch: `feat/38-anchor-export`
Issues: [spi#40](https://github.com/Cyoda/cyoda-go-spi/issues/40),
[spi#38](https://github.com/Cyoda/cyoda-go-spi/issues/38),
[spi#39](https://github.com/Cyoda/cyoda-go-spi/issues/39)
Consumer sequence: step 1 of [Cyoda/cyoda-go#516](https://github.com/Cyoda/cyoda-go/issues/516)

## Why

Two pattern operators — `LIKE` and `MATCHES_PATTERN` — are compiled to a regex
by the kernel before evaluation. `ExpandLeaf` (`eval_leaf.go:140-147`) derives:

```
FilterLike         -> compileRegex(anchor(likeToRegex(operand)))
FilterMatchesRegex -> compileRegex(anchor(operand))
```

Three defects follow from that derivation being both **wrong** and
**unreachable**.

### 1. The LIKE grammar leaks RE2 escapes (spi#40)

A `LIKE` operand is a wildcard string in which only `%`, `_` and `\` are
special. `likeToRegex` (`eval_leaf.go:593`) escapes only the characters in
`regexpSpecialChars` (`eval_leaf.go:581` — `[](){}.*+?$^|#<>-=`). Backslash is
absent from that set, and `hasEscapeRune` is consulted only for `_`
(`:605`) and `%` (`:613`), so every other `\X` reaches the `default:` arm at
`:619` and is appended verbatim into the compiled regex.

Verified against the kernel at `f91541a`:

| operand | stored value | result |
|---|---|---|
| `\d` | `7` | **true** — matches any digit |
| `\d` | `d` | false |
| `\d` | `\d` | **false** — does not match itself |
| `\w` | `q` | **true** |
| `\s` | `" "` | **true** |
| `a\nb` | `a`+LF+`b` | **true** |
| `a\nb` | literal `a\nb` | false |

An over-selecting wrong answer on user-controlled input, on every backend.

Its under-selecting twin: `\` before nothing, or before a character whose
anchored form will not compile (`a\`, `\`, `\Q`, `\p{Foo}`), yields a regex that
fails to compile. `ExpandLeaf` swallows that error (`:141-143`), leaving
`strRegex` nil, and `evalStringOp` guards on `e.strRegex != nil` (`:497`) — so
the leaf silently never matches. Described consumer-side as
[cyoda-go#487](https://github.com/Cyoda/cyoda-go/issues/487) item 9.

### 2. The derivation is unreachable, so consumers re-derive it wrongly (spi#38)

`anchor` and `likeToRegex` are unexported. A consumer validating an operand
before execution cannot reach the form the kernel will compile, so it compiles
the operand **bare** while the kernel compiles it **anchored**. The accept-sets
differ in both directions:

| operand, `MATCHES_PATTERN` | bare | anchored | consequence |
|---|---|---|---|
| `)\|(` | fails | compiles | rejected at the boundary though the kernel accepts it |
| `\Q` | compiles | fails | accepted, then the leaf never matches |

cyoda-go has this defect in **two** places, each with its own copy of the walk
and its own bare compile:

- `internal/domain/search/regex_validate.go:53,93` — ad hoc search conditions
- `internal/domain/workflow/validate.go:294,318` — workflow criteria

Both are [cyoda-go#479](https://github.com/Cyoda/cyoda-go/issues/479). The
second file's own comment already says *"Don't hand-roll the wrapper in this
package."*

A third skew of the same family sits underneath: the kernel derives the operand
string with `OperandString(f.Value)` (`prepared_filter.go:81`, defined
`filter_match.go:52`), while both validators use `fmt.Sprintf("%v", value)`.
For a nil value the kernel compiles `""` and the validators compile `"<nil>"`.

### 3. `ErrScanBudgetExhausted` declares a contract we no longer want (spi#39)

The search bounding contract settled on
[cyoda-go#475](https://github.com/Cyoda/cyoda-go/issues/475) removes
server-imposed scan budgets from the `Searcher` contract entirely: time bounding
belongs to the caller, memory bounding is a defect to fix by streaming, and a
backend cannot know whether it was invoked by a direct or an async search. The
sentinel has no use inside this module — `errors.go:110-113` declares it and
nothing here returns, wraps or tests it.

## Design

### Ordering: fix, then export

spi#40 lands **before or with** spi#38 in the same change. Exporting the
derivation first would publish the broken grammar as public contract and make it
harder to change afterwards.

The fix also collapses a question the export would otherwise force. Once
`likeToRegex` is total, a `LIKE` operand can never fail to compile, so walking
`LIKE` in a validator is behaviourally a no-op — it can only ever return nil.
That means **#487 item 9 dissolves rather than being decided**: its contract
question ("should validating LIKE turn today's empty page into a 400?") has the
answer "neither — match correctly instead". Item 9 should be closed as overtaken
when this lands, not implemented.

### 1. `likeToRegex` becomes total

**The rule.** A backslash is an escape introducer for exactly `%`, `_` and `\`.
A backslash in any other position — before any other character, or at end of
string — is a literal backslash and must reach the compiled regex as `\\`.

Stated as the three cases, since the current code decides this in a `default:`
arm by lookback (`hasEscapeRune`) and getting the boundary wrong is how the
defect arose:

| input | consumed as | emitted | matches |
|---|---|---|---|
| `\%`, `\_` | escape + literal | `%`, `_` (escaped if a metachar) | that character |
| `\\` | escape + literal backslash | `\\` | one backslash |
| `\d`, `\Q`, trailing `\` | not an escape | `\\` then `d` / `Q` / nothing | literal backslash, then the character |

Consequences:

- `\d` compiles to a literal `\d` and matches the two-character string.
- Output is always a compilable regex, for every input including `a\` and `\`.
- `\%`, `\_`, `\\` keep their present meanings. In particular `a\\b` must still
  match `a\b` — it does so today only because two raw backslashes reach the
  regex and happen to form one escaped backslash, so this is the case most at
  risk from the fix. `TestLikeToRegex_Grammar` (`eval_leaf_test.go:322-350`)
  stays green **unchanged**, which is the regression guard.

Whether the implementation keeps the lookback form or switches to lookahead is
an implementation choice for the plan; the table above is the contract either
must satisfy.

### 2. One internal derivation

```go
func compileLeafPattern(op FilterOp, value any) (*regexp.Regexp, error) {
	operand := OperandString(value)
	switch op {
	case FilterLike:
		return compileRegex(anchor(likeToRegex(operand)))
	case FilterMatchesRegex:
		return compileRegex(anchor(operand))
	}
	return nil, nil
}
```

`ExpandLeaf`'s two compile lines are **replaced** by one call to it. It keeps
swallowing the error exactly as today, so evaluation behaviour is unchanged by
spi#38 (it changes only via spi#40's grammar fix). `Prepare`'s documented
contract — "a leaf whose operand cannot be expanded becomes a leaf that never
matches" (`prepared_filter.go:46-49`) — is untouched; this design gives callers
a way to ask *before* that point, it does not promote the swallow to a
rejection.

`compileRegex` stays a package var, so
`TestPrepare_CompilesRegexExactlyOncePerQuery`
(`prepared_filter_internal_test.go:17`) keeps proving compile-once-per-query.

Taking `any` rather than `string` and calling `OperandString` internally closes
defect 3 by construction: a caller cannot supply a differently-derived operand,
because it does not derive one.

`anchor` and `likeToRegex` stay **unexported**. Exporting the ingredients would
leave the composition — port-then-anchor, in that order, for LIKE only — as the
caller's job, and the composition is the half that is wrong today.

### 3. Two exported entry points

```go
// leaf primitive
func ValidateLeafPattern(op FilterOp, value any) error

// tree walker, mirroring ValidateConditionOperators
func ValidateConditionPatterns(cond predicate.Condition) error
```

`ValidateLeafPattern` is `_, err := compileLeafPattern(op, value); return err`.

`ValidateConditionPatterns` mirrors `validateOperatorsAtDepth`
(`condition_filter.go:648`) exactly: the same `MaxConditionDepth` guard, the same
arms (`SimpleCondition` / `LifecycleCondition` → check; `GroupCondition` →
recurse; `ArrayCondition` and `FunctionCondition` → nil, neither carries an
operator), resolving the operator through `MapOperator` so `LIKE` and
`MATCHES_PATTERN` are both covered without the caller switching on operator
names.

**This reverses a documented decision, deliberately.**
`ValidateConditionOperators`' godoc (`condition_filter.go:639-643`) states the
operand obligations are "deliberately not folded in", one of them pending "a
pattern-cost bound this module has not settled". That reasoning does not survive
spi#40: pattern *validity* is no longer the open question a cost bound was
waiting on, and the alternative is two consumer repos maintaining two walks
each. The cost bound remains unsettled and out of scope — this exports validity
only. The spi#38 godoc will say so.

**Known hole, documented not fixed.** `compileLeafPattern` returns `(nil, nil)`
for any op that compiles no pattern, which includes the zero `FilterOp` that
`MapOperator` returns for an unrecognised name (`condition_filter.go:751`). So
`ValidateConditionPatterns` silently passes a condition whose operator is
misspelled. That is `ValidateConditionOperators`' job, and its godoc already
carries the matching disclaimer — "Passing this function is not the same as
having validated the condition." Both new functions carry it too, and
cross-reference each other.

### 4. Delete `ErrScanBudgetExhausted`

Deleted outright, not deprecated for a release. `MAINTAINING.md:69-75` prefers a
`// Deprecated:` window "where feasible"; it is not, and the window would be
actively harmful. This is a contract sentinel, not a helper: after cyoda-go#475
removes the engine mappings, a backend still returning it produces an **opaque
500** instead of the 400 it produces today. A compile break is the safe failure
and names the work precisely.

## Behaviour changes

| Change | Visible how | Source |
|---|---|---|
| `LIKE` with `\X` (X not `%`/`_`/`\`) now matches the literal two characters | different rows match; no error | spi#40 |
| `LIKE` operands that failed to compile now compile and match literally | previously-empty results become correct results | spi#40 |
| `ErrScanBudgetExhausted` removed | consumer compile break | spi#39 |
| `ValidateLeafPattern`, `ValidateConditionPatterns` added | additive | spi#38 |

`MATCHES_PATTERN` evaluation is **unchanged**. No `Searcher`, store, or
conformance surface changes; `spitest` is untouched, so no backend needs a new
`Skip` entry.

## Test plan

Fold into existing files. No new test file is warranted.

| Scenario | Layer | Where |
|---|---|---|
| `\d`, `\w`, `\s`, `\n` match their literal 2-char forms | unit, behavioural | extend `TestLikeToRegex_Grammar`, `eval_leaf_test.go:322` |
| existing `%`, `_`, `\%`, `\\`, metachar cases still pass | unit | same table, **unchanged** — the regression guard |
| `likeToRegex` output always compiles (`a\`, `\`, `\Q`, `\p{Foo}`, `%\`) | unit | same file, new totality test |
| `ValidateLeafPattern(FilterMatchesRegex, ")\|(")` is nil | unit, exported | `eval_leaf_test.go` |
| `ValidateLeafPattern(FilterMatchesRegex, "\\Q")` is non-nil | unit, exported | `eval_leaf_test.go` |
| `ValidateLeafPattern(FilterLike, …)` is nil for every corpus operand | unit, exported | `eval_leaf_test.go` |
| nil value derives `""`, not `"<nil>"` | unit | `eval_leaf_test.go` |
| non-pattern ops, zero op, unrecognised op all return nil | unit | `eval_leaf_test.go` |
| validator agrees with kernel across the corpus | internal | `eval_leaf_test.go` (`package spi`, so `strRegex` is reachable) |
| walker: depth guard, group recursion, array/function arms, LIKE+MATCHES_PATTERN leaves | unit, exported only | `condition_filter_test.go` (`package spi_test`) |
| compile-once-per-query still proven | internal | `prepared_filter_internal_test.go:17`, unchanged |

The two named skew witnesses (`)|(`, `\Q`) are load-bearing: they are what makes
the corpus catch a future "simplification" back to a bare compile. An
`iff strRegex != nil` assertion alone is near-tautological once there is one
derivation, so the exported contract is pinned **by example** as well.

New tests must not call `t.Parallel()` — `ValidateLeafPattern` routes through the
`compileRegex` package var, and the swap in
`prepared_filter_internal_test.go:15-16` is a data race against parallel tests.

**No HTTP/gRPC error-code table.** `.claude/rules/gate-brainstorming.md` requires
one for API/gRPC changes; this module has no endpoints and no wire format. The
consumer-visible status-code consequences belong to cyoda-go#479 and #475.

## Consolidation contract

Verifiable exit checks, not intentions. Each is a grep with an expected count.

| Check | Expected |
|---|---|
| callers of `anchor(` | 1 — inside `compileLeafPattern` |
| callers of `likeToRegex(` | 1 — inside `compileLeafPattern` |
| `compileRegex(` outside its declaration | 1 — inside `compileLeafPattern` |
| `\A(?:` occurrences repo-wide | 1 — `eval_leaf.go`, the `anchor` body |
| `ErrScanBudgetExhausted` repo-wide | 0 in code and docs; CHANGELOG history retained |
| new test files added | 0 — all coverage folded into existing files |
| `ExpandLeaf`'s old compile lines | deleted, not left beside the new path |

**Sequenced-incomplete, not left-behind.** This change intentionally leaves both
consumers uncompilable until their pin bump: cyoda-go's sqlite budget
(`plugins/sqlite/searcher.go:131,299`) and the commercial backend's
`search/direct_*` still reference the sentinel. That is the #516 sequence —
cyoda-go#475 is step 2, the commercial backend follows at its next bump — and it
is tracked there, not here.

## Docs and coordination

- `CHANGELOG.md` `[Unreleased]`: `### Breaking` for the sentinel removal (with
  the caller-side-bounding migration note) and for the LIKE grammar change (no
  compile break warns of it, so it must be loud); `### Added` for the two new
  functions.
- Pre-merge notification to both `KNOWN_CONSUMERS.md` entries, linked from the
  PR, per `MAINTAINING.md:73-75`.
- **Gate 7**: the LIKE fix is a deliberate divergence from Cloud's
  `Like.prepareSpecialCharacters`, which has the identical hole. A
  `docs/cloud-parity/` entry is owed **in cyoda-go**, recording the new grammar
  and that Cloud follows. Tracked on #516.
- **No tag.** Per `MAINTAINING.md`, `v0.8.4` is cut only once every SPI change
  for that milestone has merged; spi#32 is still outstanding. cyoda-go stays on
  a pseudo-version pin.

## Out of scope

- Fixing either cyoda-go validator to call the new export — cyoda-go#479, step 3.
- Removing the sqlite scan budget, engine mappings, error code and help topic —
  cyoda-go#475, step 2.
- The pattern-cost bound `ValidateConditionOperators`' godoc refers to.
- Dead `spi.FilterLike` pushdown branches in cyoda-go's sqlite and postgres
  query planners (`query_planner.go:517` / `:581`), unreachable since LIKE left
  `isPushable`. A re-arming hazard given the grammar change; recorded on #516.
