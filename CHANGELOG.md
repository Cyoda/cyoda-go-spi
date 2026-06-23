# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to the deprecation policy documented in
[MAINTAINING.md](MAINTAINING.md#deprecation-policy).

For the rationale behind the absence of CHANGELOG entries before v0.7.1,
see the [Fixing forward](MAINTAINING.md#fixing-forward) section of
MAINTAINING.md.

## [Unreleased]

## [0.8.0] - 2026-06-23

> **v0.8.0 re-cut note:** an earlier v0.8.0 tag was created prematurely on
> 2026-06-13 at the tx-state-sentinels merge commit `c301c0e`, before the
> remaining v0.8.0-milestone SPI changes were merged. That tag was deleted
> from the remote and re-cut here at the completed milestone, preserving
> lock-step with the cyoda-go v0.8.0 release. This was a one-time controlled
> exception to the immutability rule in MAINTAINING.md, safe because the
> premature tag was never consumed outside the cyoda-platform org and the sole
> consumer (cyoda-go) tracked a pseudo-version during the interval. Consumers
> that fetched the premature tag should set
> `GOPRIVATE=github.com/Cyoda-platform/*` (or otherwise bypass sum.golang.org
> for cyoda-platform modules) and refresh.

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
