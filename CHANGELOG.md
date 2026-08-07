# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to the deprecation policy documented in
[MAINTAINING.md](MAINTAINING.md#deprecation-policy).

For the rationale behind the absence of CHANGELOG entries before v0.7.1,
see the [Fixing forward](MAINTAINING.md#fixing-forward) section of
MAINTAINING.md.

## [Unreleased]

### Added

- Search-filter translation relocated into the SPI, completing the v0.8.3
  type-core relocation: `ConditionToFilter` (with `FieldDescriptor`,
  `ClassifyType`, `ClassifyTypesFold`, `MetaField`, `ResolveMetaField`,
  `IsTemporalMetaField`, `MapOperator`, `NormalisePath`), plus the read-side
  model tree behind `FieldsMapFromSchema` (`ModelNode`, `NodeKind`,
  `UnmarshalModelNode`, and the node constructors/accessors).

  Two closed vocabularies ship with enumeration accessors, not just point
  lookups: `OperatorNames` alongside `MapOperator`/`LookupOperator`, and
  `MetaFieldNames` alongside `ResolveMetaField`. A caller needing the SET —
  to validate membership, or to render a "valid values are…" diagnostic —
  would otherwise keep a private copy, which is a silent drift surface
  because nothing compares the copies.

  `ValidateConditionOperators` walks a condition tree rejecting unrecognised
  operator names, so a self-executing backend does not write that recursion
  itself. `MaxConditionDepth` caps it.

  `ConditionToFilter` is the only supported way to build a `Filter` the
  leaf-comparison kernel evaluates correctly, and it previously lived in
  cyoda-go's `internal/domain/search`, unreachable from a plugin. A backend
  that self-executes a search — one receiving a serialized condition rather
  than a ready-made `Filter` — therefore had to ship a second evaluator,
  which then drifts and answers the same query differently. Everything the
  translator needs was already here (`predicate`, `Filter`, `DataType`,
  `OrderKind`); only `FieldDescriptor` had to move, and its `Types` field
  was already `[]DataType`.

  **`ConditionToFilter(cond, nil)` is not a safe degraded mode.** An empty
  declared type set does not degrade every leaf alike, because the kernel
  only consults declared types where it needs a type slot to compare in.
  The eight comparison and ordering leaves (`EQUALS`, `NOT_EQUAL`,
  `GREATER_THAN`, `GREATER_OR_EQUAL`, `LESS_THAN`, `LESS_OR_EQUAL`,
  `BETWEEN`, `BETWEEN_INCLUSIVE`) annihilate to false — `ExpandLeaf`
  engages no bucket, errors, and `evalLeafFilter` swallows that into a
  non-match. The other eighteen evaluate normally, having never needed a
  type: `IS_NULL`/`NOT_NULL` decide purely on whether the stored value is
  present and non-null despite the null operand, and the string family —
  `CONTAINS`, `NOT_CONTAINS`, `STARTS_WITH`, `NOT_STARTS_WITH`,
  `ENDS_WITH`, `NOT_ENDS_WITH`, `LIKE`, `MATCHES_PATTERN`, `IEQUALS`,
  `INOT_EQUAL`, `ICONTAINS`, `INOT_CONTAINS`, `ISTARTS_WITH`,
  `INOT_STARTS_WITH`, `IENDS_WITH`, `INOT_ENDS_WITH` — compares
  stringified forms. The negated and case-insensitive members are the easy
  ones to misjudge: `ICONTAINS` resembles a comparison but is not one.

  The resulting filter is therefore internally inconsistent rather than
  merely empty: under `AND` a dropped comparison removes rows that should
  have matched, and under `OR` a surviving string disjunct admits rows the
  failed comparison was meant to exclude. Both silently. Callers that
  cannot supply declared types should refuse the query rather than proceed.
  Meta leaves are unaffected; their types come from the static meta
  vocabulary.

  The FILTER and SORT classifications stay deliberately distinct:
  `ClassifyType` keeps temporal subtypes as `OrderTemporal`, while a sort
  path folding them onto `OrderText` composes via `ClassifyTypesFold`
  rather than by changing the filter classifier.

  **`ConditionToFilter` translates; it does not validate.** The engine is
  safe only because it runs a condition validator first, and a direct caller
  inherits four obligations, each of which fails silently — a wrong or empty
  result set, never an error. Unrecognised operator names translate to an
  anchored regex (`NOT_EQUALS`, a misspelling of `NOT_EQUAL`, inverts the
  intended polarity; an operand of `.*` matches every row). An object operand
  reaches the kernel and is compared as the literal text `map[a:1]`. A
  malformed `BETWEEN` arity leaves `Values` nil and the leaf no-matches. An
  uncompilable pattern leaves the compiled program nil and the leaf returns
  false. `ValidateConditionOperators` covers the first; the other three are
  documented on `ConditionToFilter` and remain the caller's.

  `LookupOperator` returns the zero `FilterOp` on an unrecognised name
  rather than the regex fallback, so `op, _ := LookupOperator(name)` cannot
  quietly hand back the very filter the function exists to prevent.

### Fixed

- `UnmarshalModelNode` rejects a JSON-null child node with an error. The
  equivalent decoder this was derived from dereferences the nil and panics,
  which is reachable from persisted bytes.

## [0.8.3] - 2026-07-26

> Recorded retroactively. The v0.8.3 release did not carry out
> [MAINTAINING.md](MAINTAINING.md)'s release step 2 (rename `[Unreleased]`
> to the version being cut), so this section's contents sat under
> `[Unreleased]` after the tag had already shipped. The entries are
> unchanged; only the heading is corrected. There is no `[0.8.2]` section —
> that release shipped with an empty `[Unreleased]` and its changes were
> never recorded; they are not reconstructed here rather than guessed at.

### Breaking

- `Searcher.Search` is bounded-or-fail. `SearchOptions.Limit > 0` is a cap
  on the matched set, not a page size: an implementation that matches more
  than `Limit` MUST return `ErrSearchResultLimitExceeded` and MUST NOT
  return a truncated prefix. Exactly-at-limit succeeds. `Limit <= 0` means
  unbounded and an implementation MUST NOT substitute a default of its own —
  the calling engine resolves the direct-search default before invoking.
- `MergePage` becomes `MergeBounded`: same k-way merge, but it raises
  `ErrSearchResultLimitExceeded` instead of truncating, and the `offset`
  parameter is gone.
- `SearchOptions.Offset` is removed. Direct search does not paginate — no
  transport exposes an offset, and async search paginates over its own
  persisted result-ID list instead.

**Migration.** A plugin that previously truncated at `Limit` must now fail
instead:

1. Change `spi.MergePage(next, adds, deleted, order, offset, limit)` to
   `spi.MergeBounded(next, adds, deleted, order, limit)` — it already
   returns `ErrSearchResultLimitExceeded` for you, so propagate its error
   rather than discarding it.
2. Find every other branch that slices a result down to `Limit` by hand
   (typically the non-transaction and point-in-time paths, which do not go
   through the merge) and make each raise `ErrSearchResultLimitExceeded`
   when the matched set is larger than `Limit`. Returning the prefix
   alongside the error still violates the contract — return no entities.
3. Drop every read of `opts.Offset`; there is no replacement, and no caller
   set a non-zero value.
4. Leave `Limit <= 0` alone: do not clamp it, and do not substitute a
   default. It is a deliberate request for the complete matched set.

The new `spitest` `Searcher` group asserts all of this and is the check that
a migration is complete.

### Added

- Follow-on-action attribution: `Principal` / `PrincipalKind`
  (`user`/`service`/`system`), `UserContext.Kind`,
  `TransactionState.Origin` (the tx's immutable attribution root) and
  `TransactionState.DeleteAttribution` (per-staged-delete
  attributed/executor pair, same OpMu/savepoint posture as `Deletes`),
  `ScheduledTask.ArmedBy` (arming principal at fire-time), and
  `EntityMeta.ChangeUserKind`/`ChangeExecutor` +
  `EntityVersion.AttributedKind`/`Executor`. `WithAmbientOrigin` /
  `GetAmbientOrigin` seed an origin for causal-chain roots with no
  transaction yet (the scheduled-fire case); `ResolveOrigin`
  (parent-tx > ambient > UserContext) and `AttributionFor`
  (attributed, executor stamp rule) are the shared, single-source
  implementations every backend must use.
- `spitest/transaction.go`: `Attribution/OriginCaptureAndJoin`,
  `Attribution/OriginAmbientRoot`, `Attribution/DeleteAttributionSavepoint`
  conformance subtests.
- `spitest/entity.go`: `Attribution/ExecutorRoundTrip` conformance subtest
  (covers a DELETED version's `Executor` being readable without
  dereferencing `Entity`).
- `scheduled_task_store_conformance.go`: `ArmedBy` round-trip through
  `Upsert`/`Get`, plus the legacy-row (zero `ArmedBy`) case.
- `spitest/searcher.go`: `Searcher/BoundedOrFail` and
  `Searcher/BoundedOrFail/InTx` conformance subtests holding every backend
  to the bounded-or-fail contract — over the limit fails, exactly at the
  limit succeeds, zero and negative limits are unbounded — on both the
  committed path and the in-transaction read-your-own-writes overlay.
  Backends whose `EntityStore` does not implement the optional `Searcher`
  interface skip the group automatically; no `Harness.Skip` entry is needed
  or wanted.

## [0.8.1] - 2026-06-23

> **There is no v0.8.0 release.** A v0.8.0 tag was created prematurely on
> 2026-06-13 at the tx-state-sentinels commit `c301c0e`, before the rest of the
> v0.8.x SPI surface was merged. It was fetched through proxy.golang.org, which
> permanently bound `v0.8.0` to that incomplete commit — a Go module version
> cannot be re-cut once the proxy/checksum-database has served it. Rather than
> ship a poisoned version behind a GOPRIVATE workaround, v0.8.0 is abandoned and
> **v0.8.1 is the canonical, complete v0.8.x SPI release**. It resolves cleanly
> through the public proxy with no special configuration. (See MAINTAINING.md:
> a module version is tagged exactly once, at the final commit, never re-cut.)
>
> v0.8.1 contains the full v0.8.x surface below — nothing was dropped.

### Added

- Transaction-state sentinel hierarchy: `ErrTxNotFound`,
  `ErrSavepointNotFound`, `ErrTxTerminated`, `ErrTxRolledBack`,
  `ErrTxAlreadyCommitted`, `ErrTxCommitInProgress`,
  `ErrTxTenantMismatch`. Backwards-compatible: `ErrTxNotFound` and
  `ErrSavepointNotFound` wrap `ErrNotFound`, so existing
  `errors.Is(err, ErrNotFound)` callers continue to match.
- Seven new `spitest/transaction.go` subtests asserting backend
  conformance to the sentinel contract.
- Added `Iterable` / `Iterator` / `IterateOptions` SPI for filter-aware streaming iteration over a model's entities. Used by cyoda-go's grouped-stats endpoint as the streaming-tally fallback when native GROUP BY pushdown isn't available.
- Added `GroupedAggregator` SPI for native GROUP BY pushdown plus `GroupExpr`, `AggregateOp`, `AggregateExpr`, `GroupedAggregationsOptions`, `GroupKeyEntry`, `GroupedAggregateBucket`. Plugins that can answer grouped-aggregation queries in one storage roundtrip implement this; those that decline a specific request shape signal fall-through via `ErrAggregationNotPushdownable`.
- Added sentinels `ErrGroupCardinalityExceeded`, `ErrAggregationNotPushdownable`.
- `TransitionSchedule` type + `TransitionDefinition.Schedule` field for
  the scheduled-transition shape carve-out (cyoda-go #259). The new
  type carries `DelayMs` (required, >0) and `TimeoutMs *int64`
  (optional; nil = no timeout, &0 = strictest, &N = drop if late > N
  ms). Runtime not yet wired — see cyoda-go #251 for full feature
  tracking.
- `ProcessorConfig.AsyncResult *bool` and
  `ProcessorConfig.CrossoverToAsyncMs *int64` for the async-result /
  crossover-timer configuration shape carve-out (cyoda-go #261). The
  fields are pointer-typed (omitempty) so the absent case round-trips
  byte-equivalent. Runtime not yet wired — see cyoda-go #223 for full
  feature tracking. Consuming engines that do not implement
  async-result semantics MUST reject non-default values at the
  configuration-import boundary rather than silently degrade.
- `Annotations json.RawMessage` field on `WorkflowDefinition`,
  `StateDefinition`, and `TransitionDefinition` for opaque,
  client-owned metadata. Stored and round-tripped; the engine does not
  validate or interpret the contents.

### Changed

- Document `ProcessorDefinition.Type` field as the execution-location
  axis (deferred from cyoda-go #250 per its spec §5.3, intentionally
  bundled with the first substantive SPI carve-out — that is cyoda-go
  #259).

### Notes for consumers

- Plugins should wrap the sentinels at every tx-state error site.
  The in-tree memory, sqlite, and postgres plugins in `cyoda-go`
  are migrated as part of the corresponding `cyoda-go v0.8.0`
  release.
- The `OpAfterRollback` subtest may be skipped on backends that
  delegate transaction state to an external engine — such backends
  surface mid-op rollback as `ErrConflict` rather than
  `ErrTxTerminated` (for example, the postgres plugin reports
  SQLSTATE `25P02` via `pgx.Tx`). See `ErrTxTerminated` godoc for
  details.
- The new `Iterable` and `GroupedAggregator` interfaces are optional via
  type assertion. Out-of-tree plugins MAY skip implementing them; cyoda-go's
  service layer returns 501 NOT_IMPLEMENTED_BY_BACKEND for the grouped-stats
  endpoint when neither is present. No code changes required to remain
  compatible.

## [0.7.1] - 2026-05-05

### Added

- `.github/workflows/ci.yml`: self-contained CI running `go vet`,
  `go build`, `go test`, race detector, and `golangci-lint`.
- `.github/workflows/codeql.yml`: weekly CodeQL analysis + on-PR.
- `.github/dependabot.yml`: weekly Dependabot updates for gomod and
  github-actions ecosystems.
- `.github/PULL_REQUEST_TEMPLATE.md`: PR template prompting CHANGELOG
  and KNOWN_CONSUMERS hygiene on public-symbol changes.
- `MAINTAINING.md`: release process, deprecation policy, and the
  fixing-forward statement establishing the new regime.
- `CHANGELOG.md`: this file.
- `KNOWN_CONSUMERS.md`: opt-in registry of projects depending on
  this module.
- `README.md`: Versioning & Compatibility section linking to the
  three documents above.
- `spitest/README.md`: third-party plugin authoring guide with a
  copy-pasteable conformance CI snippet.

### Changed

- Tags from this release forward are annotated and signed. Tags
  `v0.1.0` through `v0.7.0` remain lightweight per the
  fixing-forward rule.
