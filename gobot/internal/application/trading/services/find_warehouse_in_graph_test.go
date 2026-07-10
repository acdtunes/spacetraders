package services

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// TestFindWarehouseInGraph_PrefersNewestOverLowerLexicographicID is the regression
// pin for sp-3lj5 at the tour deposit-candidate seam (Lane C). findWarehouseInGraph
// used to break ties between multiple RUNNING rows at the same graph-reachable
// waypoint by lowest lexicographic ID - arbitrary, and wrong whenever a stale
// "zombie" row (a warehouse container stopped without its storage_operations row
// being terminalized) happens to sort below its live replacement. Here the zombie's
// ID sorts lower ("warehouse-aaa...") than the live op's ("warehouse-zzz...") even
// though the zombie is older, so the old lowest-ID logic picks the dead operation.
// (The literal incident IDs, bad719ff/3477282e, are not used here because they
// coincidentally already sort correctly under the old logic - see
// TestFindWarehouseInGraph_ExactIncidentIDsResolveToLiveOperation below for that
// literal shape, and warehouse_selection_test.go for where the ID pair actually
// pins the bug against a naive picker.)
func TestFindWarehouseInGraph_PrefersNewestOverLowerLexicographicID(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	zombie := warehouseOpAt(t, "warehouse-aaaaaaaa", "X1-TORWIND-12", t0)
	live := warehouseOpAt(t, "warehouse-zzzzzzzz", "X1-TORWIND-12", t0.Add(2*time.Hour))
	finder := &fakeWarehouseFinder{ops: []*storage.StorageOperation{zombie, live}}

	got, _, _, err := findWarehouseInGraph(context.Background(), finder, []string{"X1-TORWIND"}, 1)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.ID() != live.ID() {
		t.Fatalf("want live op %s, got %v", live.ID(), got)
	}
}

// TestFindWarehouseInGraph_ExactIncidentIDsResolveToLiveOperation reproduces the
// literal sp-3lj5 incident shape at this seam: warehouse-TORWIND-12-bad719ff
// (stopped at 15:24Z, zombie row never terminalized) alongside
// warehouse-TORWIND-12-3477282e (the live replacement). Confirms the deposit
// candidate path resolves to the live operation for the exact IDs seen in
// production.
func TestFindWarehouseInGraph_ExactIncidentIDsResolveToLiveOperation(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	zombie := warehouseOpAt(t, "warehouse-TORWIND-12-bad719ff", "X1-TORWIND-12", t0)
	live := warehouseOpAt(t, "warehouse-TORWIND-12-3477282e", "X1-TORWIND-12", t0.Add(2*time.Hour))
	finder := &fakeWarehouseFinder{ops: []*storage.StorageOperation{zombie, live}}

	got, _, _, err := findWarehouseInGraph(context.Background(), finder, []string{"X1-TORWIND"}, 1)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.ID() != live.ID() {
		t.Fatalf("want live op %s, got %v", live.ID(), got)
	}
}

// TestFindWarehouseInGraph_NoRunningOpsReturnsNil is the fail-closed sanity check
// through the refactor: with nothing RUNNING in the graph (e.g. the only warehouse
// at the waypoint is now properly terminalized-stopped, so FindRunning no longer
// returns it), the deposit path finds no warehouse and returns nil with no error -
// the caller then offers no deposit candidates rather than guessing.
func TestFindWarehouseInGraph_NoRunningOpsReturnsNil(t *testing.T) {
	finder := &fakeWarehouseFinder{ops: nil}

	got, _, matches, err := findWarehouseInGraph(context.Background(), finder, []string{"X1-TORWIND"}, 1)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("want nil warehouse, got %v", got)
	}
	if matches != 0 {
		t.Fatalf("want 0 matches, got %d", matches)
	}
}

// TestFindWarehouseInGraph_ReportsCollisionCount confirms the caller-visible
// matches count reflects the number of RUNNING operations that collided in the
// graph filter, so BuildDepositCandidates can escalate its verdict line to WARNING
// on exactly the sp-3lj5 zombie-row scenario (matches > 1) without a separate log
// statement here.
func TestFindWarehouseInGraph_ReportsCollisionCount(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	zombie := warehouseOpAt(t, "warehouse-TORWIND-12-bad719ff", "X1-TORWIND-12", t0)
	live := warehouseOpAt(t, "warehouse-TORWIND-12-3477282e", "X1-TORWIND-12", t0.Add(2*time.Hour))
	finder := &fakeWarehouseFinder{ops: []*storage.StorageOperation{zombie, live}}

	_, _, matches, err := findWarehouseInGraph(context.Background(), finder, []string{"X1-TORWIND"}, 1)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if matches != 2 {
		t.Fatalf("want 2 colliding matches, got %d", matches)
	}
}
