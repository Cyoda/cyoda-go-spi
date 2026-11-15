package spitest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// The OneVersionPerTx cases pin the rule that, within one transaction, an
// entity has at most one pending version: a Save, CompareAndSave or Delete
// of an entity the transaction already wrote replaces that pending version
// instead of adding one, SaveAll and DeleteAll follow the rule per entity,
// and the commit records that one version. Its content is the last write's,
// its change type is DELETED when the last write is a delete and otherwise
// CREATED for the entity's first version and UPDATED after it, its number is
// the previous version's + 1, and its attribution is the last write's.
// A rollback to a savepoint restores each entity's pending version as it was
// at the savepoint, including no pending version.
//
// GetVersionByTransaction reads committed versions only, inside a transaction
// too, and returns the one version a transaction wrote.

const oneVersionModel = "m-one-version"

func runOneVersionSuite(t *testing.T, h Harness, tracker *skipTracker) {
	runSubtest(t, h, tracker, "OneVersionPerTx/SaveSave", testOneVersionSaveSave)
	runSubtest(t, h, tracker, "OneVersionPerTx/SaveCompareAndSave", testOneVersionSaveCompareAndSave)
	runSubtest(t, h, tracker, "OneVersionPerTx/DeleteThenSave_Existed", testOneVersionDeleteThenSaveExisted)
	runSubtest(t, h, tracker, "OneVersionPerTx/SaveThenDelete_Existed", testOneVersionSaveThenDeleteExisted)
	runSubtest(t, h, tracker, "OneVersionPerTx/CreateThenDelete", testOneVersionCreateThenDelete)
	runSubtest(t, h, tracker, "OneVersionPerTx/CreateThenSave", testOneVersionCreateThenSave)
	runSubtest(t, h, tracker, "OneVersionPerTx/SaveSaveDeleteSave", testOneVersionSaveSaveDeleteSave)
	runSubtest(t, h, tracker, "OneVersionPerTx/VersionNumberIsPreviousPlusOne", testOneVersionVersionNumber)
	runSubtest(t, h, tracker, "OneVersionPerTx/AttributionIsLastWrite", testOneVersionAttribution)
	runSubtest(t, h, tracker, "OneVersionPerTx/SavepointRestoresReplacedPending", testOneVersionSavepointRestoresReplaced)
	runSubtest(t, h, tracker, "OneVersionPerTx/SavepointRestoresPendingReplacedByDelete", testOneVersionSavepointRestoresReplacedByDelete)
	runSubtest(t, h, tracker, "OneVersionPerTx/SavepointRestoresNoPending", testOneVersionSavepointRestoresNone)
	runSubtest(t, h, tracker, "OneVersionPerTx/SavepointRollbackThenSaveAgain", testOneVersionSavepointThenSaveAgain)
	runSubtest(t, h, tracker, "OneVersionPerTx/GetVersionByTransaction/LastSaveWins", testOneVersionGetVersionByTransactionLastSaveWins)
	runSubtest(t, h, tracker, "OneVersionPerTx/GetVersionByTransaction/InsideWritingTxIsNotFound", testOneVersionGetVersionByTransactionInsideWritingTx)
	runSubtest(t, h, tracker, "OneVersionPerTx/SaveAllDeleteAll", testOneVersionSaveAllDeleteAll)
}

// oneVersion is the fixture of the OneVersionPerTx cases: a fresh tenant with
// the case's model created and locked.
type oneVersion struct {
	h      Harness
	tenant spi.TenantID
	ctx    spiCtx
	mref   spi.ModelRef
}

func newOneVersion(t *testing.T, h Harness) *oneVersion {
	t.Helper()
	tenant := h.NewTenant()
	ctx := tenantContext(tenant)
	mref := spi.ModelRef{EntityName: oneVersionModel, ModelVersion: "1"}
	EnsureLockedModel(t, h.Factory, ctx, mref)
	return &oneVersion{h: h, tenant: tenant, ctx: ctx, mref: mref}
}

