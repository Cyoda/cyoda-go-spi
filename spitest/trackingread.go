package spitest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// trackingread.go holds the whole read-set contract of the opt-in tracking
// read, for both filter-taking read entry points. IterateOptions.TrackingRead
// and SearchOptions.TrackingRead are the same flag on the same contract:
//
//   - set, it records what the read hands back — and only that. A row the
//     filter excludes was never handed to the caller and must not be
//     recorded, whichever layer excluded it.
//   - unset, it records nothing at all.
//   - a point-in-time read records nothing either way: it is committed-only
//     and historical, and a read set can only carry the current version.
//
// Both entry points run one driver here, so neither can drift from the other,
// and no backend has to re-derive the contract in a test of its own — which
// is what every backend did before this file existed.
//
// Observed black box, never via a backend's internal state: an independent
// transaction overwrites one of the two seeded entities and commits, and the
// tracking transaction's own commit outcome is the read-out of what its read
// recorded — the technique the GetPage read-set cases already use.

// trackingReadModel is the model these cases seed into. Each case runs under
// a fresh tenant (see tenantContext), so one fixed name shared by the
// Iterable and Searcher suites is collision-free — the convention
// iterableModelRef and filterNotModel already follow.
const trackingReadModel = "tracking-read"

// trackingReadSearchLimit bounds the Search entry point. Search is
// bounded-or-fail (SearchOptions.Limit >= 1 is required), and the fixture
// seeds two entities, so any bound above two takes the limit out of play:
// these cases are about the read set, not the bound.
const trackingReadSearchLimit = 10

// trackingReadOptions is the slice of each entry point's options struct that
// decides read-set participation.
type trackingReadOptions struct {
	trackingRead bool
	pointInTime  *time.Time
}

// trackingReader runs one entry point's read inside txCtx and returns the ids
// it handed back.
type trackingReader func(t *testing.T, txCtx spiCtx, es spi.EntityStore, mref spi.ModelRef, filter spi.Filter, opts trackingReadOptions) []string

func trackingReadViaIterate(t *testing.T, txCtx spiCtx, es spi.EntityStore, mref spi.ModelRef, filter spi.Filter, opts trackingReadOptions) []string {
	t.Helper()
	it, err := es.Iterate(txCtx, mref, filter, spi.IterateOptions{
		TrackingRead: opts.trackingRead,
		PointInTime:  opts.pointInTime,
	})
	require.NoError(t, err)
	got, err := drainIterator(t, it)
	require.NoError(t, err)
	return entityIDs(got)
}

func trackingReadViaSearch(t *testing.T, txCtx spiCtx, es spi.EntityStore, mref spi.ModelRef, filter spi.Filter, opts trackingReadOptions) []string {
	t.Helper()
	got, err := es.Search(txCtx, filter, spi.SearchOptions{
		ModelName:    mref.EntityName,
		ModelVersion: mref.ModelVersion,
		Limit:        trackingReadSearchLimit,
		TrackingRead: opts.trackingRead,
		PointInTime:  opts.pointInTime,
	})
	require.NoError(t, err)
	return entityIDs(got)
}

// trackingReadPredicate is one way of selecting the same single row out of the
// two seeded entities. Two shapes are run against the recording contract, and
// the pair is the point: a backend that translates the predicate into storage
// never scans the excluded row at all and satisfies the contract trivially,
// which is correct but proves nothing about how it records. The negated shape
// is the one no reference backend translates, so the excluded row is fetched
// and rejected in process — the layer where recording-too-early happens.
//
// Nothing obliges a backend to leave the negated shape alone. If one gains a
// translation for it, this pair stops discriminating there and the case needs
// a shape that planner still leaves alone. It stays a correct assertion
// either way; it just stops being a sharp one.
type trackingReadPredicate struct {
	name   string
	filter spi.Filter
}

