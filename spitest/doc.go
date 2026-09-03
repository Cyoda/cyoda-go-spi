// Package spitest provides a conformance test harness for spi.StoreFactory
// implementations. Plugin authors wire StoreFactoryConformance into their
// test suite with a single call; the harness exercises all SPI contract
// invariants across every store interface exposed by the factory.
//
// Every subtest runs under a fresh tenant produced by Harness.NewTenant.
// No subtest reuses another's tenant, so no database truncation, Reset
// hook, or explicit teardown is needed.
//
// Temporal subtests use Harness.AdvanceClock to move the plugin's virtual
// clock forward deterministically. The contract: after AdvanceClock(d)
// returns, every subsequent timestamp the plugin assigns strictly
// dominates every timestamp assigned before the call. d > 0.
//
// Error assertions use errors.Is against spi sentinel errors
// (spi.ErrNotFound, spi.ErrConflict). Plugins MUST wrap backend-native
// errors at the SPI boundary.
package spitest