// tx runs fn in one transaction, commits it and returns its id. The clock
// advances after the commit, so the next transaction's versions sort after it.
func (o *oneVersion) tx(t *testing.T, fn func(txID string, txCtx spiCtx, es spi.EntityStore)) string {
	t.Helper()
	tm, err := o.h.Factory.TransactionManager(o.ctx)
	require.NoError(t, err)
	txID, txCtx := beginGuarded(t, tm, o.ctx)
	es, err := o.h.Factory.EntityStore(txCtx)
	require.NoError(t, err)
	fn(txID, txCtx, es)
	require.NoError(t, tm.Commit(txCtx, txID))
	o.h.AdvanceClock(1 * time.Millisecond)
	return txID
}

// seed commits a version of a new entity in a transaction of its own and
// returns the entity's id.
func (o *oneVersion) seed(t *testing.T, payload map[string]any) string {
	t.Helper()
	id := newID()
	o.tx(t, func(_ string, txCtx spiCtx, es spi.EntityStore) {
		_, err := es.Save(txCtx, newEntity(t, oneVersionModel, id, payload))
		require.NoError(t, err)
	})
	return id
}

func (o *oneVersion) save(t *testing.T, txCtx spiCtx, es spi.EntityStore, id string, payload map[string]any) {
	t.Helper()
	_, err := es.Save(txCtx, newEntity(t, oneVersionModel, id, payload))
	require.NoError(t, err)
}

// history is the entity's committed versions, newest first.
func (o *oneVersion) history(t *testing.T, id string) []spi.EntityVersionMeta {
	t.Helper()
	es, err := o.h.Factory.EntityStore(o.ctx)
	require.NoError(t, err)
	metas, err := es.GetVersionMetadata(o.ctx, id, spi.VersionMetadataOptions{})
	require.NoError(t, err)
	return metas
}

// writtenBy returns the committed versions of id that transaction txID wrote.
func (o *oneVersion) writtenBy(t *testing.T, id, txID string) []spi.EntityVersionMeta {
	t.Helper()
	var out []spi.EntityVersionMeta
	for _, m := range o.history(t, id) {
		if m.TransactionID == txID {
			out = append(out, m)
		}
	}
	return out
}

// requireOneVersion asserts that transaction txID committed exactly one
// version of id, with the given change type, and returns it.
func (o *oneVersion) requireOneVersion(t *testing.T, id, txID, changeType, why string) spi.EntityVersionMeta {
	t.Helper()
	got := o.writtenBy(t, id, txID)
	require.Len(t, got, 1, "%s: the transaction must commit exactly one version of the entity, got %d", why, len(got))
	require.Equal(t, changeType, got[0].ChangeType, "%s: change type of the transaction's one version", why)
	require.Equal(t, changeType == "DELETED", got[0].Deleted, "%s: Deleted must follow the change type", why)
	return got[0]
}

func (o *oneVersion) requireContent(t *testing.T, id, field string, want int, why string) {
	t.Helper()
	es, err := o.h.Factory.EntityStore(o.ctx)
	require.NoError(t, err)
	got, err := es.Get(o.ctx, id)
	require.NoError(t, err, why)
	require.Equal(t, int64(want), gjsonInt(t, got, field), "%s: content must be the transaction's last write", why)
}

func (o *oneVersion) requireGone(t *testing.T, id, why string) {
	t.Helper()
	es, err := o.h.Factory.EntityStore(o.ctx)
	require.NoError(t, err)
	_, err = es.Get(o.ctx, id)
	require.ErrorIs(t, err, spi.ErrNotFound, why)
}

// save, save, save of a new entity: one CREATED version with the last save.
func testOneVersionSaveSave(t *testing.T, h Harness) {
	o := newOneVersion(t, h)
	id := newID()
	txID := o.tx(t, func(_ string, txCtx spiCtx, es spi.EntityStore) {
		o.save(t, txCtx, es, id, map[string]any{"v": 1})
		o.save(t, txCtx, es, id, map[string]any{"v": 2})
		o.save(t, txCtx, es, id, map[string]any{"v": 3})
	})
	o.requireOneVersion(t, id, txID, "CREATED", "save, save, save")
	require.Len(t, o.history(t, id), 1, "the entity's whole history is the one version")
	o.requireContent(t, id, "v", 3, "save, save, save")
}