func trackingReadPredicates() []trackingReadPredicate {
	return []trackingReadPredicate{
		{
			name: "Equality",
			filter: spi.Filter{
				Op:       spi.FilterEq,
				Source:   spi.SourceData,
				Path:     "status",
				Value:    searcherMatchValue,
				Declared: []spi.DataType{spi.String},
			},
		},
		{
			name: "Negated",
			filter: spi.Filter{
				Op: spi.FilterNot,
				Children: []spi.Filter{{
					Op:       spi.FilterEq,
					Source:   spi.SourceData,
					Path:     "status",
					Value:    searcherDecoyValue,
					Declared: []spi.DataType{spi.String},
				}},
			},
		},
	}
}

// runTrackingReadYieldedOnly drives both directions of the recording contract
// under each predicate shape. Both directions are required: conflicting on
// the yielded row must abort the tracking transaction, so a backend that
// records nothing cannot pass by under-recording either.
func runTrackingReadYieldedOnly(t *testing.T, h Harness, read trackingReader) {
	t.Helper()
	for _, p := range trackingReadPredicates() {
		t.Run(p.name, func(t *testing.T) {
			t.Run("ExcludedRowIsNotRecorded", func(t *testing.T) {
				trackingReadOutcome(t, h, read, trackingReadCase{
					opts:               trackingReadOptions{trackingRead: true},
					filter:             p.filter,
					wantCommitSucceeds: true,
					because:            "the excluded entity was never handed to the caller, so it must not be in the read set: a concurrent commit touching it must not abort this tx",
				})
			})
			t.Run("YieldedRowIsRecorded", func(t *testing.T) {
				trackingReadOutcome(t, h, read, trackingReadCase{
					opts:              trackingReadOptions{trackingRead: true},
					filter:            p.filter,
					conflictOnYielded: true,
					because:           "the yielded entity must be in the read set, so the conflicting concurrent commit aborts this tx",
				})
			})
		})
	}
}

// runTrackingReadDisabled pins the flag's default: a read with TrackingRead
// unset records nothing, not even the row it returned. Without this a backend
// that ignores the flag and always records passes every YieldedOnly case.
func runTrackingReadDisabled(t *testing.T, h Harness, read trackingReader) {
	t.Helper()
	trackingReadOutcome(t, h, read, trackingReadCase{
		opts:               trackingReadOptions{trackingRead: false},
		filter:             trackingReadPredicates()[0].filter,
		conflictOnYielded:  true,
		wantCommitSucceeds: true,
		because:            "TrackingRead unset must record nothing, so a concurrent commit touching even the returned entity must not abort this tx",
	})
}

// runTrackingReadPointInTime pins the one case where TrackingRead is set and
// still records nothing: a point-in-time read is committed-only and
// historical (see the PointInTime fields), and a read set can only carry the
// current committed version, which is not what an as-at read saw. The cutoff
// is deliberately in the future, so only committed-only routing — never the
// window — can be what the case observes.
func runTrackingReadPointInTime(t *testing.T, h Harness, read trackingReader) {
	t.Helper()
	trackingReadOutcome(t, h, read, trackingReadCase{
		opts:               trackingReadOptions{trackingRead: true},
		asAtFuture:         true,
		filter:             trackingReadPredicates()[0].filter,
		conflictOnYielded:  true,
		wantCommitSucceeds: true,
		because:            "a point-in-time read records nothing even with TrackingRead set, so a concurrent commit touching the returned entity must not abort this tx",
	})
}

// trackingReadCase is one read-set outcome: how the read was configured, and
// whether the tracking transaction survives a concurrent commit to the row it
// yielded (conflictOnYielded) or to the row the filter excluded.
type trackingReadCase struct {
	opts   trackingReadOptions
	filter spi.Filter
	// asAtFuture resolves opts.pointInTime after the fixture is committed —
	// it cannot be a literal on the case, because the instant must be past
	// the seed's own submit time.
	asAtFuture         bool
	conflictOnYielded  bool
	wantCommitSucceeds bool
	because            string
}

