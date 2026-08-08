// Package spi defines the storage-plugin contract for cyoda-go.
//
// # Plugin Authoring — Minimal Example
//
// A storage plugin is a Go module that implements the Plugin interface
// and registers itself at package init() time:
//
//	package myplugin
//
//	import (
//		"context"
//		spi "github.com/cyoda-platform/cyoda-go-spi"
//	)
//
//	func init() { spi.Register(&plugin{}) }
//
//	type plugin struct{}
//
//	func (p *plugin) Name() string { return "myplugin" }
//
//	func (p *plugin) NewFactory(
//		ctx context.Context,
//		getenv func(string) string,
//		opts ...spi.FactoryOption,
//	) (spi.StoreFactory, error) {
//		// Parse config via getenv (not os.Getenv — the core injects a
//		// closure so tests can supply a fake environment).
//		dsn := getenv("MYPLUGIN_DSN")
//
//		// Resolve options (e.g., a ClusterBroadcaster for cluster-wide
//		// notifications). Plugins that don't need any option can skip.
//		cfg := spi.ApplyFactoryOptions(opts)
//		_ = cfg.ClusterBroadcaster() // nil if unset
//
//		// Connect, run migrations, return a ready factory. Use ctx for
//		// all blocking setup work so unreachable infra fails fast.
//		return newStoreFactory(ctx, dsn), nil
//	}
//
// The binary is built with a blank import of the plugin:
//
//	import _ "example.com/myplugin"
//
// which causes init() to run and Register to install the plugin in the
// process-global registry. The core resolves the active backend at
// startup by calling spi.GetPlugin(name).
//
// # The getenv injection
//
// Plugins read their configuration through the injected getenv function,
// not os.Getenv directly. In production the core passes os.Getenv; in
// tests the core passes a closure over a map[string]string so that test
// fixtures can provide exactly the variables the test needs without
// leaking into the process environment.
//
// # Plugin-owned TransactionManager
//
// Each plugin provides its own TransactionManager via StoreFactory's
// TransactionManager(ctx) method. The plugin's TM implementation is
// free to couple tightly to the plugin's stores (memory does) or be a
// lightweight lifecycle tracker (postgres does — the pgx.Tx is tracked
// in a plugin-internal registry and stores look it up by txID when
// called inside an active transaction). The pattern for bridging a
// logical txID to a physical transaction handle:
//
//	// Begin registers the handle in the plugin's internal registry.
//	func (tm *TM) Begin(ctx context.Context) (string, context.Context, error) {
//		phys := openPhysical(ctx)
//		txID := uuid.UUID(tm.uuids.NewTimeUUID()).String()
//		tm.registry.Register(txID, phys)
//		state := &spi.TransactionState{ID: txID}
//		return txID, spi.WithTransaction(ctx, state), nil
//	}
//
//	// Stores resolve the handle from context.
//	func (f *StoreFactory) queryExecutor(ctx context.Context) Querier {
//		if state := spi.GetTransaction(ctx); state != nil {
//			if phys, ok := f.tm.Lookup(state.ID); ok {
//				return phys
//			}
//		}
//		return f.defaultExecutor  // e.g., a connection pool
//	}
//
// # Startable and Close — symmetry
//
// Plugins with background goroutines implement the optional Startable
// interface. The core calls Start(ctx) immediately after NewFactory
// and before any store-facing call (including TransactionManager), so
// plugins whose TransactionManager depends on Start's side effects
// (e.g. cassandra's shard-rebalance wait) can rely on the ordering.
// Plugins must tear down those goroutines in StoreFactory.Close():
// each goroutine observes either ctx.Done() or a shutdown channel
// closed by Close(); Close() waits (bounded) for them to exit with a
// sync.WaitGroup. Leaked goroutines compound under test-driven
// create/destroy cycles.
//
// # Dependencies
//
// The spi package depends on the Go standard library plus google/uuid
// and tidwall/gjson. Plugin authors depend only on this module; they do
// not depend on cyoda-go itself.
//
// # Predicate AST, and translating it
//
// Submodule spi/predicate holds the search-predicate AST and JSON
// parse/marshal helpers. Plugins with no search semantics may ignore it.
//
// A plugin that receives a predicate.Condition should translate it with
// [ConditionToFilter] rather than interpreting the AST itself. The
// resulting [Filter] is what the leaf-comparison kernel ([Prepare],
// [EvalLeaf]) evaluates, and building one any other way means answering
// the same query differently from every other backend — the comparison
// rules are type-directed and subtle enough that an independent
// implementation drifts rather than merely lagging. Declared types come
// from [FieldsMapFromSchema] over ModelDescriptor.Schema; supplying a nil
// fields map is not a safe degraded mode, and ConditionToFilter documents
// why.
//
// Deciding which parts of a Filter to narrow on in the plugin's own query
// dialect (SQL, CQL) is the separate and expected step: a planner may
// push down what it can and leave the rest residual, because the kernel
// re-checks every candidate.
package spi