// save, CompareAndSave naming the transaction's own id: one version, the
// CAS's content.
func testOneVersionSaveCompareAndSave(t *testing.T, h Harness) {
	o := newOneVersion(t, h)
	id := newID()
	txID := o.tx(t, func(txID string, txCtx spiCtx, es spi.EntityStore) {
		o.save(t, txCtx, es, id, map[string]any{"v": 1})
		_, err := es.CompareAndSave(txCtx, newEntity(t, oneVersionModel, id, map[string]any{"v": 2}), txID)
		require.NoError(t, err, "CompareAndSave naming the transaction's own id must succeed after its Save")
	})
	o.requireOneVersion(t, id, txID, "CREATED", "save, CompareAndSave")
	require.Len(t, o.history(t, id), 1, "the entity's whole history is the one version")
	o.requireContent(t, id, "v", 2, "save, CompareAndSave")
}

// delete, save of an entity that existed: one UPDATED version.
func testOneVersionDeleteThenSaveExisted(t *testing.T, h Harness) {
	o := newOneVersion(t, h)
	id := o.seed(t, map[string]any{"v": 1})
	txID := o.tx(t, func(_ string, txCtx spiCtx, es spi.EntityStore) {
		require.NoError(t, es.Delete(txCtx, id))
		o.save(t, txCtx, es, id, map[string]any{"v": 2})
	})
	o.requireOneVersion(t, id, txID, "UPDATED", "delete, save of an existing entity")
	require.Len(t, o.history(t, id), 2, "the seed version and the transaction's one version")
	o.requireContent(t, id, "v", 2, "delete, save of an existing entity")
}

// save, delete of an entity that existed: one DELETED version.
func testOneVersionSaveThenDeleteExisted(t *testing.T, h Harness) {
	o := newOneVersion(t, h)
	id := o.seed(t, map[string]any{"v": 1})
	txID := o.tx(t, func(_ string, txCtx spiCtx, es spi.EntityStore) {
		o.save(t, txCtx, es, id, map[string]any{"v": 2})
		require.NoError(t, es.Delete(txCtx, id))
	})
	o.requireOneVersion(t, id, txID, "DELETED", "save, delete of an existing entity")
	require.Len(t, o.history(t, id), 2, "the seed version and the transaction's one version")
	o.requireGone(t, id, "save, delete of an existing entity leaves it deleted")
}

// create, delete of an entity that did not exist: one DELETED version, the
// tombstone.
func testOneVersionCreateThenDelete(t *testing.T, h Harness) {
	o := newOneVersion(t, h)
	id := newID()
	txID := o.tx(t, func(_ string, txCtx spiCtx, es spi.EntityStore) {
		o.save(t, txCtx, es, id, map[string]any{"v": 1})
		require.NoError(t, es.Delete(txCtx, id))
	})
	o.requireOneVersion(t, id, txID, "DELETED", "create, delete")
	require.Len(t, o.history(t, id), 1, "the entity's whole history is the tombstone")
	o.requireGone(t, id, "create, delete leaves no entity")
}

// create, save: one CREATED version with the save's content.
func testOneVersionCreateThenSave(t *testing.T, h Harness) {
	o := newOneVersion(t, h)
	id := newID()
	txID := o.tx(t, func(_ string, txCtx spiCtx, es spi.EntityStore) {
		o.save(t, txCtx, es, id, map[string]any{"v": 1})
		o.save(t, txCtx, es, id, map[string]any{"v": 2})
	})
	got := o.requireOneVersion(t, id, txID, "CREATED", "create, save")
	require.Len(t, o.history(t, id), 1, "the entity's whole history is the one version")
	require.Equal(t, int64(1), got.Version, "the entity's first version is version 1")
	o.requireContent(t, id, "v", 2, "create, save")
}

