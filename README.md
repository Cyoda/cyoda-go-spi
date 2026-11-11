# cyoda-go-spi

Storage-plugin contract for [cyoda-go](https://github.com/cyoda-platform/cyoda-go).

This module defines the interfaces and value types that any storage
backend must implement. Plugin authors depend only on this module.

## Packages

- `spi` — core interfaces, value types, sentinel errors, `UUIDGenerator`,
  `ClusterBroadcaster`, and `Plugin` registration machinery.
- `spi/predicate` — search-predicate AST types and JSON parse/marshal.

## Dependencies

Standard library only.

## Versioning & Compatibility

- **Releases & changes:** see [`CHANGELOG.md`](CHANGELOG.md).
- **Release process & deprecation policy:** see [`MAINTAINING.md`](MAINTAINING.md).
- **Notifications for breaking changes:** open an issue or PR your project
  into [`KNOWN_CONSUMERS.md`](KNOWN_CONSUMERS.md).

## License

Apache 2.0.

## For Plugin Authors

Every `spi.StoreFactory` implementation should run the `spitest` conformance harness to verify it meets the SPI contract.

### Wiring the Harness

```go
package myplugin_test

import (
    "testing"
    "time"

    spitest "github.com/cyoda-platform/cyoda-go-spi/spitest"
    "github.com/your-org/myplugin"
)

func TestConformance(t *testing.T) {
    factory := myplugin.NewStoreFactory(/* ... */)
    spitest.StoreFactoryConformance(t, spitest.Harness{
        Factory:      factory,
        AdvanceClock: func(d time.Duration) { time.Sleep(d) },
    })
}
```

### Optional Capabilities

A plugin may implement these interfaces in addition to the required ones.
cyoda-go detects each by type assertion; a plugin without one is
conformant.

- `DescribablePlugin` (on the `Plugin`): lists the configuration variables
  the plugin reads.
- `Startable` (on the `StoreFactory`): `Start` runs after `NewFactory` and
  before any store-facing call.
- `CompositeUniqueKeyCapable` (on the `StoreFactory`): composite unique-key
  support.
- `TransactionCoordinationCapable` (on the `StoreFactory`): the store
  coordinates a transaction's callouts and callback scopes itself, so a
  transaction can be served by any cyoda-go node. A nil coordinator with a
  nil error means "not capable". The `Coordination/*` conformance cases run
  only for a capable factory.
- `GroupedAggregator` (on the `EntityStore`): grouped aggregation in the
  store. The `GroupedAggregator/*` conformance cases run only when the
  store implements it.

### `AdvanceClock` Contract

The harness calls `AdvanceClock(d time.Duration)` between writes that need distinct timestamps. After `AdvanceClock` returns, every subsequent timestamp the plugin assigns must strictly dominate every timestamp assigned before the call. `d > 0`.

Plugins wire this to whatever clock mechanism they use:

- **In-memory / app-side clock:** inject a `TestClock` with an `Advance(d)` method via a factory option; `AdvanceClock` calls that.
- **DB-side clock (e.g., PostgreSQL):** use `time.Sleep(d)`. The DB's monotonic wall clock satisfies the contract; ~1–5ms gaps are sufficient.
- **Logical clock (e.g., Cassandra HLC):** advance the physical component via a test-only hook.

### `Harness.Now` (optional)

If your plugin uses an injected `TestClock`, also set `Harness.Now` to the clock's `Now()` method so the harness's temporal assertions use the same clock as the plugin. Defaults to `time.Now` which matches wall-clock-based plugins (postgres).

### Error-Assertion Contract

The harness uses `errors.Is()` against SPI sentinels (`spi.ErrNotFound`, `spi.ErrConflict`). Plugins MUST wrap backend-native errors at the SPI boundary:

```go
// WRONG — harness will fail
return pgx.ErrNoRows

// RIGHT — harness passes
return fmt.Errorf("entity %q: %w", id, spi.ErrNotFound)
```

### State Isolation

Every subtest runs under a fresh tenant. The harness never calls `Reset`, `Truncate`, or any teardown hook. A single factory handles all subtests across different tenants. `Factory.Close()` is called once when the suite finishes.

Cross-tenant leakage is caught by the explicit `TenantIsolation/*` subtests, not by infrastructure.

### Known Limitations / Harness.Skip

Backends with structural incompatibilities can register documented skips via `Harness.Skip`. The key is the subtest path **below** the root test name (the part after the first `/`). Mistyped keys cause the suite to fail with an "unused skip key" error, preventing stale entries from silently accumulating.

```go
spitest.StoreFactoryConformance(t, spitest.Harness{
    Factory:      factory,
    AdvanceClock: testClock.Advance,
    Skip: map[string]string{
        "Transaction/Join":                        "pending #42: Join does not share write-set",
        "AsyncSearch/SaveAndGetResults/Pagination": "pending #43: SaveResults not yet implemented",
    },
})
```
