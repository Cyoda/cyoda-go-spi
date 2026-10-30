package spi

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Plugin is the storage-backend contract. Implementations register
// themselves at init time by calling Register.
type Plugin interface {
	Name() string
	NewFactory(ctx context.Context, getenv func(string) string, opts ...FactoryOption) (StoreFactory, error)
}

// DescribablePlugin is an optional Plugin capability: it exposes the
// configuration variables the plugin reads, so --help can render them.
type DescribablePlugin interface {
	Plugin
	ConfigVars() []ConfigVar
}

// Startable is an optional StoreFactory capability: the core calls
// Start immediately after NewFactory and before any store-facing call
// (including TransactionManager). Plugins that need background
// goroutines — shard managers, consumer groups, rebalance waits,
// long-lived cluster connections — implement this. Start must
// complete (successfully) before the factory is expected to serve
// transactions; plugins whose TransactionManager depends on Start's
// side effects (rebalance-assigned shards, consumer group membership)
// can rely on this ordering.
//
// Start is bounded by the caller's context (typically a startup
// timeout). Plugins must honor ctx.Done() for cancellation.
type Startable interface {
	Start(ctx context.Context) error
}

// ConfigVar documents a single environment variable a plugin reads.
type ConfigVar struct {
	Name        string
	Description string
	Default     string
	Required    bool
}

// Clock is the source of a platform's business time: the instants a store
// records as submit times, entity creation and modification dates, audit
// event times and consistency times, and the instants it compares them
// with (the retention of submit times, the age at which a finished search
// job is reaped).
//
// A Clock may run at any rate, may be set to any instant, and may go
// backward. It is read concurrently and must be safe for that.
type Clock interface {
	Now() time.Time
}

// FactoryOption configures a storage factory during NewFactory.
// Plugins receive options via the variadic parameter and resolve them
// with ApplyFactoryOptions.
type FactoryOption func(*factoryConfig)

// factoryConfig is the mutable, unexported accumulator for options.
type factoryConfig struct {
	broadcaster ClusterBroadcaster
	clock       Clock
}

// FactoryConfig is the read-only view plugins see after resolution.
type FactoryConfig struct {
	inner factoryConfig
}

// ClusterBroadcaster returns the cluster broadcaster supplied via
// WithClusterBroadcaster, or nil if none was supplied.
func (c FactoryConfig) ClusterBroadcaster() ClusterBroadcaster {
	return c.inner.broadcaster
}

// WithClusterBroadcaster injects the cluster broadcaster for plugins
// that use ClusterBroadcaster for cluster-wide notifications.
func WithClusterBroadcaster(b ClusterBroadcaster) FactoryOption {
	return func(c *factoryConfig) { c.broadcaster = b }
}

// Clock returns the clock supplied via WithClock, or nil if none was
// supplied.
func (c FactoryConfig) Clock() Clock {
	return c.inner.clock
}

// WithClock gives the plugin the platform's business clock.
//
// A plugin given a clock takes every business stamp from it: submit time,
// SnapshotTime, consistency time, audit event times, the retention of
// submit times behind GetSubmitTime, and the cutoff by which search jobs are
// reaped. It keeps its own physical clock, unaffected by c, for liveness:
// scheduler-owner heartbeats and sweeps, search-job heartbeats and stale
// reclaim, and the database or lock timeouts it applies.
//
// Without the option a plugin's business clock is its physical clock.
func WithClock(c Clock) FactoryOption {
	return func(f *factoryConfig) { f.clock = c }
}

// ApplyFactoryOptions resolves the variadic options into a read-only
// FactoryConfig. Plugins call this inside NewFactory.
func ApplyFactoryOptions(opts []FactoryOption) FactoryConfig {
	cfg := &factoryConfig{}
	for _, o := range opts {
		o(cfg)
	}
	return FactoryConfig{inner: *cfg}
}

// --- Registry ---

var (
	registryMu sync.RWMutex
	registry   = map[string]Plugin{}
)

// Register adds p to the plugin registry. Register panics if another
// plugin has already been registered under the same Name — a naming
// collision at init time is always a programmer error. Matches the
// database/sql.Register convention.
func Register(p Plugin) {
	registryMu.Lock()
	defer registryMu.Unlock()
	name := p.Name()
	if _, dup := registry[name]; dup {
		panic(fmt.Sprintf(
			"cyoda-go-spi: two storage plugins registered with the name %q. "+
				"Check the blank imports in your main package — a binary must "+
				"include exactly one plugin per name.",
			name))
	}
	registry[name] = p
}

// GetPlugin returns the registered plugin with the given name.
func GetPlugin(name string) (Plugin, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	p, ok := registry[name]
	return p, ok
}

// RegisteredPlugins returns the names of all currently registered
// plugins, sorted by name for deterministic ordering.
func RegisteredPlugins() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
