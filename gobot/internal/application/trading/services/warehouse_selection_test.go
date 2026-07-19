package services

import (
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// warehouseOpAt builds a RUNNING warehouse operation with id at waypoint, created at
// createdAt (via a MockClock pinned to that instant, so CreatedAt() is fully
// controllable for tie-break fixtures).
func warehouseOpAt(t *testing.T, id, waypoint string, createdAt time.Time) *storage.StorageOperation {
	t.Helper()
	op, err := storage.NewWarehouseOperation(id, 1, waypoint, []string{id + "-WH"}, []string{"FOOD"}, &shared.MockClock{CurrentTime: createdAt})
	if err != nil {
		t.Fatalf("warehouse op %s: %v", id, err)
	}
	if err := op.Start(); err != nil {
		t.Fatalf("start %s: %v", id, err)
	}
	return op
}

// TestSelectNewestRunningWarehouse_PicksNewestByCreatedAt pins the zombie-vs-live
// tie-break: a stopped warehouse whose storage_operations row was never terminalized
// can still surface as "RUNNING" alongside its live replacement at the same waypoint.
// The selector must resolve to the newer, live operation - not the older zombie - or
// a caller reads the dead operation's now-unregistered storage ships as zero free
// space and wrongly declares the warehouse full.
func TestSelectNewestRunningWarehouse_PicksNewestByCreatedAt(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	zombie := warehouseOpAt(t, "warehouse-TORWIND-12-bad719ff", "X1-TORWIND-12", t0)
	live := warehouseOpAt(t, "warehouse-TORWIND-12-3477282e", "X1-TORWIND-12", t0.Add(2*time.Hour))

	got := SelectNewestRunningWarehouse([]*storage.StorageOperation{zombie, live})

	if got == nil || got.ID() != live.ID() {
		t.Fatalf("want live op %s, got %v", live.ID(), got)
	}
}

// TestSelectNewestRunningWarehouse_ReverseOrderStillPicksNewest confirms the result
// doesn't depend on slice order - the repository's FindRunning query has no ORDER
// BY, so ordering can't be relied on to already put the newest op first.
func TestSelectNewestRunningWarehouse_ReverseOrderStillPicksNewest(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	zombie := warehouseOpAt(t, "warehouse-TORWIND-12-bad719ff", "X1-TORWIND-12", t0)
	live := warehouseOpAt(t, "warehouse-TORWIND-12-3477282e", "X1-TORWIND-12", t0.Add(2*time.Hour))

	got := SelectNewestRunningWarehouse([]*storage.StorageOperation{live, zombie})

	if got == nil || got.ID() != live.ID() {
		t.Fatalf("want live op %s, got %v", live.ID(), got)
	}
}

// TestSelectNewestRunningWarehouse_TieBreaksByHigherID covers the (unlikely but
// possible) exact-CreatedAt collision, where the ID comparison is the only
// remaining source of determinism.
func TestSelectNewestRunningWarehouse_TieBreaksByHigherID(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	a := warehouseOpAt(t, "warehouse-AAA", "X1-TORWIND-12", t0)
	b := warehouseOpAt(t, "warehouse-BBB", "X1-TORWIND-12", t0)

	got := SelectNewestRunningWarehouse([]*storage.StorageOperation{a, b})

	if got == nil || got.ID() != "warehouse-BBB" {
		t.Fatalf("want warehouse-BBB (higher id on an exact CreatedAt tie), got %v", got)
	}
}

// TestSelectNewestRunningWarehouse_EmptySliceReturnsNil is the fail-closed sanity
// check through the refactor: no candidates in means no warehouse out, same as
// today's behavior when nothing is RUNNING at all.
func TestSelectNewestRunningWarehouse_EmptySliceReturnsNil(t *testing.T) {
	if got := SelectNewestRunningWarehouse(nil); got != nil {
		t.Fatalf("want nil for empty slice, got %v", got)
	}
}