func trackingReadOutcome(t *testing.T, h Harness, read trackingReader, c trackingReadCase) {
	t.Helper()
	ctx := tenantContext(h.NewTenant())
	mref := spi.ModelRef{EntityName: trackingReadModel, ModelVersion: "1"}
	yieldedID, excludedID := newID(), newID()

	withTx(t, h, ctx, func(txCtx spiCtx) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		_, err = es.Save(txCtx, newEntity(t, mref.EntityName, yieldedID, map[string]any{"status": searcherMatchValue}))
		require.NoError(t, err)
		_, err = es.Save(txCtx, newEntity(t, mref.EntityName, excludedID, map[string]any{"status": searcherDecoyValue}))
		require.NoError(t, err)
	})

	opts := c.opts
	if c.asAtFuture {
		asAt := h.Now().UTC().Add(1 * time.Hour)
		opts.pointInTime = &asAt
	}

	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	tx := trackingTx{tm: tm}
	tx.id, tx.ctx = beginGuarded(t, tm, ctx)

	es, err := h.Factory.EntityStore(tx.ctx)
	require.NoError(t, err)

	// Both rows must be in this transaction's view before the predicate runs.
	// Otherwise "the excluded row was not recorded" could pass because the row
	// was never there to record — the fixture would be asserting nothing. The
	// guard reads through Iterate with TrackingRead unset, which records
	// nothing itself and so cannot stand in for the recording under test.
	all, err := es.Iterate(tx.ctx, mref, spi.Filter{}, spi.IterateOptions{})
	require.NoError(t, err)
	visible, err := drainIterator(t, all)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{yieldedID, excludedID}, entityIDs(visible),
		"both seeded entities must be visible to the transaction before the predicate runs (read here through Iterate with TrackingRead unset — a failure here is an Iterate defect, not a read-set one)")

	require.ElementsMatch(t, []string{yieldedID}, read(t, tx.ctx, es, mref, c.filter, opts),
		"the predicate selects exactly one of the two seeded entities")

	conflictID := excludedID
	if c.conflictOnYielded {
		conflictID = yieldedID
	}
	err = commitAfterConflictingWrite(t, h, ctx, mref, conflictID, tx)
	if c.wantCommitSucceeds {
		require.NoError(t, err, c.because)
		return
	}
	require.Error(t, err, c.because)
	require.ErrorIs(t, err, spi.ErrConflict)
}

// trackingTx is the transaction under test in a read-set case: the manager it
// was begun on, its id, and its context. Grouped because the three travel
// together and two loose context arguments of the same type are easy to
// transpose at a call site.
type trackingTx struct {
	tm  spi.TransactionManager
	id  string
	ctx spiCtx
}

// commitAfterConflictingWrite is the second half of every read-set case: an
// independent transaction overwrites conflictID and commits, then tx commits.
// The returned error is that second commit's — whether it survived is the
// only black-box read-out of what the first transaction's read recorded.
//
// ctx is the plain tenant context the independent transaction begins on, NOT
// tx.ctx: the two must not share a transaction, or there is no conflict to
// observe.
func commitAfterConflictingWrite(t *testing.T, h Harness, ctx spiCtx, mref spi.ModelRef, conflictID string, tx trackingTx) error {
	t.Helper()
	tm2, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	txID2, txCtx2, err := tm2.Begin(ctx)
	require.NoError(t, err)
	es, err := h.Factory.EntityStore(txCtx2)
	require.NoError(t, err)
	_, err = es.Save(txCtx2, newEntity(t, mref.EntityName, conflictID, map[string]any{"v": "conflict"}))
	require.NoError(t, err)
	require.NoError(t, tm2.Commit(txCtx2, txID2))
	return tx.tm.Commit(tx.ctx, tx.id)
}