// save, save, delete, save of an entity that existed: one UPDATED version
// with the last save's content.
func testOneVersionSaveSaveDeleteSave(t *testing.T, h Harness) {
	o := newOneVersion(t, h)
	id := o.seed(t, map[string]any{"v": 1})
	txID := o.tx(t, func(_ string, txCtx spiCtx, es spi.EntityStore) {
		o.save(t, txCtx, es, id, map[string]any{"v": 2})
		o.save(t, txCtx, es, id, map[string]any{"v": 3})
		require.NoError(t, es.Delete(txCtx, id))
		o.save(t, txCtx, es, id, map[string]any{"v": 4})
	})
	o.requireOneVersion(t, id, txID, "UPDATED", "save, save, delete, save of an existing entity")
	require.Len(t, o.history(t, id), 2, "the seed version and the transaction's one version")
	o.requireContent(t, id, "v", 4, "save, save, delete, save of an existing entity")
}

// An entity at version n that a transaction saves twice is at version n+1.
func testOneVersionVersionNumber(t *testing.T, h Harness) {
	o := newOneVersion(t, h)
	id := o.seed(t, map[string]any{"v": 1})
	o.tx(t, func(_ string, txCtx spiCtx, es spi.EntityStore) {
		o.save(t, txCtx, es, id, map[string]any{"v": 2})
	})
	before := o.history(t, id)
	require.Len(t, before, 2)
	n := before[0].Version

	txID := o.tx(t, func(_ string, txCtx spiCtx, es spi.EntityStore) {
		o.save(t, txCtx, es, id, map[string]any{"v": 3})
		o.save(t, txCtx, es, id, map[string]any{"v": 4})
	})
	got := o.requireOneVersion(t, id, txID, "UPDATED", "save, save of an entity at version n")
	require.Equal(t, n+1, got.Version, "one transaction takes one version number: n+1")

	es, err := h.Factory.EntityStore(o.ctx)
	require.NoError(t, err)
	cur, err := es.Get(o.ctx, id)
	require.NoError(t, err)
	require.Equal(t, n+1, cur.Meta.Version, "the current entity is at version n+1")

	next := o.tx(t, func(_ string, txCtx spiCtx, es spi.EntityStore) {
		o.save(t, txCtx, es, id, map[string]any{"v": 5})
	})
	got = o.requireOneVersion(t, id, next, "UPDATED", "the next transaction's save")
	require.Equal(t, n+2, got.Version, "the next transaction takes the next number: n+2")
}

// The one version carries the last write's attribution: a save's from the
// entity it writes, a delete's from the caller.
func testOneVersionAttribution(t *testing.T, h Harness) {
	o := newOneVersion(t, h)
	alice := spi.Principal{ID: "alice", Kind: spi.PrincipalUser}
	bob := spi.Principal{ID: "bob", Kind: spi.PrincipalService}
	as := func(txCtx spiCtx, p spi.Principal) spiCtx {
		return spi.WithUserContext(txCtx, spi.GetUserContext(tenantContextAs(o.tenant, p.ID, p.Kind)))
	}

	saved := newID()
	deleted := o.seed(t, map[string]any{"v": 1})
	txID := o.tx(t, func(_ string, txCtx spiCtx, es spi.EntityStore) {
		aCtx, bCtx := as(txCtx, alice), as(txCtx, bob)
		_, err := es.Save(aCtx, newAttributedEntity(t, oneVersionModel, saved, map[string]any{"v": 1}, alice.ID, alice.Kind, alice))
		require.NoError(t, err)
		_, err = es.Save(bCtx, newAttributedEntity(t, oneVersionModel, saved, map[string]any{"v": 2}, bob.ID, bob.Kind, bob))
		require.NoError(t, err)

		_, err = es.Save(aCtx, newAttributedEntity(t, oneVersionModel, deleted, map[string]any{"v": 2}, alice.ID, alice.Kind, alice))
		require.NoError(t, err)
		require.NoError(t, es.Delete(bCtx, deleted))
	})

	got := o.requireOneVersion(t, saved, txID, "CREATED", "save by alice, save by bob")
	require.Equal(t, bob.ID, got.User, "the version's user is the last save's")
	require.Equal(t, bob.Kind, got.AttributedKind, "the version's attributed kind is the last save's")
	require.Equal(t, bob, got.Executor, "the version's executor is the last save's")

	got = o.requireOneVersion(t, deleted, txID, "DELETED", "save by alice, delete by bob")
	require.Equal(t, bob.ID, got.User, "the tombstone's user is the deleting caller")
	require.Equal(t, bob.Kind, got.AttributedKind, "the tombstone's attributed kind is the deleting caller's")
	require.Equal(t, bob, got.Executor, "the tombstone's executor is the deleting caller")
}

