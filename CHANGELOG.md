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

- **An array's length is not part of the model.** `FieldDescriptor.MaxWidth`,
  `ArrayBranch.MaxWidth` and `ModelNode.ObserveArrayWidth` are removed. The
  width was a discovery-time statistic the wire form never carried, so every
  tree decoded from persisted bytes reported zero and nothing a plugin could
  reach ever held a real value. An array branch declares its element and
  nothing else.

- **`EntityStore` has no whole-model read; `Search` and `Iterate` are
  required.** `GetAll` and `GetAllAsAt` are removed, and the optional
  `Searcher` and `Iterable` interfaces are folded into `EntityStore` as
  `Search` and `Iterate`. Every engine path that reads more than one entity
  already required one of them and refused a store without it; an optional
  interface every consumer requires only kept those refusal branches alive.
  There is no deprecation window: a deprecated `Iterable`/`Searcher` alias
  would keep `store.(spi.Iterable)` compiling and hide exactly the dead
  branches the change removes.

  **`CompareAndSave` requires a non-empty `expectedTxID`.** An empty one
  is now a contract violation the implementation MUST reject with an
  error — a caller bug, so it carries no sentinel, the treatment `Search`
  already gives `Limit <= 0`. It used to mean "expect no entity", and that
  reading was unsound: the compared field, `EntityMeta.TransactionID`, is
  empty for an absent entity, for a deleted one, AND for an entity written
  outside any transaction, so the empty string named all three at once and
  a "create only" call could silently overwrite an entity that exists —
  a fail-open on the one primitive whose job is to fail closed. The
  capability this removes is atomic insert-if-absent, which had no caller;
  unconditional create is `Save`'s job, and `Save` is likewise how a
  deleted entity is re-created. Should insert-if-absent be wanted later it
  gets its own explicit spelling rather than an overloaded sentinel.

  The comparison for a non-empty `expectedTxID` is unchanged: a literal
  string comparison against the entity's current `EntityMeta.TransactionID`
  as the caller's own transaction sees it, with no synonyms and no
  existence test. A missing or deleted entity carries the empty ID and so
  matches no non-empty expected ID — `CompareAndSave` can never create and
  never resurrect — and once a delete is staged in the caller's own
  transaction, nothing can compare-and-save against that entity for the
  rest of the transaction. `GetVersionByTransaction` already treated the
  empty transaction ID as never matchable; the two now agree rather than
  reading as opposite conventions for the same sentinel, and each
  cross-references the other.

  Doc-only contract clarifications ship alongside. `EntityStore.Search`'s
  godoc states the operational definition of its in-transaction result:
  identical to a committed-plus-buffer merge for the same transaction
  state, the merge `MergeBounded` computes. `EntityStore.Iterate` absorbs
  the full iteration semantics that previously sat as a detached comment in
  `iterable.go` — including the rules `spitest` enforces (a non-empty
  `OrderBy` with an ambient transaction MUST error; the engine-executed vs
  self-executing `OrderBy` split; no retry on transient driver errors),
  which `go doc` did not publish once the `Iterable` interface they were
  attached to was gone. `TransactionManager.Join`'s godoc now states that
  concurrent goroutines participate in the same transaction only through
  application-side serialisation, one operation at a time per transaction.

  **Two obligations `spitest` already enforces are now documented on the
  interface.** `GetPage` MUST return a non-nil, empty slice for a page
  with no rows — an empty model or an offset past the end — so the
  idiomatic `return nil, nil` is a contract violation. `CountByState` MUST
  return a non-nil map for every zero-count result, the in-transaction
  ones (after a `DeleteAll`, say) included. Neither was stated anywhere
  before; a backend returning nil was red with no rule to point at.

  **Migration:** delete `GetAll`/`GetAllAsAt` from your store (read a model
  with `GetPage` or `Iterate` with a zero-value filter); move your
  `Search`/`Iterate` methods onto the store type if they were on a separate
  one; drop `var _ spi.Searcher`/`spi.Iterable` assertions. `SearchOptions`,
  `IterateOptions` and `Iterator` are unchanged. In `CompareAndSave`,
  reject an empty `expectedTxID` up front with an error and delete the
  create-on-empty path — there is no longer a case in which an empty
  expected ID reaches the comparison.

  spitest: `GetAll/EmptyModel`, `GetAll/Population`, `GetAllAsAt`,
  `GetAllAsAt/CommittedOnlyInTx` and `TenantIsolation/GetAll` are gone
  (`GetPage/*` already pins those contracts); new cases
  `TenantIsolation/GetPage`, `Transaction/DeleteThenCompareAndSave` (a
  compare-and-save after a same-transaction delete MUST conflict),
  `Transaction/DeleteThenSave` (a save after a same-transaction delete
  wins: the entity is present after commit with the new payload; version
  history is backend-specific and not pinned),
  `Transaction/SaveThenCompareAndSave`, `TxStateErrors/OpAfterCommit`
  (every operation on a committed transaction's context, reads included,
  fails with `ErrTxAlreadyCommitted`, or `ErrTxNotFound` on backends that
  purge committed-tx state), `CompareAndSave/ExpectedIDIsLiteral`
  (a non-empty expected transaction ID is compared literally — it
  conflicts against a missing entity instead of creating it, and a stale
  one conflicts against an existing entity without writing),
  `CompareAndSave/EmptyExpectedIDRejected` (an empty expected transaction
  ID errors, inside a transaction and outside one, against a missing
  entity, an existing entity — left unchanged — and an entity with a
  same-transaction delete staged),
  `Entity/Count/InTxBufferShapes`, three cross-tenant cases for the
  multi-entity reads this change makes mandatory —
  `TenantIsolation/Search`, `TenantIsolation/Count` (both `Count` and
  `CountByState`) and `GroupedAggregator/TenantIsolation`, each carrying a
  positive control so an empty cross-tenant answer is evidence of scoping
  rather than of a seed that never landed — and the gated
  `GroupedAggregator/InTxRecordsNothing` suite (in-transaction grouped
  aggregation records nothing into the read-set). A `Skip` map keyed on a
  removed name fails the run.

  The `Searcher` and `Iterable` spitest GROUP names are deliberately
  unchanged even though the interfaces they were named for are gone:
  renaming them would invalidate every consumer's `Harness.Skip` keys for
  no contract benefit. `GroupedAggregator` is the remaining optional
  interface, and like every type-asserted group it must NOT be given a
  `Harness.Skip` entry — an unmatched `Skip` key fails the run.

