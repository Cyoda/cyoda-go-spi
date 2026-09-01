# Array-wildcard quantifier (spi#32) — research

Factual record established by **running** the code at `cyoda-go-spi@78ee5ab` and
`cyoda-go@0d053c7` (`release/v0.8.4`), not by reading it. Written before any
design, because spi#32 predates `cyoda-go#511` and three of its premises no
longer hold.

## 1. Today's quantifier rule, measured

Probe: `internal/match.Prepare` + `Match`, condition `$.items[*].sku <OP> "A"`.

| document | EQUALS | NOT_EQUAL | IS_NULL | NOT_NULL | CONTAINS | NOT_CONTAINS |
|---|---|---|---|---|---|---|
| `[{"sku":"A"},{"sku":"B"}]` | true | **true** | false | true | true | **true** |
| `[{"sku":"A"},{"sku":"A"}]` | true | false | false | true | true | false |
| `[{"sku":"B"},{"sku":"B"}]` | false | true | false | true | false | true |
| `[]` (empty) | false | false | false | false | false | false |
| `[{"sku":"A"},{"other":1}]` | true | false | false | true | true | false |
| `{"other":1}` (no `items`) | false | false | true | false | false | false |
| `[{"sku":"A"},{"sku":null}]` | true | false | true | true | true | false |

**The rule is existential over the operator, for every operator.** Row 1 is the
case spi#32 names: `["A","B"] NOT_EQUAL "A"` is **true**, because element `B`
differs. `EQUALS "A"` is also true, so the two contradictory predicates both
hold on the same entity.

Two properties the issue does not state:

- **An empty array is false for every operator**, negatives and both presence
  tests included. `IS_NULL` and `NOT_NULL` are therefore both false on `[]` —
  they are not complements.
- **Elements missing the key are dropped before the quantifier sees them**
  (row 5): gjson's `#` projection omits them, so `NOT_EQUAL "A"` is false even
  though one element has no `sku` at all. This matches spi#32.

## 2. Three spi#32 premises are stale — `#511` overtook them

**(a) "Nested wildcards are broken today … `$.a[*].b[*].c` is always false."**
False. `convertJSONPath` emits `a.#.b.#.c|@flatten` and the probe matches:

```
gjsonPath="a.#.b.#.c|@flatten"  match=true
```

`internal/match/match.go:60-101` adds one `|@flatten` per array level beyond the
first, exactly to fix this. Shipped in `#511`.

**(b) "Nothing in Cloud's reference documentation, the help topics, or the API
specification states a quantifier rule. It is unwritten on both sides."**
False. All three now state it, as of `#511`:

- `docs/cloud-parity/trailing-wildcard-path.md` — "A leaf on such a path holds
  when **some** element satisfies it", and "**Existential, and vacuously false
  on an empty array.**"
- `cmd/cyoda/help/content/search.md:102` — "It is existential, so nothing
  matches an empty array — neither `IS_NULL` nor `NOT_NULL` holds on
  `{"tags": []}`."
- `api/openapi.yaml:9346-9362` — the accept rule for subscripted paths.

The rule is **published v0.8.4 contract that Cloud is obliged to follow**. It is
not unwritten; changing it is a breaking change to something this release ships.

What is genuinely absent is a **worked negation example**. "Holds when some
element satisfies it" does decide `NOT_EQUAL` by construction, but no document
says so, and the reading that surprises users is the one currently in force.

**(c) Acceptance box "Nested-wildcard evaluation fixed" is already done.**
See (a). `#478`'s scope note ("`/03` … evaluates false today on every backend
because the in-process evaluator iterates only the outer level") is stale for
the same reason, as is its file reference
`internal/domain/search/filter_translate.go:212-231` — that translation now
lives in the SPI at `condition_filter.go:425-459` (`stripDollarDot`).

## 3. What is actually missing — confirmed

`spi.Filter` cannot express a quantifier, and `Filter.Path`'s published grammar
forbids the syntax outright (`filter.go:96-107`: "no … asterisk, bracket").
The filter kernel has **no array iteration at all** — `grep -n 'IsArray\|ForEach'`
over the non-test SPI sources returns only schema-descriptor hits, and
`prepared_filter.go:138-143` resolves a leaf as a bare
`gjson.GetBytes(data, n.path)`.

So the stack runs **two evaluators with different array semantics**:

| | evaluator | array-valued data |
|---|---|---|
| fallback + workflow criteria | `internal/match.Prepared` | existential over elements |
| every plugin's residual re-check, grouped stats | `spi.Prepare(Filter)` | compares the operand against the whole array |

`plugins/{memory,sqlite,postgres}` all install the residual via `spi.Prepare`,
so the split is **condition-shape-dependent, not backend-dependent**: the same
predicate answers differently depending on whether the rest of the condition
happened to be translatable.

## 4. A live divergence this causes — reachable today

Probe: same condition through both evaluators, model declaring `$.tags[*]`
(array of `String`), path `$.tags` (**no** subscript, so it translates and is
pushed down).

| document | operator | `internal/match` | `spi.Filter` | |
|---|---|---|---|---|
| `{"tags":["A","B"]}` | EQUALS `"A"` | true | false | diverge |
| `{"tags":["A","B"]}` | NOT_EQUAL `"A"` | true | false | diverge |
| `{"tags":["A","B"]}` | CONTAINS `"A"` | true | false | diverge |
| `{"tags":["B","B"]}` | NOT_EQUAL `"A"` | true | false | diverge |
| **`{"tags":[]}`** | **NOT_NULL** | **false** | **true** | **diverge** |

