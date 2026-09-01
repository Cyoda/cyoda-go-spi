# What the condition evaluators do today, measured

Established 2026-08-30, before designing the `NOT` node (this repo's issue #46).
Every claim below was produced by **running** the code at
`99b0b81` (`fix(filter): bound a positional subscript to int32`), not by reading
it and inferring. The measurement files were throwaway and are not committed;
each section states what to re-run to reproduce it.

This document exists because the two issues that motivated this work, #32 and
#46, were written before `cyoda-go#538` landed, and #32 had already accumulated
four stale premises from earlier changes. Its purpose is to record the ground
truth once so the design is not written against a remembered system.

## 1. A wildcard leaf is already existential

`#32`'s headline claim — that an array-wildcard predicate cannot be expressed in
`spi.Filter` at all — **is false as of `cyoda-go#538`**. No quantifier node is
needed to ask "does some element satisfy P".

Against `{"items":[{"sku":"A","qty":1},{"sku":"B","qty":9}],"tags":["red","blue"]}`:

| Filter | Result |
|---|---|
| `items[*].sku EQ "A"` | true |
| `items[*].sku EQ "B"` | true |
| `items[*].sku EQ "Z"` | false |
| `tags[*] EQ "red"` | true |
| `tags[*] NE "red"` | true — existential over the operator |

The mechanism is `PreparedFilter.match`: it iterates `storedAll` (which is
`ResolvePath` for a data leaf) and returns true when any addressed value
satisfies the expansion. `prepared_filter.go`.

## 2. Two leaves over the same array are not correlated

An `AND` of two wildcard leaves is satisfied by **different elements**.

```
AND( items[*].sku EQ "A", items[*].qty EQ 9 )  =>  true
```

on the document above, where `sku:"A"` has `qty:1` and `qty:9` belongs to
`sku:"B"`. No single element satisfies both.

This is **not a defect**. `path-grammar.md` section 3 defines a leaf on a
multi-value path as holding when *some* addressed value satisfies it, and each
leaf is independent. PostgreSQL's SQL/JSON answers the same way for two separate
path expressions. Asking the correlated question (MongoDB's `$elemMatch`) is a
capability this system does not have; it is a missing feature, not a wrong
answer.

## 3. The group-operator mapping is lenient, and silently

`spi.groupToFilter` (`condition_filter.go:437-455`) maps any operator that is
not `"OR"` (matched case-insensitively) to `FilterAnd`. Measured through
`ConditionToFilter`:

| `GroupCondition.Operator` | Translated as | Error |
|---|---|---|
| `"AND"` | AND | none |
| `"OR"` | OR | none |
| `"NOT"` | **AND** | **none** |
| `"and"` | **AND** | none |
| `"xor"` | **AND** | none |

Unreachable from the HTTP boundary today: `internal/domain/search/operators.go:145`
rejects anything that is not exactly `"AND"` or `"OR"`, case-sensitively, and
`internal/match/prepared.go:294` errors on it. So the leniency is currently
masked by two stricter layers rather than being harmless in itself.

## 4. The published contract already advertises `NOT`

Three sources disagree:

| Source | Says |
|---|---|
| `cyoda-go/api/openapi.yaml`, `GroupConditionDto.operator` enum | `AND`, `OR`, **`NOT`** |
| `cyoda-go/cmd/cyoda/help/content/search.md:147` | "`NOT` is not supported" |
| the server | `400`, "unknown group operator" |

`git log -S` dates the enum value to `d1f6875`, the initial import from
cyoda-light-go — so it was inherited from the Cloud contract rather than added
speculatively here.

## 5. Two tree walks, one path resolver

| | `spi.Prepare` / `PreparedFilter.Match` | `internal/match.Prepare` / `Prepared.Match` |
|---|---|---|
| input | `spi.Filter` (closed `FilterOp` enum) | `predicate.Condition` (free-text operator names) |
| error channel | none, deliberately | yes |
| unknown operator name | not representable | error (`errUnsupportedOperator`) |
| operand expands to no declared type | leaf never matches | leaf never matches (`prepNever`) |
| path resolution | `spi.ResolvePath` | `spi.ResolvePath` |

The single resolver is `path-grammar.md` section 10, delivered by `cyoda-go#538`.
The two **tree walks** remain distinct and are held together by
`internal/match/prepared_equivalence_test.go`.

`internal/match.Prepare`'s callers: the search residual
(`internal/domain/search/service.go:782`), the conditional-delete residual
(`internal/domain/entity/service.go:1083`), the grouped-stats residual
(`internal/domain/entity/grouped_stats_service.go:295`) and the workflow
criterion engine (`internal/domain/workflow/engine.go:1059`). The workflow engine
supplies a **lazy** `FieldTypes` closure: it avoids loading the model when the
criterion carries no data leaf, and latches an infra error so a model-store
outage is reported as a server fault rather than a client one. A
`FunctionCondition` is intercepted at the criterion's top level
(`engine.go:1005`) and never reaches the walk.

## 6. "Did not match" conflates two states

Both evaluators answer `false` for a leaf that was evaluated and did not match,
**and** for a leaf that could not be evaluated at all. Measured causes of the
second kind:

- an operand that expands to no declared type (`ExpandLeaf` fails)
- a `Filter.Path` that fails to parse (`prepareNode` leaves `hops` nil)
- a `SourceData` leaf with an empty `Path` (guarded explicitly in `prepareNode`)

With no negation in the language this is sound: unevaluable and unmatched both
fail closed. It stops being sound the moment a `NOT` can invert `false` into
`true`, because the second kind would then match everything.

## 7. Vacuity, confirming `path-grammar.md` section 5

Against `{"empty":[]}` with `absent` genuinely absent:

| Filter | Result |
|---|---|
| `empty[*] EQ "x"` | false |
| `empty[*] IS_NULL` | false |
| `empty[*] NOT_NULL` | false |
| `absent[*] EQ "x"` | false |
| `absent EQ "x"` | false |
| `absent NE "x"` | false |

The two presence tests are not complements on a wildcard path, exactly as
section 5 requires. An absent field is a non-match for a negative operator too,
which is what makes `NOT(EQUALS)` and `NOT_EQUAL` different predicates.

## 8. Negative twins

Of the 26 canonical operator names (`condition_filter.go:677-686`), 22 have a
negative twin and four do not: `LIKE`, `MATCHES_PATTERN`, `BETWEEN`,
`BETWEEN_INCLUSIVE`. Consequence for a `NOT` node: `NONE(P)` is expressible for
all 26 as `NOT(P)` over a wildcard leaf, while `ALL(P)`, which needs the negated
element predicate as a leaf, is expressible only for the 22.

## How to reproduce

Write a `package spi_test` file in this module that builds `spi.Filter` values
directly and calls `spi.Prepare(f).Match([]byte(doc), spi.EntityMeta{})`, and for
the wire-level rows calls `spi.ConditionToFilter` with a `predicate.GroupCondition`.
Print rather than assert, run with `go test -run <Name> -v .`, then delete the
file. Note `predicate.SimpleCondition`'s field is `JsonPath`, not `JSONPath`, and
that `operatorType` is the canonical wire key with `operator` accepted as an alias.