- **A schema node holds the set of kinds it was observed as.** `ModelNode.Kind()`,
  `.Types()`, `.Element()` and `.Children()` are replaced by `.Scalar()`,
  `.Object()`, `.Array()` — each returning that branch or nil — plus `.Kinds()`,
  `.Branch()`, `.IsPolymorphic()`, `.Nullable()` and `.DeclaredTypes()`. A single
  label could only ever name one of three independent payload slots, so any
  reader that dispatched on it lost the others.

  Nullability is a flag rather than a `NULL` member of a type set, recorded only
  while the node carries no scalar branch — the collapse `TypeSet.Add` already
  applies when it drops `NULL` in the presence of a concrete type. A path
  observed only as `null` therefore declares no kind at all.

  The persisted form gains `"kinds"`. A node with at most one branch still
  writes `"kind"`, so every monomorphic node — nullable or not — serialises
  byte-identically to before and there is no migration. Decoding accepts both
  spellings.

  `ArrayInfo` is gone: `ObserveElement`, `Elements` and `IsUniform` had no
  caller, and the live half — one observed maximum width — is now
  `ArrayBranch.MaxWidth`, set through `ModelNode.ObserveArrayWidth`.

  The mutators an engine needs to build a tree (`AddScalarTypes`, `SetNullable`,
  `SetChild`, `SetElement`, `ObserveArrayWidth`) are exported. Deciding what a
  model's schema becomes is still the engine's job alone; that split is a
  statement of responsibility, not a lock, exactly as it already is for
  `TypeSet.Add`.

- **`ConditionToFilter` requires a condition's `jsonPath` to be JSON Path
  nomenclature: the `$.` leader is now mandatory.** This is a behaviour
  tightening on accepted input. Previously the leader was optional —
  `$.amount` and a bare `amount` were both accepted, the latter passed
  through unchanged. A bare identifier is not a path, and it is now rejected
  with an error wrapping `ErrInvalidFilterPath`. So are an empty path, an
  empty or trailing segment (`$..a`, `$.a.`), bracket-quoted property access
  (`$['x']`, `$.['x']`), and any character outside the segment set
  (`ALPHA / DIGIT / "_" / "-"`, ASCII only) — several of which the old
  character-only check let through as malformed `Filter.Path` values.

  **No longer true as of this same `[Unreleased]` window: a WELL-FORMED
  array-subscripted path (`$.tags[*].name`, `$.arr[0]`, `$.matrix[*][*]`) is
  valid JSON Path AND NOW TRANSLATES**, instead of failing with a plain
  unpushdownable error — see the "Filter.Path's grammar admits an array
  subscript" and "`ConditionToFilter` no longer refuses a well-formed
  subscripted path" entries below. The sentinel distinction itself is
  unchanged and still the point: a wrapped error is a client error (400), an
  unwrapped plain error is the caller's in-memory-evaluation fallback signal.
  What changed is which condition types land in the unwrapped class — it now
  holds only the ones `ConditionToFilter` genuinely cannot express (a
  `FunctionCondition`, an unrecognised condition type), not a well-formed
  subscripted path. A caller that treats every translation error as "fall
  back" will not observe either change.

  **Subscripts are now scanned rather than short-circuited.** The grammar
  previously stopped reading at the first `[` and accepted the remainder
  unread, so an unbalanced bracket (`$.a[`, `$.a]`), a slice (`$.a[0:2]`), a
  union (`$.a[0,1]`), a filter expression (`$.a[?(@.x)]`), a negative index
  (`$.a[-1]`), a double-quoted property access (`$.a["x"]`), a subscript with
  no field before it (`$.[0]`) and arbitrary trailing garbage
  (`$.a[0];DROP`, `$.a[0].xé`) all landed in the *unpushdownable* class —
  i.e. callers fell back to in-memory evaluation, which resolves none of
  them, and the request answered an empty page for a field that exists. The
  full path is now scanned:

      jsonPath  = "$." segment ( "." segment )*
      segment   = name subscript*
      name      = 1*( ALPHA / DIGIT / "_" / "-" )   ; ASCII only
      subscript = "[" ( "*" / 1*DIGIT ) "]"

  Anything outside it wraps `ErrInvalidFilterPath` (400). Chained and
  mid-path subscripts (`$.matrix[*][*]`, `$.orders[*].lines[*].sku`) are
  admitted, matching the `[*]` key convention `FieldsMapFromSchema` emits.

  Also unchanged: `Filter.Path`, the plugin-facing form this function emits,
  stays BARE (`amount`). The wire form requires the leader; the plugin-facing
  form forbids it. And metadata is unaffected: a `LifecycleCondition` names a
  member of the closed meta vocabulary directly and never goes through path
  translation. A data path spelled `$._meta.state` is an ordinary dotted path
  and is accepted as one.

  Migration: prefix condition `jsonPath` values with `$.`. `NormalisePath`
  does that canonicalisation, but it is a canonicaliser and not a licence —
  run it over paths you construct, not over untrusted input you meant to
  validate.

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
  by `ValidateLeafPattern` and never match when evaluated.

- **`ErrScanBudgetExhausted` is removed.** Server-imposed scan budgets left
  the `Searcher` contract; time bounding belongs to the caller and memory
  bounding is fixed by streaming. A backend returning it will not compile.
  Remove the scan-budget path rather than substituting another sentinel.