// save v1, savepoint, save v2, rollback to the savepoint: the commit records
// one version with v1.
func testOneVersionSavepointRestoresReplaced(t *testing.T, h Harness) {
	o := newOneVersion(t, h)
	tm, err := h.Factory.TransactionManager(o.ctx)
	require.NoError(t, err)
	id := newID()
	txID := o.tx(t, func(txID string, txCtx spiCtx, es spi.EntityStore) {
		o.save(t, txCtx, es, id, map[string]any{"v": 1})
		sp, err := tm.Savepoint(txCtx, txID)
		require.NoError(t, err)
		o.save(t, txCtx, es, id, map[string]any{"v": 2})
		require.NoError(t, tm.RollbackToSavepoint(txCtx, txID, sp))
	})
	o.requireOneVersion(t, id, txID, "CREATED", "save v1, savepoint, save v2, rollback to the savepoint")
	require.Len(t, o.history(t, id), 1, "the entity's whole history is the one version")
	o.requireContent(t, id, "v", 1, "the rollback restores v1 as the pending version")
}

// save v1, savepoint, delete, rollback to the savepoint: the rollback restores
// v1 as the pending version in place of the tombstone, so the commit records
// one version with v1 — CREATED for a new entity, UPDATED for one that
// existed.
func testOneVersionSavepointRestoresReplacedByDelete(t *testing.T, h Harness) {
	o := newOneVersion(t, h)
	tm, err := h.Factory.TransactionManager(o.ctx)
	require.NoError(t, err)
	existing := o.seed(t, map[string]any{"v": 0})
	fresh := newID()
	txID := o.tx(t, func(txID string, txCtx spiCtx, es spi.EntityStore) {
		o.save(t, txCtx, es, fresh, map[string]any{"v": 1})
		o.save(t, txCtx, es, existing, map[string]any{"v": 1})
		sp, err := tm.Savepoint(txCtx, txID)
		require.NoError(t, err)
		require.NoError(t, es.Delete(txCtx, fresh))
		require.NoError(t, es.Delete(txCtx, existing))
		require.NoError(t, tm.RollbackToSavepoint(txCtx, txID, sp))
	})

	o.requireOneVersion(t, fresh, txID, "CREATED", "new entity: save v1, savepoint, delete, rollback to the savepoint")
	require.Len(t, o.history(t, fresh), 1, "the new entity's whole history is the one version")
	o.requireContent(t, fresh, "v", 1, "the rollback restores the new entity's v1 as the pending version")

	o.requireOneVersion(t, existing, txID, "UPDATED", "existing entity: save v1, savepoint, delete, rollback to the savepoint")
	require.Len(t, o.history(t, existing), 2, "the seed version and the transaction's one version")
	o.requireContent(t, existing, "v", 1, "the rollback restores the existing entity's v1 as the pending version")
}

// savepoint, save, rollback to the savepoint: the commit records no version
// of the entity, whether it existed before or not.
func testOneVersionSavepointRestoresNone(t *testing.T, h Harness) {
	o := newOneVersion(t, h)
	tm, err := h.Factory.TransactionManager(o.ctx)
	require.NoError(t, err)
	existing := o.seed(t, map[string]any{"v": 1})
	fresh := newID()
	txID := o.tx(t, func(txID string, txCtx spiCtx, es spi.EntityStore) {
		sp, err := tm.Savepoint(txCtx, txID)
		require.NoError(t, err)
		o.save(t, txCtx, es, existing, map[string]any{"v": 2})
		o.save(t, txCtx, es, existing, map[string]any{"v": 3})
		o.save(t, txCtx, es, fresh, map[string]any{"v": 1})
		require.NoError(t, tm.RollbackToSavepoint(txCtx, txID, sp))
	})
	require.Empty(t, o.writtenBy(t, existing, txID), "a rolled-back write must leave no version")
	require.Len(t, o.history(t, existing), 1, "the existing entity keeps only its seed version")
	o.requireContent(t, existing, "v", 1, "the existing entity keeps its committed content")

	es, err := h.Factory.EntityStore(o.ctx)
	require.NoError(t, err)
	_, err = es.GetVersionMetadata(o.ctx, fresh, spi.VersionMetadataOptions{})
	require.ErrorIs(t, err, spi.ErrNotFound, "a rolled-back create must leave no version history")
}

