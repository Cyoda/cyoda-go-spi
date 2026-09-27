// Package spitest provides a conformance test harness for spi.StoreFactory
// implementations. Plugin authors wire StoreFactoryConformance into their
// test suite with a single call; the harness exercises all SPI contract
// invariants across every store interface exposed by the factory.
//
// Every subtest runs under a fresh tenant produced by Harness.NewTenant.
// No subtest reuses another's tenant, so no database truncation or Reset
// hook is needed. The ScheduledTasks group is the one exception to "no
// teardown": ClaimDue is cross-tenant, so each of its subtests removes the
// tasks it armed when it ends.
//
// Temporal subtests use Harness.AdvanceClock to move the plugin's virtual
// clock forward deterministically. The contract: after AdvanceClock(d)
// returns, every subsequent timestamp the plugin assigns strictly
// dominates every timestamp assigned before the call. d > 0.
//
// A harness backed by a real clock (one that sleeps rather than moving an
// injected clock) may cap how far a single call advances: it moves the
// clock forward by at least min(d, that cap), never less, and the strict-
// dominance guarantee still holds for the smaller amount. A subtest that
// needs a specific elapsed duration larger than a harness's cap issues
// AdvanceClock that many times rather than relying on one call for the
// full d.
//
// A backend whose StoreFactory.ScheduledTaskStore returns an error
// satisfying errors.Is(err, errors.ErrUnsupported) skips the ScheduledTasks
// group.
//
// A few SPI interfaces are optional (spi.GroupedAggregator, for example).
// The harness detects an absent optional interface by type assertion and
// skips that whole group: a backend that does not implement it is
// conformant, not broken. Do NOT add a Harness.Skip entry for such a group —
// StoreFactoryConformance reports every Skip key that never matched as an
// error, so the entry would turn a conformant backend red.
//
// Error assertions use errors.Is against spi sentinel errors
// (spi.ErrNotFound, spi.ErrConflict). Plugins MUST wrap backend-native
// errors at the SPI boundary.
package spitest