The scalar-operand rows are **not reachable** through the search boundary:
`isKnownContainerPath` + `carriesScalarOperand`
(`internal/domain/search/condition_type_validate.go:180-209`) reject a scalar
comparison on a container path with `400`.

**The last row is reachable.** `carriesScalarOperand` returns false for
`IS_NULL`/`NOT_NULL` precisely so they "remain valid on a container path". So
`$.tags NOT_NULL` against `{"tags": []}`:

- pushed down (condition wholly translatable) → residual `spi.Prepare` → **true**
- fallback (anything in the condition untranslatable) → `internal/match` → **false**
- workflow criterion (never pushed down) → **false**

and the published contract in `trailing-wildcard-path.md` says the answer is
**false**. The pushdown path contradicts a shipped Cloud-facing contract, and a
search disagrees with a criterion over the same predicate.

This is the same root cause as spi#32 — the filter kernel cannot quantify — so
it belongs to this step rather than to a separate issue.

## 5. Consequences for the design

- **The quantifier IS derivable from the wire path — CORRECTED 2026-08-27.**
  An earlier draft of this section said it was not, and concluded emission must
  be schema-driven. That was wrong, and the error was a conflation: `$.tags[*]`
  and `$.tags` are distinct as *written*, and it is `convertJSONPath` that
  erases the difference when it compiles them to the identical gjson path
  (`match.go:73-79`). That erasure is an artefact of gjson compilation, not a
  property of the path. A node built from the wire path — which is what
  `ConditionToFilter` receives — distinguishes the two spellings exactly.
  Emission is therefore path-driven, and consulting the schema to decide it
  would introduce a second source of truth that can disagree with what the
  caller wrote.
- **There is no such thing as a schema-less search — RETRACTED 2026-08-27.**
  The earlier draft raised one as an open question. It cannot happen: every
  entity is bound to a model, `validateOrExtend` extends that model's schema
  additively on every write, and `loadFieldsMap` derives the fields map from
  the model store on every search. A path that resolves against stored data is
  in the fields map by construction. The question was invented, and nothing in
  the design should answer it.
- **The schema's role is VALIDATION, not emission.** The two spellings are
  different assertions about the model — `$.items[*].sku` asserts `items` is an
  array (or polymorphic with an array member), `$.items.sku` asserts `items` is
  an object — and a query whose path shape contradicts the model is an error,
  not something to interpret flexibly. `findUnknownPaths` already enforces
  exactly this, because the fields-map keys carry the `[*]` hops; what it lacks
  is that it does not always run (see §7).
- **Positional subscripts must keep working.** `$.arr[0]` is a *different*
  contract (`docs/cloud-parity/positional-subscript-path.md`) and is not
  quantified; an `ArrayCondition` still translates to positional `tags.0` eq
  leaves (`condition_filter.go:309-344`).

## 6. Not verifiable here

Cloud dictionary scenarios `06-entity-delete/delete/03` and `/04`, cited by
spi#32 and `#478`, are in the `cyoda-cloud` repository, which is not checked out
on this machine. Their shape is taken from `#478`'s description
(`/03` = `$.kids[*].kids[*].name EQUALS grandSon`) and not independently
confirmed — but note that per §2(a) a nested wildcard **already evaluates
correctly**, so `/03` is expected to pass today rather than fail.

## 7. Path validation does not always run — found 2026-08-27

Read from source at `cyoda-go@eb13ff9` (`release/v0.8.4`). Each of these is a
path on which a search returns a result set having validated nothing, or having
translated against an empty fields map. A failing test is owed for each before
any fix.

`SearchService.validateConditionPaths` (`internal/domain/search/service.go`)
returns `nil` — validation passed — on three failures that are not passes:

| line | condition | today |
|---|---|---|
| 1562-1569 | `s.factory.ModelStore(ctx)` errors | `slog.Debug` + `return nil` |
| 1571-1582 | `loadFieldsMap` errors | `slog.Debug` + `return nil` |
| 1583-1586 | `fields == nil` (descriptor carries no schema) | `return nil` |

The first two are dependency failures. Under
`.claude/rules/correctness-over-availability.md` an unavailable dependency that
a correct result requires fails the operation; it does not downgrade it. Both
comments justify the skip as letting "the matcher's own error path surface a
useful error", but the matcher has no field-path check — it answers an empty
page for an unknown path, which is indistinguishable from a legitimate empty
result.

Separately, all four `ConditionToFilter` call sites discard the fields-map
error and translate with whatever they got:

- `internal/domain/search/service.go:658` — `fields, _ := loadFieldsMap(...)`,
  commented `// best-effort; nil-tolerant`
- `internal/domain/search/service.go:1163`
- `internal/domain/entity/service.go:1099`
- `internal/domain/entity/grouped_stats_service.go:208`

`ConditionToFilter`'s own godoc says a nil fields map yields an INTERNALLY
INCONSISTENT filter — the eight comparison and ordering leaves annihilate to
false while the other eighteen evaluate normally — so under `AND` rows that
should have matched are dropped and under `OR` rows a failed comparison was
meant to exclude are admitted. Both silent. The godoc's instruction is explicit:
"Callers that cannot supply declared types should treat that as an error and
refuse the query." Four callers do the opposite.

This is not caused by the quantifier work and predates it. It is recorded here
because it was found while establishing what the schema is consulted for, and
because §5's corrected answer — the schema's role is validation — is only true
if the validation actually runs.
