package spi

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Helpers for ScheduledTaskStore implementations. A backend that chooses its
// claims in Go, or validates its input before it writes, uses these so that
// every backend applies the same rules. A backend that meets a rule in its
// query language instead need not call them.

// MaxTaskErrorBytes is the most a recorded scheduled-task error text
// (Attempt.Error, Failure.Error) may take, in bytes.
const MaxTaskErrorBytes = 1024

// SelectClaims picks the tasks one ScheduledTaskStore.ClaimDue call takes from
// cands, the tasks the store found claimable. At most one task per entity; at
// most req.PerTenantLimit - req.TenantInProgress[tenant] per tenant; at most
// req.Limit in all. Within a tenant the order is (NextAttemptTime, ID).
// Tenants take turns, one task per turn, so each tenant's first task comes
// before any tenant's second; the tenant with the earliest candidate goes
// first, ties broken by tenant id. SelectClaims sorts cands in place.
func SelectClaims(cands []ScheduledTask, req ClaimRequest) []ScheduledTask {
	sort.Slice(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.NextAttemptTime != b.NextAttemptTime {
			return a.NextAttemptTime < b.NextAttemptTime
		}
		if a.TenantID != b.TenantID {
			return a.TenantID < b.TenantID
		}
		return a.ID < b.ID
	})
	var tenants []TenantID
	queues := make(map[TenantID][]ScheduledTask)
	for _, c := range cands {
		if _, ok := queues[c.TenantID]; !ok {
			tenants = append(tenants, c.TenantID)
		}
		queues[c.TenantID] = append(queues[c.TenantID], c)
	}
	quota := make(map[TenantID]int, len(tenants))
	for _, tn := range tenants {
		quota[tn] = req.PerTenantLimit - req.TenantInProgress[tn]
	}

	type entityKey struct {
		tenant TenantID
		id     string
	}
	seen := make(map[entityKey]bool)
	var out []ScheduledTask
	for progress := true; progress && len(out) < req.Limit; {
		progress = false
		for _, tn := range tenants {
			if len(out) >= req.Limit {
				break
			}
			for quota[tn] > 0 && len(queues[tn]) > 0 {
				c := queues[tn][0]
				queues[tn] = queues[tn][1:]
				ek := entityKey{tenant: tn, id: c.EntityID}
				if seen[ek] {
					continue
				}
				seen[ek] = true
				quota[tn]--
				out = append(out, c)
				progress = true
				break
			}
		}
	}
	return out
}

// ValidateTaskErrorText refuses an error text that no backend stores as
// given: over MaxTaskErrorBytes, not valid UTF-8, or holding a NUL
// (PostgreSQL refuses the last two with SQLSTATE 22021). The error satisfies
// errors.Is(err, ErrStoreRejected) and never repeats the text, which may
// carry anything a compute node sent.
func ValidateTaskErrorText(s string) error {
	switch {
	case len(s) > MaxTaskErrorBytes:
		return fmt.Errorf("scheduled task error text is %d bytes, over %d: %w", len(s), MaxTaskErrorBytes, ErrStoreRejected)
	case !utf8.ValidString(s):
		return fmt.Errorf("scheduled task error text is not valid UTF-8: %w", ErrStoreRejected)
	case strings.IndexByte(s, 0) >= 0:
		return fmt.Errorf("scheduled task error text contains NUL: %w", ErrStoreRejected)
	}
	return nil
}

// ValidateFailureReason refuses a reason that is not one of the five
// ScheduledTaskFailureReason constants, with an error that satisfies
// errors.Is(err, ErrStoreRejected).
func ValidateFailureReason(r ScheduledTaskFailureReason) error {
	switch r {
	case FailureUnsafeWorkNotCompleted, FailureOwnerLostRepeatedly,
		FailureExpiredAfterFailedAttempts, FailureRunPanicked,
		FailureStoppedAfterPartialCommit:
		return nil
	}
	return fmt.Errorf("scheduled task failure reason is not a known reason: %w", ErrStoreRejected)
}

// ValidateArm refuses a ReconcileRequest whose Arm names a task without an
// id, or whose Arm and Cancel both name the same id — a caller defect: Arm
// means "this task's life continues" and Cancel means "remove it", and a
// request cannot mean both for the same id. Either refusal satisfies
// errors.Is(err, ErrStoreRejected). The tenant and the entity come from the
// request (see ReconcileRequest.Arm), so an Arm item's own fields for them
// are not checked.
func ValidateArm(req ReconcileRequest) error {
	for _, a := range req.Arm {
		if a.ID == "" {
			return fmt.Errorf("scheduled task arm for entity %s names a task without an id: %w", req.EntityID, ErrStoreRejected)
		}
	}
	cancelled := make(map[string]bool, len(req.Cancel))
	for _, id := range req.Cancel {
		cancelled[id] = true
	}
	for _, a := range req.Arm {
		if cancelled[a.ID] {
			return fmt.Errorf("scheduled task %s is in both Arm and Cancel for entity %s: %w", a.ID, req.EntityID, ErrStoreRejected)
		}
	}
	return nil
}

// ValidateClaimRequest refuses a ClaimRequest whose Limit or PerTenantLimit
// is below 1, or whose TenantInProgress holds a negative count, with an error
// that satisfies errors.Is(err, ErrStoreRejected). Every
// ScheduledTaskStore.ClaimDue calls it before it claims anything, so a
// tenant's quota, PerTenantLimit - TenantInProgress[tenant], is never above
// PerTenantLimit.
func ValidateClaimRequest(req ClaimRequest) error {
	if req.Limit < 1 || req.PerTenantLimit < 1 {
		return fmt.Errorf("claim due scheduled tasks: Limit and PerTenantLimit must be >= 1, got %d and %d: %w",
			req.Limit, req.PerTenantLimit, ErrStoreRejected)
	}
	for tenant, n := range req.TenantInProgress {
		if n < 0 {
			return fmt.Errorf("claim due scheduled tasks: TenantInProgress for tenant %s is %d, below 0: %w",
				tenant, n, ErrStoreRejected)
		}
	}
	return nil
}

// ValidateScheduledTaskQuery refuses a ScheduledTaskQuery whose Limit is
// below 1, with an error that satisfies errors.Is(err, ErrStoreRejected).
// Every ScheduledTaskStore.Query calls it before it reads a page. The upper
// bound of Limit is the caller's to enforce (see ScheduledTaskQuery.Limit).
func ValidateScheduledTaskQuery(q ScheduledTaskQuery) error {
	if q.Limit < 1 {
		return fmt.Errorf("query scheduled tasks: Limit must be >= 1, got %d: %w", q.Limit, ErrStoreRejected)
	}
	return nil
}
