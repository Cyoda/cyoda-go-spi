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

- **The quantifier cannot be derived from the path alone.** `$.tags[*]` and
  `$.tags` compile to the *identical* gjson path (`convertJSONPath` drops a
  trailing `[*]` run, `match.go:73-79`); today only the runtime shape of the
  data distinguishes them. Emission must instead be driven by the **schema** —
  the fields map already records `IsArray` at the `[*]` key — so the node is
  decided at translation time, deterministically, for both spellings.
- **A schema-less search loses implicit quantification** under schema-driven
  emission, where the data-shape route quantifies today. Needs a stated answer.
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