- **`Filter.Path`'s grammar admits an array subscript.** The wire and
  plugin-facing grammars are now the same shape minus the `"$."` leader:

      path      = segment ( "." segment )*
      segment   = name subscript*
      name      = 1*( ALPHA / DIGIT / "_" / "-" )   ; ASCII only
      subscript = "[" ( "*" / 1*DIGIT ) "]"          ; the digit run must fit an int32

  A bracket (`tags[0]`, `tags[*]`) is an array index. A dotted numeric
  segment (`tags.0`) is a field whose name is that digit string. **The two
  address different values, and a backend must not collapse them.** A field
  can be declared as both an object and an array branch at once, in which
  case `tags.0` and `tags[0]` are both valid statements about it and address
  different data. `tags[0]` was previously outside the grammar entirely — see
  the `spitest`/conformance entry below for what changes for an out-of-tree
  backend.

  A positional index's digit run must fit an `int32` — not Go's `int`
  (`int64` on every supported platform) — because `int32` is the
  intersection every in-tree backend can address; a run that overflows is
  rejected the same as any other malformed subscript, not truncated or
  wrapped. This grammar and `cyoda-go`'s wire `jsonPath` grammar
  (`docs/cloud-parity/path-grammar.md` section 2) are the same production
  minus the `"$."` leader, kept from drifting apart by `ParseFilterPath` and
  `IsArrayIndex` — see the `IsArrayIndex` entry below.

  Migration for a plugin author with a hand-rolled `Filter.Path` parser or
  renderer: accept and render the `subscript` production above. A backend
  that renders `tags[0]` and `tags.0` to the same underlying query — the same
  SQL column expression, the same document lookup — answers one of them
  wrongly and must stop doing so; render them distinctly (see
  `docs/cloud-parity/path-grammar.md` section 9's SQLite/PostgreSQL rendering
  table for the two-column pattern this repo's own backends use). A backend
  that already rejected every bracket outright must instead accept the two
  well-formed subscript forms and reject everything else the grammar excludes
  (a slice, a union, a filter expression, a negative or signed index, an
  index too large to fit an `int32`, an unbalanced or unmatched bracket, a
  chained subscript on a non-array).

- **`ConditionToFilter` no longer refuses a well-formed subscripted path; it
  translates it.** `$.tags[*].name`, `$.arr[0]` and `$.matrix[*][*]` used to
  be valid JSON Path but "unpushdownable" — a plain error that did not wrap
  `ErrInvalidFilterPath`, so a caller fell back to in-memory evaluation. They
  now translate to a `Filter` like any other well-formed path, because the
  kernel resolves a subscripted path directly (see the leaf-comparison-kernel
  entry below) rather than needing an in-memory fallback to serve it.

  The plain-error, does-not-wrap-`ErrInvalidFilterPath` class this function
  can still return now covers only condition types it cannot express at all —
  a `FunctionCondition`, and an unrecognised `predicate.Condition`
  implementation — not any well-formed path. A caller whose translate-failure
  handling assumed "the only plain error is an unpushdownable subscript" no
  longer holds; check what remains in that class before writing new fallback
  logic against it.

  Migration: a caller that special-cased array-subscripted paths as
  "translate fails, fall back to memory" can delete that special case —
  `ConditionToFilter` now serves them. A caller that inspected the error
  message for the string "not pushdownable" or similar to detect this case
  should instead check `errors.Is(err, spi.ErrInvalidFilterPath)`, which is
  unaffected by this change.

- **An `array` clause is rewritten into an `AND` of positional comparisons
  before any evaluator sees it, via the new `DesugarCondition`.**
  `ConditionToFilter` calls it first, so no evaluator — pushdown or
  in-memory — ever sees a `predicate.ArrayCondition` again; `DesugarCondition`
  is now the ONLY place the clause's positional semantics are defined.
  `arrayToFilter` and `arrayElementPath` are gone — deleted, not deprecated,
  because they encoded a second, competing definition of the same
  desugaring.

  **The clause's `jsonPath` must carry a trailing `[*]`.** A bare path
  (`$.tags`) addresses the array itself, not its elements, and cannot carry a
  positional test — `docs/cloud-parity/path-grammar.md` section 8. This
  repo's `DesugarCondition` is total and does not itself reject a bare
  `jsonPath` (it appends `[i]` rather than replacing a trailing `[*]`, so the
  malformed input still fails downstream at the ordinary path-grammar check);
  a caller enforcing the array-clause contract at its boundary must reject
  the bare form itself rather than relying on this function to.

  Migration: a caller building a `Filter` from an `ArrayCondition` by hand,
  or maintaining its own copy of the old `arrayToFilter`/`arrayElementPath`
  logic, must switch to `ConditionToFilter` (which now calls
  `DesugarCondition` for you) or to `DesugarCondition` directly if it needs
  the rewritten `predicate.Condition` tree rather than a `Filter`. There is
  no compatibility shim; the two removed functions will not compile.

- **The leaf-comparison kernel resolves a path by its syntax rather than the
  stored shape.** `preparedNode` (the `Searcher`/in-memory evaluation path)
  used to call `gjson.GetBytes` on the raw `Filter.Path`, which routes on
  what the *stored value* looks like: a bare path over an array matched
  existentially across its elements, and a `[*]` path reaching a scalar fell
  through to comparing the scalar directly. It now parses the path once
  (`ParseFilterPath`) and resolves it per row through the new `ResolvePath`,
  which addresses exactly what `docs/cloud-parity/path-grammar.md` section 3
  says: **a bare path never unwraps an array, a wildcard never wraps a
  scalar, and a positional path (`tags[0]`) addresses exactly one position.**
  A leaf now holds when SOME value the path addresses satisfies it.

  **Vacuity consequence that bites: a wildcard path never answers the
  array's own nullness.** `tags[*]` addresses elements and nothing else, so
  an empty array, an explicit `null`, and an absent field all present zero
  elements to it — `IS_NULL` and `NOT_NULL` both answer **false** for all
  three. On a wildcard path the two presence tests are therefore **not
  complements**; they are complements only where at least one element
  exists. Ask about the array itself with the bare path (`tags`), which
  separates the three states. A positional path (`tags[0]`) differs: it
  addresses exactly one position, which may be absent, so `tags[0] IS_NULL`
  holds over `[]` the way an ordinary absent field does. See
  `docs/cloud-parity/path-grammar.md` section 5 for the full vacuity table.

  Migration: a caller relying on a bare or wildcard path resolving by the
  stored value's shape — matching an array existentially through a bare
  path, or a scalar through a `[*]` path — must repoint the condition at the
  syntax that now carries that meaning. A caller doing its own
  `IS_NULL`/`NOT_NULL` bookkeeping alongside a wildcard leaf must not treat
  the two as complementary; both are `false` over `[]`, `null`, and absent.

- **`ModelNode`/schema surface: `IsArrayIndex` is now exported from this
  module and is the single definition of a well-formed array index** — a
  non-empty run of ASCII digits, checked for digit class only (magnitude is
  `parsePathSub`'s job, layered on top). `isSupportedSubscript`
  (`condition_filter.go`, the wire-path scanner) and `parsePathSub`
  (`filter_path.go`, the plugin-facing parser) both delegate to it now
  instead of each scanning its own copy — the prior duplication had let the
  two independently drift on subscript magnitude (a 19+ digit index passed
  one copy's check and failed the other's). A consuming repo's own
  array-index predicate — `cyoda-go`'s `internal/domain/model/schema`
  package carries one — should delegate to this exported definition rather
  than keep its own digit-run loop, for the same reason. Migration: no
  signature or behaviour change to an existing exported symbol; only a new
  export to consolidate onto.

**Conformance: `spitest`'s filter-path tables now demand the grammar above.**
`filterPathRejects`/`filterPathAcceptsData` (`spitest/searcher.go`) moved
`tags[0]`/`tags[*]` from the reject table to the accept table and added
overflow, slice, union, filter-expression and unbalanced-bracket cases to the
reject table. **A backend that has not implemented bracket-subscript
translation will fail `Searcher/FilterPath/Grammar` (and
`Iterable/FilterPath/Grammar`) on its next dependency update to this
module.** Of the listed consumers in `KNOWN_CONSUMERS.md`, this lands
directly on `cyoda-go`'s in-tree memory/sqlite/postgres plugins (which this
same milestone already updates in lock-step) and, separately, on
`cyoda-go-cassandra`, an out-of-tree plugin that must implement bracket
subscripts before it next bumps its pin, or add a `Harness.Skip` entry while
it catches up.

- **An unsatisfiable comparison now follows operator polarity, per
  stored-value type family.** `EvalLeaf`'s `kindCompare` and `kindStringOp`
  arms used to hardcode `false` whenever a field's own type family had no
  surviving sub-condition for the operand — correct for a positive operator,
  wrong for a negative one. `$.n NOT_EQUAL 12.5` on a field declared
  `INTEGER` used to answer `false` for every entity; it now answers `true`
  for every entity holding a non-null value there, because no integer equals
  `12.5` (PostgreSQL agrees: `select 5::int <> 12.5` is `t`). Decided per
  stored-value type family rather than on whole-expansion voidness: a field
  declared `[INTEGER, String]` is not exempt, because the string branch
  accepting the operand does not rescue the numeric branch's own answer.
  `evalCompare` now returns `(matched, hadCandidate)`; both arms answer
  `isNegativeOp(op)` on no candidate instead of hardcoding `false`.
  `Expansion.void` — dead in production, read only by its own test — is
  removed; an all-buckets-empty expansion is now just the ordinary
  no-candidate case both arms already handle. A stored value the kernel
  cannot *parse* (not merely a type-family mismatch — e.g. a JSON number
  whose scale overflows `Decimal`) is unaffected: that is a read failure,
  not a no-candidate answer, and stays non-match for every operator,
  positive or negative, per `correctness-over-availability.md`. Null and
  absent values are unaffected either way — that gate runs before either arm
  and applies uniformly to every binary operator.

- **`Prepare` and `prepareNode` return `(_, error)` for a leaf that cannot be
  evaluated, instead of silently degrading it to a leaf that never
  matches.** A never-matching leaf was safe while the condition language had
  no negation; a `NOT` above it inverts that into matches-everything, so
  every cause of it is now decided at prepare time, from the condition
  alone, before any entity is read. Four causes reject with an error
  wrapping the new `ErrUnevaluableLeaf`: an operand parsing into none of the
  leaf's declared types (including a nil/empty declared set); a
  `SourceData` **or** `SourceMeta` leaf whose `Path` is empty or outside its
  grammar (a `SourceMeta` path must additionally be a member of the closed
  meta vocabulary `extractFilterMetaValue` recognises); an unsupported
  operator, including a zero-`Op` leaf (which previously annihilated or was
  silently ignored depending on position in the tree); and a
  `LIKE`/`MATCHES_PATTERN` operand that will not compile, detected from the
  existing `ExpandLeaf` result rather than a second, duplicate compile.
  `preparedNode`'s `expanded` field is now dead (every node a successful
  prepare returns has a valid expansion) and is removed. **Migration: every
  caller of `Prepare` must handle the second return value** — `cyoda-go`'s
  three in-tree plugins update seven non-test call sites
  (`memory/searcher.go`, `memory/grouped_stats.go` ×2, `sqlite/searcher.go`,
  `sqlite/query_planner.go`, `sqlite/grouped_stats.go`,
  `postgres/query_planner.go`) in the same milestone. A caller that used to
  treat "no match" and "cannot evaluate" as the same outcome must now
  distinguish them: the former is a valid `PreparedFilter`, the latter is an
  error before one exists.

