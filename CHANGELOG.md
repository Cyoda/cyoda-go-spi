# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to the deprecation policy documented in
[MAINTAINING.md](MAINTAINING.md#deprecation-policy).

For the rationale behind the absence of CHANGELOG entries before v0.7.1,
see the [Fixing forward](MAINTAINING.md#fixing-forward) section of
MAINTAINING.md.

## [Unreleased]

### Breaking

- **`Searcher.Search` requires `Limit >= 1`; `Limit <= 0` is now a contract
  violation.** Previously `Limit <= 0` meant "unbounded" and the
  implementation returned the complete matched set; it now MUST return an
  error instead. The unbounded mode is gone from `Search`.

  Migration: callers that passed `0` or a negative `Limit` to mean "give me
  everything" must move to the `Iterable` streaming surface — `Iterate`
  with a zero-value `Filter` yields every match with bounded memory,
  instead of asking `Search` for an unbounded materialized slice.

- **`spitest` subtest renames:**
  `Searcher/BoundedOrFail/ZeroLimitUnbounded` → `.../ZeroLimitRejected`,
  `Searcher/BoundedOrFail/NegativeLimitUnbounded` → `.../NegativeLimitRejected`.
  Both now assert a non-nil error and an empty result, matching the
  `Limit <= 0` contract-violation change above.

  Migration: a `Harness.Skip` entry keyed on either old name now fails the
  conformance run ("possible typo or stale entry") — rename the keys to
  match.

- **`IterateOptions` gains `OrderBy []OrderSpec` and `TrackingRead bool`.**
  Ordering — for both `Searcher.Search` and the new `Iterable.Iterate` — is
  per-engine canonical, not guaranteed identical across backends; see
  `OrderSpec`'s doc comment.

- **`MergeBounded` requires `limit >= 1`; `limit <= 0` is now a contract
  violation.** Previously `limit <= 0` meant "unbounded" and the helper
  drained and materialized the entire surviving sequence; it now returns
  `fmt.Errorf("MergeBounded: limit must be >= 1")` instead. There is no
  unbounded mode, matching the `Searcher.Search` change above.

  Migration: callers that passed `0` or a negative `limit` for "everything"
  must move to the new `MergeOrdered` streaming helper (below) driven off an
  `Iterable`-backed ordered pull-stream, instead of asking for an unbounded
  materialized slice.

- **`EntityStore.GetVersionHistory` is removed; replaced by `GetPage`,
  `GetVersionByTransaction`, and `GetVersionMetadata`.** The single
  whole-history method conflated three different callers — a paged listing
  of current entities, a lookup of the specific version a known transaction
  wrote, and a lightweight audit trail — into one API that always paid for
  full entity payloads and never bounded or windowed its result.

  - `GetPage(ctx, modelRef, limit, offset, asAt)` pages `modelRef`'s current
    entities in the engine's canonical per-engine entity-ID order (see
    `OrderSpec`'s doc comment). `limit >= 1 && offset >= 0` is required —
    either violation is a contract violation, not a substituted default.
    `asAt == nil` reads the live in-transaction overlay and unconditionally
    records the page in the transaction's read-set; `asAt != nil` reads
    committed-only state as of that instant.
  - `GetVersionByTransaction(ctx, entityID, txID)` returns the earliest
    version of `entityID` written by transaction `txID`. DELETED tombstones
    never match (they carry no entity payload), and an empty `txID` never
    matches a stored-empty `TransactionID` — both return `ErrNotFound`.
  - `GetVersionMetadata(ctx, entityID, opts)` returns `entityID`'s version
    metadata (no entity payload) newest-first, tie-break `Version DESC`,
    windowed by `opts.From`/`opts.Until` (inclusive; nil side unbounded) and
    capped by `opts.Limit` (`0` means all — deliberately unbounded, since the
    result is one entity's own history, never a model-wide scan). The new
    `EntityVersionMeta` DTO carries `Deleted`, canonically derived from
    `ChangeType == "DELETED"`, true only on the tombstone row; `Version` is
    populated on every row including the tombstone.

  Migration: a caller that listed current entities uses `GetPage`; a caller
  that had a transaction ID and wanted that transaction's write uses
  `GetVersionByTransaction`; a caller that wanted the audit trail (who, when,
  what changed) without paying for full payloads uses `GetVersionMetadata`.
  There is no direct replacement for "give me every full-payload version at
  once" — that shape was the unbounded scan `GetVersionHistory` never
  bounded; page through `GetVersionMetadata` for metadata and fetch specific
  payloads via `GetVersionByTransaction` or `GetAsAt` as needed.

- **`spitest` subtest renames:** `Entity/GetVersionHistory/Ordering` →
  `Entity/GetVersionMetadata/Ordering`, now asserting `GetVersionMetadata`'s
  newest-first / `Version DESC` tie-break / tombstone-only-`Deleted`
  contract instead of `GetVersionHistory`'s. New subtests:
  `Entity/GetPage/OrderAndBounds`, `Entity/GetPage/AsAtSnapshot`,
  `Entity/GetVersionByTransaction/EarliestWins`,
  `Entity/GetVersionByTransaction/DeletedNeverMatches`,
  `Entity/GetVersionByTransaction/EmptyTxID`.

  Migration: a `Harness.Skip` entry keyed on `Entity/GetVersionHistory/*`
  now fails the conformance run ("possible typo or stale entry") — rename
  to `Entity/GetVersionMetadata/Ordering`, and add entries for the new
  `GetPage`/`GetVersionByTransaction` subtests if the backend needs to skip
  them.

- **`AsyncSearchStore.Cancel` takes a caller-supplied `finishTime`.**
  `Cancel(ctx context.Context, jobID string) error` is now
  `Cancel(ctx context.Context, jobID string, finishTime time.Time) error`.

  Migration: pass the cancellation instant; stores must stamp it on the
  transition and must not overwrite it on an idempotent re-cancel.

- **`AsyncSearchStore` write methods are epoch-fenced; terminal jobs are
  write-once.** `SearchJob` gains `HeartbeatTime *time.Time` (liveness
  stamp; nil means never stamped, staleness measured from `CreateTime`) and
  `Epoch int64` (claim/attempt counter; `CreateJob` always persists `1`
  regardless of the input job's value). `UpdateJobStatus` and `SaveResults`
  each gain an `epoch int64` parameter, and the new `Heartbeat` method takes
  one too; every one of the three MUST return `ErrStaleClaim` when the
  caller's epoch does not match the job's current `Epoch`, and MUST return
  `ErrAlreadyTerminal` against a job already `SUCCESSFUL`/`FAILED`/
  `CANCELLED` — `Cancel` remains the sole idempotent-nil exception,
  unaffected by this change.

  Old: `UpdateJobStatus(ctx context.Context, jobID string, status string, resultCount int, errMsg string, finishTime time.Time, calcTimeMs int64) error`
  New: `UpdateJobStatus(ctx context.Context, jobID string, epoch int64, status string, resultCount int, errMsg string, finishTime time.Time, calcTimeMs int64) error`

  Old: `SaveResults(ctx context.Context, jobID string, entityIDs []string) error`
  New: `SaveResults(ctx context.Context, jobID string, epoch int64, entityIDs iter.Seq[string]) error`
  — the results parameter also changes from a materialized slice to a
  pull-stream.

  Migration: thread the epoch a caller was claimed under (`1` for a job's
  own `CreateJob`-assigned epoch, or whatever `ClaimStale` last returned)
  through every `UpdateJobStatus`/`SaveResults`/`Heartbeat` call; replace a
  `[]string` results slice at each `SaveResults` call site with
  `slices.Values(ids)` or any other `iter.Seq[string]` producer; treat
  `ErrStaleClaim` and `ErrAlreadyTerminal` as expected outcomes — a slower
  executor's write landing after a peer reclaimed or finished the job — not
  as bugs to suppress.

- **`AsyncSearchStore` gains `Heartbeat`, `ClaimStale`, and `ClearResults`;
  two new sentinels `ErrAlreadyTerminal` and `ErrStaleClaim`.**
  `Heartbeat(ctx, jobID, epoch) error` stamps liveness, fenced like the
  write methods above. `ClaimStale(ctx, staleAfter, limit) ([]*SearchJob,
  error)` atomically claims up to `limit` `RUNNING` jobs whose heartbeat (or
  `CreateTime` baseline, when never heartbeated) is older than `staleAfter`;
  claiming bumps `Epoch` and refreshes `HeartbeatTime` so concurrent
  claimers get disjoint sets, and a terminal job is never claimed.
  `ClearResults(ctx, jobID) error` idempotently deletes a job's persisted
  result IDs, so a reclaimed job's next `SaveResults` epoch starts clean
  rather than colliding with or being contaminated by the stale epoch's
  rows. `ClaimStale` is cross-tenant like `ReapExpired` — obtain it with a
  background/tenant-less context.

  Migration: implement all three methods on every `AsyncSearchStore`. A
  `SelfExecutingSearchStore` (whose `CreateJob` dispatches and persists
  results itself) MAY no-op `Heartbeat`/`ClaimStale`/`ClearResults` and
  reject `SaveResults`, since liveness and reclaim are meaningless when a
  store owns execution outright.

- **Three `AsyncSearchStore` job-record contract fixes are now normative,
  each with its own conformance subtest.** These are new requirements that
  will fail conformance on implementations that pass today:

  - `GetResultIDs(ctx, jobID, offset, limit)` MUST return an error — never
    panic, never silently clamp — when `offset < 0` or `limit < 1`.
    (`spitest`: `AsyncSearch/GetResultIDs/DegenerateInputs`.)
  - `UpdateJobStatus` against a `jobID` with no job row MUST return
    `ErrNotFound`, not a generic or nil error.
    (`spitest`: `AsyncSearch/UpdateStatus/MissingIsNotFound`.)
  - A zero-value `finishTime` passed to `UpdateJobStatus` MUST be stored as
    absent (`SearchJob.FinishTime == nil` on readback), never persisted as a
    real zero-value timestamp.
    (`spitest`: `AsyncSearch/UpdateStatus/ZeroFinishTimeAbsent`.)

  Migration: audit each of the three call sites — degenerate pagination
  input, an update against an already-deleted or never-created job, and a
  status update with no finish time set — and bring the implementation in
  line before upgrading; the corresponding `spitest` subtest will fail
  otherwise.

- **`MatchFilter`, `EvalLeafString` and `Expansion.Void` are removed.**
  Filter evaluation is now a prepare/execute split: build a `PreparedFilter` once
  per query with `Prepare(Filter)`, then call `Match(data, meta)` once per row.
  (The unexported `evalLeafFast` is deleted too, but it was never reachable from
  outside this module.)

  Migration:

  ```go
  // before — parsed the operand, bucketed types and compiled the regex per row
  for _, e := range rows {
      if spi.MatchFilter(f, e.Data, e.Meta) { … }
  }

  // after — all of that happens once, above the loop
  p := spi.Prepare(f)
  for _, e := range rows {
      if p.Match(e.Data, e.Meta) { … }
  }
  ```

  `Prepare` returns no error: a leaf whose operand cannot be expanded becomes a
  leaf that never matches, exactly as the per-row evaluator did. A `PreparedFilter`
  is immutable and safe to share across goroutines. The zero `PreparedFilter`, and
  `Prepare(Filter{})`, both match everything.

  A leaf-level `EvalLeafString` replacement is deliberately not provided: leaving
  one would let a caller keep compiling per row while only the tree walk was forced
  open. Use `ExpandLeaf` once and `EvalLeaf` per row if you need leaf-level control.

  `Expansion.Void` is removed with no replacement. It existed for the
  group-combining case (OR-drop / AND-annihilate), which `Prepare` now subsumes
  internally. Anyone needing an unsatisfiable-leaf signal of their own should
  raise it — it belongs as a `PreparedFilter`-level accessor, not on `Expansion`.

  No one-release `// Deprecated:` grace period: removal is the mechanism that
  forces each caller to re-site the preparation, and a shim would silently preserve
  the defect.

### Added

- **`MergeOrdered` helper.** A pure pull-stream merge of an already-ordered
  committed source with a sorted overlay (adds), excluding deleted ids: on
  an equal-ID collision the overlay wins and the committed duplicate is
  consumed without a second yield; an error from the committed source is
  propagated once already-fetched entities have been yielded and is sticky
  thereafter. Pairs with `Iterable.Iterate` the way `MergeBounded` pairs
  with `Searcher.Search`.

- **Conformance: `GetSubmitTime` now requires tenant isolation.** Two new
  `spitest` subtests: `TxStateErrors/TenantMismatchOnGetSubmitTime` (a caller
  from another tenant resolving a txID — in-flight or committed — must get an
  error wrapping `ErrTxTenantMismatch`, never the submit time or the
  transaction's lifecycle state) and `TxStateErrors/NotFoundOnGetSubmitTime`
  (a txID that exists in no tenant must wrap `ErrTxNotFound`). `GetSubmitTime`
  was the only tx-lifecycle method without the tenant gate every other method
  already enforces; backends that ignore the `ctx` parameter in their
  implementation will fail the new subtests until they add the check.

- **Conformance: 38 new `spitest` subtests are pure additions, not
  renames — no `Harness.Skip` key needs to change.** `AsyncSearch` gains 16
  (`Epoch/InitialisedToOne`, `Epoch/FencedWrites`, `Terminal/WriteOnce`,
  `Claim/StaleClaimed`, `Claim/FreshNotClaimed`, `Claim/NilHeartbeatBaseline`,
  `Claim/ConcurrentDisjoint`, `Claim/TerminalNeverClaimed`,
  `ClearResults/Idempotent`, `SaveResults/ChunkSeqContinuity`,
  `SaveResults/CtxCancelObserved`, `GetResultIDs/DegenerateInputs`,
  `GetResultIDs/NonTerminalPartial`, `UpdateStatus/MissingIsNotFound`,
  `UpdateStatus/ZeroFinishTimeAbsent`, `Heartbeat/Semantics`) covering the
  epoch-fenced job surface below, plus (`SaveResults/CtxCancelObserved`) the
  ctx-cancellation-mid-stream half of the job-record contract fixes above.
  `Entity` gains 11 (`GetPage/OrderAndBounds`, `GetPage/AsAtSnapshot`,
  `GetPage/InTxWithStagedDeletes`, `GetPage/InTxRecordsReadSet`,
  `GetVersionByTransaction/EarliestWins`,
  `GetVersionByTransaction/DeletedNeverMatches`,
  `GetVersionByTransaction/EmptyTxID`,
  `GetVersionByTransaction/UnrelatedTxID`,
  `GetVersionMetadata/EmptyWindowIsNotAnError`,
  `GetVersionMetadata/LimitCaps`, `GetVersionMetadata/UntilBound`) covering
  `GetPage`/`GetVersionByTransaction` above, plus three conformance-coverage
  gaps found post-hoc: `GetPage/InTxWithStagedDeletes` pins the merge of an
  ambient transaction's staged deletes with a bounded committed prefetch (a
  real Critical bug on one backend silently under-filled or emptied the
  page); `GetVersionMetadata/EmptyWindowIsNotAnError` pins that an empty
  `From`/`Until` window on an existing entity yields an empty slice and a
  nil error, never `ErrNotFound` (two backends had diverged on this); and
  `GetPage/InTxRecordsReadSet` pins `GetPage`'s unconditional, page-scoped
  read-set recording inside a transaction (see the `EntityStore.GetVersionHistory`
  removal entry's `GetPage` description above) — a backend implementing
  `GetPage` as a plain snapshot read with no read-set effect previously
  passed every existing subtest. A wholly new `Iterable` group (11
  subtests: `Unordered/YieldsAllMatches`, `Ordered/EntityID`,
  `Ordered/UserFieldWithTieBreak`, `Ordered/InTxErrors`,
  `Residual/AppliedInNext`, `Ctx/CancelObserved`, `Err/Sticky`,
  `Close/Idempotent`, `PIT/SnapshotVariant`, `Overlay/SnapshotAtOpen`,
  `TrackingRead/Gating`) exercises the optional `spi.Iterable` interface,
  auto-skipping via type assertion on a backend that doesn't implement it —
  the same pattern `Searcher` already uses, so it needs no `Harness.Skip`
  entry either. A backend passing today keeps passing untouched; one that
  doesn't yet implement the exercised surface sees new failures until it
  does.

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
  engages no bucket, errors, and the leaf-comparison kernel swallows that
  into a non-match. The other eighteen evaluate normally, having never needed a
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

  Three obligations do remain the caller's, and each fails silently — a
  wrong or empty result set, never an error. An object operand reaches the
  kernel and is compared as the literal text `map[a:1]`. A malformed
  `BETWEEN` arity leaves `Values` nil and the leaf no-matches. An
  uncompilable pattern leaves the compiled program nil and the leaf returns
  false. All three are documented on `ConditionToFilter`.

### Changed

- **`ConditionToFilter` now rejects an unrecognised `operatorType`** with the
  new `ErrUnknownOperator` sentinel, instead of mapping it to
  `FilterMatchesRegex`. `MapOperator` returns the zero `FilterOp` for such a
  name rather than the pattern operator.

  The old fallback was justified as forcing post-filtering: no backend pushes
  a pattern leaf down, so an unknown operator would "degrade" to in-memory
  evaluation. But not pushing down is not the same as not evaluating — the
  kernel compiles the operand as an anchored pattern and matches it. The leaf
  degraded to a *different predicate*, not a slower one. `NOT_EQUALS`, the
  obvious misspelling of `NOT_EQUAL`, became `^value$`, behaved as `EQUALS`,
  and returned exactly the rows the caller meant to exclude — silently.
  Forcing post-filtering is a performance decision; it was being used as a
  correctness fallback, which it never was.

  Rejecting is also what the function already does with every other invalid
  input (nil conditions, function conditions, unsupported condition types,
  non-pushdownable paths). The unknown operator was the one case it swallowed.

  No caller relied on the fallback: the in-memory matcher accepts exactly the
  same closed set, so there was no operator it made tolerable. Callers that
  validate first — the engine does — see no change.

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
