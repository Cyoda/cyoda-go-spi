# How other systems quantify over nested/array structures — prior art

Companion to `2026-08-27-array-wildcard-quantifier-research.md`. That document
measured **our** behaviour. This one deliberately sets aside what cyoda-go has
documented or shipped and asks what the field does.

Sections 1–2 are **measured** on this machine. Section 3 is **recalled
knowledge, not run**, and is labelled as such — it corroborates, it does not
carry the argument.

## 1. PostgreSQL 17.10 — SQL/JSON `jsonpath` (the SQL:2016 standard)

`$.items[*].sku <OP> "A"`:

| document | `== "A"` | `!= "A"` | `!(… == "A")` | `?(@.sku != "A")` | `?(!(@.sku == "A"))` | `@> {"items":[{"sku":"A"}]}` |
|---|---|---|---|---|---|---|
| `[{"sku":"A"},{"sku":"B"}]` | t | **t** | f | t | t | t |
| `[{"sku":"A"},{"sku":"A"}]` | t | f | f | f | f | t |
| `[{"sku":"B"},{"sku":"B"}]` | f | t | t | t | t | f |
| `[]` | f | f | **t** | f | f | f |
| `[{"sku":"A"},{"other":1}]` | t | f | f | **f** | **t** | t |
| `{"other":1}` (absent) | f | f | **t** | f | f | f |

Three findings.

**Infix `!=` over a wildcard is existential over the operator** — row 1 is `true`
for `["A","B"]`. This is *identical* to cyoda-go's current behaviour, including
vacuous-false on an empty array and on an absent field. Our status quo is not an
accident; it is what the SQL/JSON standard specifies.

**The complement is available, spelled differently.** `!($.items[*].sku == "A")`
gives "no element equals A", and it is vacuously **true** on both the empty array
and the absent field. SQL/JSON provides a path-level `!` operator, so both
readings are expressible and the author chooses.

**Filter negation is not the same as operator negation.** Row 5 splits:
`?(@.sku != "A")` is false but `?(!(@.sku == "A"))` is true. An element with no
`sku` makes the comparison unknown — dropped by the filter — while `!` of the
unknown-false `==` is true. Three-valued logic, and the two spellings are not
interchangeable.

### `lax` vs `strict` — the standard's name for data-shape routing

Non-wildcard path `$.tags` against array data:

| document | `lax $.tags == "A"` | `strict $.tags == "A"` | `lax ?(@ == "A")` | `strict ?(@ == "A")` |
|---|---|---|---|---|
| `{"tags":["A","B"]}` | **t** | NULL | t | **f** |
| `{"tags":["B","B"]}` | f | NULL | f | f |
| `{"tags":[]}` | f | NULL | f | f |
| `{"tags":"A"}` | t | t | t | t |

**`lax` mode — the default — automatically unwraps an array and iterates its
elements**, so a path with *no* wildcard still quantifies when the data is an
array. `strict` refuses and yields SQL `NULL` (unknown), not `false`.

This is precisely the "routes on the data's shape, not the path's" behaviour
measured in `internal/match`. It is a named, standard mode, and it is the
default. Notably `spi.PreparedFilter` resembles `strict` — but answers **false**
where the standard answers **unknown**.

### Relational unnest — the explicit quantifier

`jsonb_array_elements` + `EXISTS` / `NOT EXISTS`:

| document | `∃ =A` | `¬∃ =A` | `∃ ≠A` | `∀ ≠A` |
|---|---|---|---|---|
| `[{"sku":"A"},{"sku":"B"}]` | t | f | t | f |
| `[{"sku":"A"},{"sku":"A"}]` | t | f | f | t |
| `[{"sku":"B"},{"sku":"B"}]` | f | t | t | f |
| `[]` | f | **t** | f | **t** |
| absent | f | **t** | f | **t** |

All four combinations are directly expressible, and the universal ones are
**vacuously true** on the empty array and the absent field.

## 2. SQLite 3.51.0

`json_each` + `EXISTS` / `NOT EXISTS` reproduces the postgres unnest table
exactly, vacuous-true included. Two further facts:

- **`json_extract` does not auto-unwrap.** `json_extract('{"tags":["A","B"]}','$.tags')`
  returns the text `["A","B"]`, and `= 'A'` is **false** — SQLite is `strict`-like
  and, like `spi.PreparedFilter`, answers false rather than unknown.
- **SQLite has no wildcard at all.** `json_extract(…, '$.items[*].sku')` is a hard
  error: `bad JSON path`. Implicit quantification is not offered; `json_each` is
  the only route, so the quantifier is always explicit.

## 3. Not run here — recalled, for corroboration only

- **MongoDB** — equality against an array field is existential ("the array
  contains"). `$ne` is defined as the **complement**: `{"items.sku": {$ne: "A"}}`
  selects documents where *no* element is `"A"`. `$elemMatch` exists because the
  complement reading alone cannot express "some element satisfies all of these".
  So MongoDB sits where PostgreSQL's `!(…)` sits, but reached by defining the
  negative operator rather than by an explicit negation operator.
- **XPath 1.0 / 2.0, XQuery** — general comparisons are existential on *both*
  sides: `$seq != "A"` is true if any item differs, and `not($seq = "A")` is the
  complement. The same design as SQL/JSON, and the source of the widely-cited
  `!=` vs `not(=)` trap. XQuery added value comparisons (`ne`) that raise an
  error on a sequence longer than one, rather than quantifying silently.
- **Couchbase N1QL / SQL++** — `ANY x IN items SATISFIES … END`, plus `EVERY` and
  `ANY AND EVERY`. Fully explicit; `EVERY` is vacuously true on an empty array.
- **Neo4j Cypher** — `any()`, `all()`, `none()`, `single()` list predicates.
  Fully explicit.
- **Elasticsearch** — `nested` documents, where the placement of `must_not`
  relative to the `nested` query is exactly the existential/complement choice,
  and is a long-standing documented gotcha.
- **Oracle, MySQL, SQL Server** — SQL/JSON `JSON_EXISTS` with filter
  expressions; the complement is `NOT JSON_EXISTS(…)`. Same shape as §1.
- **Apache Cassandra CQL** — collection `CONTAINS` only: existential equality,
  with no negated form. Relevant because it is the commercial backend's engine.

## 4. The taxonomy, and where cyoda-go sits

Every system surveyed falls into one of three camps:

| camp | implicit rule | how the complement is spelled |
|---|---|---|
| **explicit quantifier** — SQLite `json_each`, PG unnest, N1QL, Cypher, SQL Server | none | choose `EXISTS` / `NOT EXISTS`, `ANY` / `EVERY`, `any()` / `none()` |
| **existential + negation operator** — SQL/JSON (PG, Oracle, MySQL), XPath/XQuery | `!=` is existential | `!(… == …)`, `not(… = …)`, `NOT JSON_EXISTS` |
| **negative operator IS the complement** — MongoDB | `$ne` is universal | `$elemMatch` recovers the existential reading |

**The invariant that holds across all three: both readings are always
expressible.** Not one surveyed system offers only the existential reading.

That is where cyoda-go is anomalous, and it is a sharper statement of the
problem than "which reading should `NOT_EQUAL` have":

- our implicit rule is existential-over-the-operator — camp 2, and standard-conformant;
- but camp 2's escape hatch is missing. `groupToFilter` (`condition_filter.go:282-296`)
  builds only `FilterAnd` / `FilterOr`; **there is no `NOT`**, at group level or
  anywhere else.

So "no element has sku A" is not merely awkward to write — it is **inexpressible**,
and every other system can write it. The defect is the missing complement, not
the existential default.

Two further alignments fall out of §1:

- **Vacuity.** Wherever a universal quantifier exists, it is vacuously **true** on
  an empty array and an absent field. If a complement form is added, that is the
  answer it should give — not the existential rule's `false`.
- **Absent vs unknown.** SQL/JSON distinguishes "no such element" (filter drops
  it) from "the negation of an unknown comparison" (true). Our kernel collapses
  both to `false`. Whichever complement mechanism is chosen has to state which of
  the two it means for an element missing the key.