// savepoint, save, rollback to the savepoint, save again: the rollback left
// no pending version, so the save after it is the transaction's one version —
// a new version, not a rewrite of the entity's committed one.
func testOneVersionSavepointThenSaveAgain(t *testing.T, h Harness) {
	o := newOneVersion(t, h)
	tm, err := h.Factory.TransactionManager(o.ctx)
	require.NoError(t, err)
	existing := o.seed(t, map[string]any{"v": 1})
	seeded := o.history(t, existing)
	require.Len(t, seeded, 1)
	fresh := newID()
	txID := o.tx(t, func(txID string, txCtx spiCtx, es spi.EntityStore) {
		sp, err := tm.Savepoint(txCtx, txID)
		require.NoError(t, err)
		o.save(t, txCtx, es, existing, map[string]any{"v": 2})
		o.save(t, txCtx, es, fresh, map[string]any{"v": 2})
		require.NoError(t, tm.RollbackToSavepoint(txCtx, txID, sp))
		o.save(t, txCtx, es, existing, map[string]any{"v": 3})
		o.save(t, txCtx, es, fresh, map[string]any{"v": 3})
	})

	got := o.requireOneVersion(t, existing, txID, "UPDATED", "savepoint, save, rollback, save of an existing entity")
	require.Equal(t, seeded[0].Version+1, got.Version, "the save after the rollback takes the next version number")
	history := o.history(t, existing)
	require.Len(t, history, 2, "the seed version and the transaction's one version")
	require.Equal(t, seeded[0].TransactionID, history[1].TransactionID, "the committed seed version is left as it was")
	require.Equal(t, seeded[0].Version, history[1].Version, "the committed seed version is left as it was")
	require.Equal(t, seeded[0].ChangeType, history[1].ChangeType, "the committed seed version is left as it was")
	o.requireContent(t, existing, "v", 3, "savepoint, save, rollback, save of an existing entity")

	got = o.requireOneVersion(t, fresh, txID, "CREATED", "savepoint, save, rollback, save of a new entity")
	require.Equal(t, int64(1), got.Version, "the entity's first version is version 1")
	require.Len(t, o.history(t, fresh), 1, "the entity's whole history is the one version")
	o.requireContent(t, fresh, "v", 3, "savepoint, save, rollback, save of a new entity")
}

// Two saves in one transaction: GetVersionByTransaction returns the second
// payload, the transaction's one version.
func testOneVersionGetVersionByTransactionLastSaveWins(t *testing.T, h Harness) {
	o := newOneVersion(t, h)
	id := newID()
	txID := o.tx(t, func(_ string, txCtx spiCtx, es spi.EntityStore) {
		o.save(t, txCtx, es, id, map[string]any{"v": 1})
		o.save(t, txCtx, es, id, map[string]any{"v": 2})
	})
	got := o.requireOneVersion(t, id, txID, "CREATED", "save, save")

	es, err := h.Factory.EntityStore(o.ctx)
	require.NoError(t, err)
	v, err := es.GetVersionByTransaction(o.ctx, id, txID)
	require.NoError(t, err)
	require.NotNil(t, v)
	require.NotNil(t, v.Entity)
	require.Equal(t, int64(2), gjsonInt(t, v.Entity, "v"),
		"GetVersionByTransaction must return the transaction's one version: its last save")
	require.Equal(t, got.Version, v.Version, "the returned version is the one GetVersionMetadata lists")
}