- **`FilterNot` — a branch node with exactly one child and an empty
  `Path`.** Negates its child's own two-valued match answer. Because a leaf
  holds when SOME addressed value satisfies it, negating that answer makes
  `NOT` a **universal** quantifier over a wildcard-addressed path ("no
  element matches"), a different question from the child operator's
  negative twin applied element-wise ("some element differs") — for
  `{"tags":["red","blue"]}`, `NOT($.tags[*] EQUALS "red")` is `false` while
  `$.tags[*] NOT_EQUAL "red"` is `true`. Vacuous truth over an empty array,
  an explicit `null`, or an absent field falls out of the same mechanism,
  with no special-casing. `Prepare` rejects a malformed `FilterNot` —
  `Children` of length 0 or ≥ 2, or a single zero-`Op` child — rather than
  guessing an interpretation (never "invert the AND of the children", never
  an unguarded `Children[0]`).

  **`groupToFilter` no longer folds an unrecognised operator into
  `FilterAnd`.** The wire `Operator` is matched exactly, case-sensitively,
  against the closed set `{AND, OR, NOT}` — `MapOperator`'s own leaf-operator
  convention — so `"NOT"`, `"xor"`, `""`, and a case variant such as `"and"`
  all now fail with `ErrUnknownOperator` instead of three of them silently
  becoming a conjunction. `ValidateConditionOperators` is extended to
  validate a `GroupCondition`'s own `operator` too, closing a front-door gap
  this tightening exposed. This was reachable only by a self-executing
  backend calling `ConditionToFilter` directly — the engine's own HTTP
  boundary already rejected an unrecognised group operator — but a backend
  built against an earlier pin silently misread it as `AND` rather than
  refusing the request.

  **Conformance: `spitest` makes `FilterNot` a requirement, not advice.**
  `Searcher/FilterNot` and `Iterable/FilterNot` run a real `spi.FilterNot`
  through every filter-taking entry point the suite covers, pinning the
  universal-quantifier reading (including the vacuous empty-array and
  absent-field cases) and the arity guard (`Children` of length 0 or ≥ 2
  fails with `ErrUnevaluableLeaf` rather than matching). The existing
  nested-malformed-path case gains a second variant nested under a real
  `FilterNot`, alongside the synthetic-op-name variant that stood in for it
  before `FilterNot` existed — neither subsumes the other: one pins a
  validator recursing into an operator it has never seen, the other pins it
  recursing through the real node. **A backend that recurses filter-path
  validation only into `FilterAnd`/`FilterOr` fails these on its next
  dependency update.**

- **`spitest`'s `Searcher/Pattern/MalformedLike` case now requires an error
  from `Search`, not an empty page.** The old case was justified as
  "rejecting a malformed pattern with a `400` is the request boundary's job,
  above the `Searcher`" — but the boundary already refusing a case is a
  reason it should be **unreachable** underneath, not a reason to require
  the wrong answer there once the boundary check is bypassed (a criterion
  stored before the check existed, or a self-executing backend with no
  boundary of its own). `cyoda-go`'s own `Search` callers already propagate
  the rejection as of this same milestone's `Prepare` error-return change
  above. **A backend whose `Search` still answers an empty page for a
  malformed `LIKE` operand fails conformance on its next dependency
  update.**

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

- **Conformance: 8 further `spitest` subtests closing three cross-backend
  divergences that shipped because the harness never reached the case.** All
  are pure additions — no `Harness.Skip` key changes. Each pins a contract
  that was already stated or already settled; none of them is new behaviour
  being invented at the conformance layer.

  - `AsyncSearch/SaveResults/EmptySequenceFences`. The epoch/terminal fence
    is a property of the CALL, not of the rows it carries: `SaveResults`
    with a sequence that yields nothing must still report `ErrStaleClaim`,
    `ErrAlreadyTerminal`, or `ErrNotFound` where a non-empty sequence would
    have, and must otherwise succeed persisting nothing. Backends that
    short-circuited on "nothing to write" returned `nil` to a reclaimed
    executor — which then went on to write a job status it no longer owned.
    A search matching zero entities is an ordinary outcome, so this is the
    shape a fenced-off executor most often reaches the store in.

  - The point-in-time committed-only family:
    `Entity/GetAsAt/CommittedOnlyInTx`,
    `Entity/GetAllAsAt/CommittedOnlyInTx`,
    `Entity/GetPage/AsAtCommittedOnlyInTx`,
    `Searcher/PIT/CommittedOnlyInTx`, `Iterable/PIT/CommittedOnlyInTx`.
    A point-in-time read ignores any ambient transaction and answers from
    committed state. `Searcher` and `GetPage` already said so; `GetAsAt`,
    `GetAllAsAt`, and `IterateOptions.PointInTime` now say so too. Each
    subtest writes BOTH a create and an update inside the transaction it
    then reads from, because the two fail differently — the create shows up
    as an extra row, the update as a correct row carrying the wrong payload.
    The cutoff is deliberately in the future so the window itself can never
    be what hides the write. A backend whose ordinary reads join the
    caller's transaction must route these off it; bounding the query on a
    timestamp is not sufficient, because a transaction-stable clock puts the
    transaction's own writes inside every window it can compute.

  - `Searcher/FilterPath/Grammar` and `Iterable/FilterPath/Grammar`.
    `Filter.Path`'s grammar is now written down on the field itself
    (`filter.go`) rather than living only in each backend's validator, and
    the two filter-taking entry points are held to it: a malformed non-empty
    path must be REFUSED with an error, at both `SourceData` and
    `SourceMeta`, including nested under an `and`/`or` branch. Answering it
    with an empty result set is the divergence being closed — a mistyped
    path and a predicate that genuinely matched nothing are different
    answers. A positive-control set of well-formed shapes (dotted,
    underscore, hyphen, numeric segment, and the canonical meta names) must
    keep working, so the grammar cannot be satisfied by refusing everything.
    `"$."`-prefixed paths are in the REJECT set: `ConditionToFilter` strips
    the prefix at the wire boundary, so a bare path is the contract.

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

- **`ErrInvalidFilterPath`** — the sentinel for a `Filter.Path` or
  `OrderSpec.Path` outside the grammar documented on `Filter.Path` (see the
  `### Changed` entry below). It was previously declared separately by each
  storage backend, so the only portable assertion a caller — or the
  `spitest` conformance suite — could make about a malformed path was that
  *some* error came back, and an out-of-tree backend could return anything at
  all. Backends keep their own package-level sentinel of the same name for
  local callers; each one now wraps this, so `errors.Is(err,
  spi.ErrInvalidFilterPath)` is the backend-agnostic classification.

  The `Searcher/FilterPath/Grammar` and `Iterable/FilterPath/Grammar`
  conformance subtests assert it. Migration for an out-of-tree backend: wrap
  the SPI sentinel in your existing one (`fmt.Errorf("%w",
  spi.ErrInvalidFilterPath)` preserves your message text) — a refusal that
  does not unwrap to it now fails conformance.

- **`ValidateLeafPattern(op FilterOp, value any) error`** and
  **`ValidateConditionPatterns(cond predicate.Condition) error`** — validate
  pattern operands against the same derivation the kernel evaluates with, so a
  caller's boundary check cannot drift from what the kernel accepts. Errors
  wrap the new **`ErrInvalidPattern`** and carry neither the operand nor the
  anchored form.

- **A trailing unpaired escape (`LIKE 'abc\'`) is now detectable, but its
  evaluation is deliberately unchanged.** It remains the one malformed `LIKE`
  pattern, and a leaf carrying one still matches nothing, so a search still
  returns an empty page rather than an error — `Prepare`'s contract is that a
  leaf whose operand cannot be expanded never matches, and this release does
  not promote that to a rejection. It becomes a rejection only where a caller
  invokes `ValidateLeafPattern` or `ValidateConditionPatterns` before
  evaluating.

- **Conformance: `spitest` now pins the `LIKE` and `MATCHES_PATTERN` grammar
  through the `Searcher` surface.** Two new subtests, `Pattern/LikeGrammar`
  and `Pattern/MalformedLike`, seed a fixed corpus and assert the glob rules
  above end-to-end — there was previously zero conformance coverage of
  either grammar. A backend that has not converged on the kernel's grammar
  (translates `LIKE` to a regex, for example) will fail one or both and
  needs a `Harness.Skip` entry for `Searcher/Pattern/LikeGrammar` and/or
  `Searcher/Pattern/MalformedLike` until it does.

- **`ParseFilterPath(p string) ([]PathHop, error)`** parses a plugin-facing
  filter path — no `"$."` leader — into its hops, per the grammar on the
  `Breaking` entry above. `PathHop{Name string; Subs []PathSub}` is one
  segment; `PathSub{Wildcard bool; Index int}` is one bracketed subscript,
  either the wildcard or a parsed non-negative index. An empty path parses to
  a nil hop slice and is valid (tree operators carry one). The error, when
  non-nil, wraps `ErrInvalidFilterPath`.

- **`ValidateFilterPath(p string) error`** reports whether `p` is a
  well-formed filter path without keeping the parsed hops — the boundary
  check a plugin author writes when it only needs to accept or reject, not
  resolve.

- **`ResolvePath(data []byte, hops []PathHop) []gjson.Result`** resolves a
  parsed filter path against one entity's JSON document and returns every
  value the path addresses, in document order — the ONE resolver
  `docs/cloud-parity/path-grammar.md` section 10 requires. A bare hop
  contributes exactly one result whatever its shape, never unwrapped; a `[N]`
  subscript contributes the element at that index or a non-existent result;
  a `[*]` subscript contributes one result per element and none at all over
  a non-array; a missing key contributes one non-existent result rather than
  being dropped, so a presence test can see it.

- **`DesugarCondition(c predicate.Condition) predicate.Condition`** rewrites
  every `predicate.ArrayCondition` in `c`'s tree into a
  `predicate.GroupCondition` (`AND`) of `predicate.SimpleCondition` `EQUALS`
  leaves, one per non-null value, addressing its element by a bracket index.
  See the `array`-clause entry under Breaking for the collapsing rules (a
  single leaf collapses to a bare condition; an all-null `Values` collapses
  to an empty, vacuously-true `AND`) and for why this is now the one place
  the clause's semantics are defined.

- **`IsArrayIndex(s string) bool`** is now exported: reports whether `s` is a
  non-empty run of ASCII digits, the digit-class half of "is this a
  well-formed array index." See the `IsArrayIndex` entry under Breaking for
  what it replaces.

- **`AdmitsNumeric(t DataType, v Decimal) bool`** is the single definition of
  numeric admission: whether a field declaring `t` can hold `v`. Ingestion and
  the search kernel used to answer "does this type hold this value" by
  different routes, and the routes could disagree; this is the one predicate
  meant to replace both, so that admitted implies findable once each side
  calls it. It is deliberately NOT "is `v`
  inside `t`'s range" — for `DOUBLE` the operand bucket drops the `EQUALS`
  branch entirely above 15 significant digits or a scale of 292, so a value
  admitted on range alone would be stored where `EQUALS` could never find it
  and `NOT_EQUAL` would wrongly match it, and the precision bound is that
  53-bit mantissa argument stated as a value test rather than a label test —
  a 10-digit value like `2147483648` is admitted despite exceeding `int32`,
  a 16-digit one is not, regardless of which type's label it arrives under.
  Its first caller is the kernel's own stored-value filter
  (`evalCompare`/`evalBetween`, see the `### Fixed` entry below);
  cyoda-go's write path is the predicate's other intended caller, not yet
  wired as of this release — the point of there being one predicate is that
  the two sides of "was this value admitted" cannot disagree once both call
  it.

- **`NewEmptyNode() *ModelNode`** returns a node that declares nothing: no
  branch, and not nullable. It is the model a fresh derivation walks against
  — deriving a field's description is the same traversal as admitting a
  value, run against a model that admits nothing — and it is NOT
  `NewLeafNode(Null)`, which records that a path HAS been observed holding
  null and therefore already admits it.

### Changed

- **The `spitest` filter-path conformance table now pins the evaluator's own
  metacharacters.** `filterPathRejects` covered the injection shapes, a
  backslash and an asterisk, but not `?`, `#`, `|` or `!` — the four whose
  acceptance is not an empty page but the WRONG page. Measured against gjson:
  `?` is a single-character key wildcard (`a?b` is answered by a sibling
  `aXb`), `|` is an alternative segment separator (`a|b` is answered by a
  nested `a`→`b`, the `.` collision respelled), `#` is the array
  count/projection segment, and `!` introduces a literal (`!true` is `true`
  whatever the document holds). Every in-tree backend already refuses them —
  they enforce `[A-Za-z0-9_-]` byte-for-byte — so this states an existing
  obligation rather than adding one. A backend with a different evaluator is
  held to the same table: its own metacharacters must be a subset of what the
  grammar already excludes.
  ([#43](https://github.com/Cyoda/cyoda-go-spi/issues/43))

- **`Filter.Path` now documents its grammar on the field.** At the time of
  this entry the accepted form was `segment ( "." segment )*` with
  `segment = 1*( ALPHA / DIGIT / "_" / "-" )`, ASCII only: no empty segment,
  no leading or trailing dot, no bracketed subscript or wildcard (an array
  position was an ordinary numeric segment, `tags.0`), and no `"$."` prefix —
  `ConditionToFilter` strips that at the wire boundary, so a path arrives at a
  plugin bare. An empty `Path` stays legal and unchecked (tree operators carry
  one). A malformed non-empty path MUST be rejected with an error rather than
  answered with an empty result set, at both `FieldSource` values and
  anywhere in the tree.

  **Superseded within this same `[Unreleased]` window:** the "no bracketed
  subscript" clause above no longer holds — see "`Filter.Path`'s grammar
  admits an array subscript" under Breaking, which is the current grammar.
  Everything else in this entry (no empty/leading/trailing-dot segment, no
  `"$."` prefix, mandatory rejection of a malformed non-empty path) still
  holds.

  This was a documentation change, not a contract change, at the time it
  landed: it wrote down the grammar the SQL backends' validators already
  enforced. It is called out here because the grammar previously existed only
  inside those validators, and a backend author reading the SPI had nothing
  to conform to — which is how one backend came to accept silently what the
  others rejected.

- **Point-in-time reads are documented as committed-only across the whole
  family.** `Searcher` and `EntityStore.GetPage` already stated it;
  `EntityStore.GetAsAt`, `EntityStore.GetAllAsAt`, and
  `IterateOptions.PointInTime` now state it too, including the reason a
  timestamp bound cannot achieve it on a backend whose clock is
  transaction-stable. Also a documentation change: the contract is
  unchanged, it was simply unstated on three of the five members.

- **`AsyncSearchStore.SaveResults` spells out that the fence runs on an empty
  sequence** and that a missing job returns `ErrNotFound`. The fencing
  sentence already covered it by implication; making it explicit is what the
  new conformance subtest enforces.

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

- **`ExpandLeaf` (and `EvalLeaf`, which evaluates what it built) now states
  its contract on `declared`: at most one numeric `DataType`.** Every
  production caller already routes through `TypeSet`'s `CollapseNumeric`
  before reaching this function, so a `declared` set carrying more than one
  numeric type was always outside what the kernel exercises — but nothing
  said so. A caller that skips the collapse is now told what it forfeits: a
  stored value can satisfy several of those buckets rather than the single
  narrowest one, since `AdmitsNumeric` judges each declared type
  independently. Documentation only; no behavior changes.

- **`AdmitsNumeric`'s doc comment states the mantissa guarantee it actually
  gives, not a looser one.** It previously said "every integer above 2^53
  needs at least 16 significant digits, so precision <= 15 excludes exactly
  the values a 53-bit mantissa cannot hold" — false under the predicate's
  own stripped-precision convention (`1e16` strips to precision 1 and is
  admitted despite being past 2^53; the bound is on significant digits, not
  magnitude). The exact guarantee: a decimal of at most 15 significant
  digits round-trips uniquely through a binary64 double, which is what the
  `DOUBLE` bucket's findability and the lossless float8 pushdown need —
  `2147483648` (10 digits) is inside that, `9007199254740993` (16) is not.
  Documentation only; no behavior changes.

### Fixed

- **`spitest`: the `AsyncSearch/ReapExpired` subtests no longer race the
  reaper's clock.** They stamped a finish time from the harness clock, slept
  the TTL plus one millisecond, and expected the store's own clock to agree
  within that millisecond. A backend whose store clock is a different domain
  from the harness clock (postgres stamps `h.Now` from the database, the
  reaper's cutoff from the host) failed both subtests whenever the database
  clock ran more than that millisecond ahead of the host — reproduced
  deterministically with the harness clock skewed 20 ms ahead. Every
  timestamp is now hours apart and brackets the TTL from both sides: the
  reaped job finished an hour before the harness clock, survives a two-hour
  TTL and is reaped by a one-minute one; the new
  `AsyncSearch/ReapExpired/FreshNotReaped` subtest pins that a job finished
  a minute ago survives a one-hour TTL and that a running job created an
  hour ago is never eligible. Jobs are created before they finish. Two
  subtest paths are new since the last tag —
  `AsyncSearch/ReapExpired/CancelledIsReapable` and
  `AsyncSearch/ReapExpired/FreshNotReaped` — and `Harness.Skip` matches a
  path exactly, so a backend that skips `AsyncSearch/ReapExpired` needs a
  key per path it cannot pass.

- **Every `Decimal` path a request can reach is now bounded by the operand's
  own digits, not by its scale.** Four paths a request or an SPI consumer
  reaches cost time and memory proportional to a number the caller writes in a
  handful of bytes.
  `StripTrailingZeros` — on the search-operand path in `expandCompare` and on
  the write path in `inferDataType` — removed one zero digit per full-width
  division, so a "1" followed by a million zeros took minutes; it now counts
  the run once and divides once (1.6 s → 2.3 ms at 100,000 zeros; 68 ms at a
  million, where the old loop did not finish). `roundToScale`, reached for
  every comparing operand through `foldToInt` and through the DOUBLE bucket's
  `roundDoubleImprecise`, built `10^(scale-newScale)` before dividing, so
  `-1e-2000000000` climbed toward 900 MB and ran for minutes; below the
  operand's own precision the quotient is 0 and the whole value is the dropped
  part, so the rounded result is `0` or `±1` by sign and mode, with no power of
  ten materialised (36 s → 1.7 µs at a scale gap of 10^8; the same early exit
  makes `SetScale` report its precision-loss error without building the
  divisor, and zero now rescales without one in either direction). `Cmp`
  computed each side's adjusted exponent from `Precision()`, a full big.Int to
  decimal-string conversion, twice per comparison and therefore once per row in
  `evalCompare`/`evalBetween`; it now brackets the digit count from `BitLen`
  and pays for the exact count only when the two brackets overlap (105 ms →
  292 ns on a million-digit coefficient). And `ParseStringOrNull`'s whole-type
  branch normalised a negative scale to 0 through `SetScale`'s upward path,
  materialising `10^(-scale)` before any range check: `("1e40000000", LONG)`
  took 8.5 s. A whole value carries `Precision() + (-Scale())` digits, and past
  INT128's 39 it is outside `INTEGER`, `LONG` and `BIG_INTEGER` alike, so the
  digit count settles the parse with nothing built (8.5 s → 5.7 µs);
  `UNBOUND_INTEGER` has no range to check and now keeps the stripped
  negative-scale form, exactly as `foldToInt` does — the value is unchanged and
  every consumer reads it through `Cmp`, but `Scale()` and `Unscaled()` on that
  one result are no longer normalised to 0. All four are otherwise
  behaviour-preserving, pinned by property tests against the previous
  implementations.

- **`FieldsMapFromSchema` no longer drops a declared path on a field observed as
  more than one kind.** Both the decoder and the flattening dispatched on the
  node's single `kind` label, so the array branch of an object-and-array union
  and the scalar branch of an array-and-scalar union were discarded — the label
  named one branch while the payload carried both. A predicate on a dropped path
  then found no declared type and matched nothing, so a backend that executes
  searches itself answered with fewer rows and no error at all. Decoding is
  payload-driven as well as label-driven now, and the flattening walks the
  branch set — which is what the engine's own flattening already did, so this
  closes a divergence between the two readers of the same bytes.

- `UnmarshalModelNode` rejects a JSON-null child node with an error. The
  equivalent decoder this was derived from dereferences the nil and panics,
  which is reachable from persisted bytes.

- **`LIKE`'s `%` and `_` now match newlines.** They compiled to `.*?` and `.`,
  which exclude `\n` in RE2, so `LIKE '%'` did not match every string and a
  multi-line value was unreachable. This contradicted the published grammar
  ("any sequence of characters") and both SQL engines.

- **`LIKE` operands that could not be compiled now match literally.** The
  translation turned the operand into a regex, so `LIKE '\Q'` and
  `LIKE '\p{Foo}'` produced an expression that failed to compile; the error
  was swallowed and the leaf silently matched nothing, reporting nothing.
  `LIKE` no longer compiles anything, so those operands now match `Q` and
  `p{Foo}` respectively, per the escape rule above.

- **`OrderSpec.Path` no longer treats a dotted numeric segment as an array
  index.** `orderLeafValue` (`order_compare.go`) resolved a sort key with
  `gjson.GetBytes` directly, and gjson resolves an all-digit path segment
  against an array as a positional index — the same data-driven collapse
  `docs/cloud-parity/path-grammar.md` forbids everywhere else. A sort key
  `tags.0` meant to address a field named `"0"` inside an object at `tags`
  could instead read element 0 of an array at `tags`, silently ordering rows
  by the wrong value. It now resolves through `ParseFilterPath`/`ResolvePath`
  like every other path in the module. A sort key never carries a subscript
  (plugin validators reject one on an `OrderSpec`), so this is always a
  0-or-1-value resolution and no ordering behavior changes for a
  non-colliding path. Covered by `TestLessByOrder_NumericSegmentIsNotAnIndex`.

- **An empty leaf `Path` no longer resolves to the whole document.**
  `Filter.Path`'s empty string is legal for a tree operator (`FilterAnd` /
  `FilterOr` address no field of their own; `Children` carries the real
  leaves), and `ParseFilterPath("")` legitimately succeeds with a nil hop
  slice for that reason. `prepareNode` (`prepared_filter.go`) never
  special-cased an empty `Path` on a `SourceData` leaf before delegating to
  `ParseFilterPath`, so a leaf built with one got that same nil hop slice,
  and `ResolvePath(data, nil)` resolves a nil hop slice to the parsed ROOT
  DOCUMENT — a presence test on such a leaf matched every entity, and an
  equality test matched whenever the operand happened to equal the
  document's own `gjson.Result`. `orderLeafValue` (`order_compare.go`) had
  the identical defect on the sort side. Both now resolve an empty
  `SourceData` leaf `Path` to nothing, the same "never matches" outcome a
  path that fails to parse already produces; a tree operator's empty `Path`
  is untouched and stays legal. `SourceMeta` already resolved an empty path
  to nothing (`extractFilterMetaValue`'s switch has no `""` case) and gains
  a pinning test rather than a behavior change. Not reachable through any
  in-tree HTTP or gRPC transport today — every one requires a leaf's
  `jsonPath`/`Path` to be non-empty before it ever reaches a `Filter` — but
  `PreparedFilter.Match` is the authoritative kernel every backend defers to
  (memory directly, sqlite/postgres as their post-pushdown correctness
  check), so the fix is effective everywhere without touching per-backend
  pushdown code. Covered by `TestPreparedFilter_EmptyLeafPathNeverResolves`
  and `TestLessByOrder_EmptyDataPathNeverResolves`.

- **`ConditionToFilter` no longer re-desugars a condition tree once per
  ancestor level.** It desugars the whole tree once via `DesugarCondition`
  (whose own `GroupCondition` case already recurses into every descendant in
  that one call), but `groupToFilter` recursed back through
  `ConditionToFilter` for each child, re-running `DesugarCondition` on that
  child's already-desugared subtree. For a depth-D chain of single-child AND
  groups this telescoped into `D + (D-1) + ... + 1` = O(D²) group-node
  revisits instead of O(D). `groupToFilter` now recurses into a new
  unexported `desugaredToFilter` — `ConditionToFilter`'s post-desugar
  dispatch, factored out — instead of `ConditionToFilter` itself, so the
  desugar pass runs exactly once per `ConditionToFilter` call regardless of
  tree depth. No output changes; this is a complexity fix only. Covered by
  `TestConditionToFilter_DesugarIsNotReappliedPerLevel`.

- **`ErrUnevaluableLeaf`'s "operand parses into no declared type" message no
  longer echoes the operand verbatim.** A field with no declared type is the
  single most common way to reach this error — a search or conditional-delete
  request with an unannotated field — and search request bodies are capped at
  10 MiB, so the echoed operand could grow to megabytes: measured, a 1 MiB
  operand produced a ~2 MiB error string, doubled again by
  `prepared_filter.go`'s own wrap of the same operand around `ExpandLeaf`'s
  error. A caller mapping this to a client 400 (cyoda-go logs it at WARN)
  logged the whole thing a second time as `cause`. Both echoes are now capped
  at `maxEchoedOperandBytes` (200) with an explicit `...(truncated)` marker,
  mirroring `ErrInvalidPattern`'s existing choice to bound what a client-facing
  400 repeats back. Covered by `TestExpandLeaf_TypeMismatchError_BoundsOperandLength`
  and `TestPrepare_UnevaluableLeaf_BoundsOperandInErrorMessage`.

- **A stored number is judged by what the declared type admits, not by its
  narrowest label.** `evalCompare`/`evalBetween` derived a stored value's
  narrowest label and asked `IsAssignableTo`, so a `[DOUBLE]` leaf never
  matched a stored `2147483648` even when the operand had produced a
  `DOUBLE` sub-condition — `LONG` does not widen into `DOUBLE`. Both now ask
  `AdmitsNumeric` instead, the predicate that let the value into the field in
  the first place; `evalCompare` tests it per sub-condition and `evalBetween`
  scans the declared set, since the two shapes differ. `classifyStoredNumeric`
  had no other caller and is deleted. Covered by the new end-to-end property
  test `TestAdmitsNumeric_AdmittedValueIsFindable`: a value a type admits
  must be findable by `EQUALS` on that type.

- **The comparison operand is stripped of trailing zeros once, at
  `expandCompare`.** Ingestion already stripped trailing zeros before
  classifying a value, but the operand side did not, so `EQUALS 5.0` could
  not find a stored `5` and `NOT_EQUAL 5.0` wrongly matched it. The strip
  belongs at `expandCompare`'s `ParseDecimal` rather than inside `foldToInt`:
  the decimal bucket computes precision on the operand too, so
  `EQUALS 5.000000000000000000` against a stored `5` (and against a `DOUBLE`
  leaf) had the identical defect one function away. `-0.0` falls out of the
  same fix.

- **A 13-byte operand with an enormous exponent no longer allocates a
  ten-million-digit integer.** `foldToInt` folds a whole value by multiplying
  the coefficient by `10^-scale`, and `ParseDecimal` bounds scale only to
  `int32`, so `"1e10000000"` allocated for roughly a second before a single
  row was read — reachable from an ordinary search request through condition
  validation. `foldToInt` now refuses to normalise a scale it cannot
  represent cheaply rather than materialising it, `StripTrailingZeros` gained
  the matching underflow guard on the same values, and `Decimal.Cmp` compares
  sign and magnitude first, aligning scales only on a tie (where the cost is
  bounded by the operands' own digit counts) instead of always aligning to
  the larger scale before comparing — the same allocation was reachable
  through `toRange` in the decimal family before any fold ran. `foldToInt` no
  longer normalises a whole value (scale `<= 0`) at all, since every
  consumer now reaches the returned value only through the now-magnitude-first
  `Cmp`. `Cmp` also gains an equal-scale fast path — the common case on a
  per-row scan loop, where every value in a column shares one scale —
  comparing the two coefficients directly instead of computing each side's
  adjusted exponent (two `Precision()` calls, each a string conversion)
  first.

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