// GetVersionByTransaction reads committed versions only: inside the writing
// transaction, after its save, the transaction's own id is not found, from
// the transaction and from outside it. After the commit it is found.
func testOneVersionGetVersionByTransactionInsideWritingTx(t *testing.T, h Harness) {
	o := newOneVersion(t, h)
	existing := o.seed(t, map[string]any{"v": 1})
	fresh := newID()
	outside, err := h.Factory.EntityStore(o.ctx)
	require.NoError(t, err)
	txID := o.tx(t, func(txID string, txCtx spiCtx, es spi.EntityStore) {
		o.save(t, txCtx, es, existing, map[string]any{"v": 2})
		o.save(t, txCtx, es, fresh, map[string]any{"v": 1})
		for _, id := range []string{existing, fresh} {
			_, err := es.GetVersionByTransaction(txCtx, id, txID)
			require.ErrorIs(t, err, spi.ErrNotFound,
				"inside the writing transaction its own uncommitted version must not be found")
			_, err = outside.GetVersionByTransaction(o.ctx, id, txID)
			require.ErrorIs(t, err, spi.ErrNotFound,
				"outside the writing transaction its uncommitted version must not be found")
		}
	})
	for id, want := range map[string]int{existing: 2, fresh: 1} {
		v, err := outside.GetVersionByTransaction(o.ctx, id, txID)
		require.NoError(t, err, "after the commit the transaction's version is found")
		require.Equal(t, int64(want), gjsonInt(t, v.Entity, "v"))
	}
}

// SaveAll follows the rule per entity, and so does DeleteAll.
func testOneVersionSaveAllDeleteAll(t *testing.T, h Harness) {
	o := newOneVersion(t, h)
	a, b := newID(), newID()
	tx1 := o.tx(t, func(_ string, txCtx spiCtx, es spi.EntityStore) {
		_, err := es.SaveAll(txCtx, iterSeq([]*spi.Entity{
			newEntity(t, oneVersionModel, a, map[string]any{"v": 1}),
			newEntity(t, oneVersionModel, b, map[string]any{"v": 1}),
		}))
		require.NoError(t, err)
		_, err = es.SaveAll(txCtx, iterSeq([]*spi.Entity{
			newEntity(t, oneVersionModel, a, map[string]any{"v": 2}),
		}))
		require.NoError(t, err)
	})
	o.requireOneVersion(t, a, tx1, "CREATED", "SaveAll [a, b], SaveAll [a']: a")
	o.requireOneVersion(t, b, tx1, "CREATED", "SaveAll [a, b], SaveAll [a']: b")
	o.requireContent(t, a, "v", 2, "SaveAll [a, b], SaveAll [a']: a")
	o.requireContent(t, b, "v", 1, "SaveAll [a, b], SaveAll [a']: b")

	c := newID()
	tx2 := o.tx(t, func(_ string, txCtx spiCtx, es spi.EntityStore) {
		_, err := es.SaveAll(txCtx, iterSeq([]*spi.Entity{
			newEntity(t, oneVersionModel, a, map[string]any{"v": 3}),
			newEntity(t, oneVersionModel, b, map[string]any{"v": 3}),
			newEntity(t, oneVersionModel, c, map[string]any{"v": 3}),
		}))
		require.NoError(t, err)
		require.NoError(t, es.DeleteAll(txCtx, o.mref))
	})
	for name, id := range map[string]string{"a": a, "b": b, "c": c} {
		o.requireOneVersion(t, id, tx2, "DELETED", "SaveAll then DeleteAll: "+name)
		o.requireGone(t, id, "SaveAll then DeleteAll: "+name)
	}
	require.Len(t, o.history(t, a), 2, "a: tx1's version and tx2's tombstone")
	require.Len(t, o.history(t, b), 2, "b: tx1's version and tx2's tombstone")
	require.Len(t, o.history(t, c), 1, "c: created and deleted in tx2, one tombstone")
}

// gjsonInt reads a top-level integer field out of an entity's payload.
func gjsonInt(t *testing.T, e *spi.Entity, field string) int64 {
	t.Helper()
	r := gjson.GetBytes(e.Data, field)
	require.True(t, r.Exists(), "entity %s has no %q field", e.Meta.ID, field)
	return r.Int()
}
