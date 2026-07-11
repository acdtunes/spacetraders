package services

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// C1 (sp-64je) — BuildStockSources offers warehouse stock with a KNOWN cost basis
// as zero-ask-at-basis withdrawal sources, fails closed on unknown basis, respects
// the tour graph, and nets outstanding cross-tour reservations.

type stubStockFinder struct {
	ops []*storage.StorageOperation
	err error
}

func (f *stubStockFinder) FindRunning(_ context.Context, _ int) ([]*storage.StorageOperation, error) {
	return f.ops, f.err
}

type stubStockReader struct {
	basis     map[string]map[string]int // opID -> good -> basis (absent = unknown)
	available map[string]map[string]int // opID -> good -> units
}

func (r *stubStockReader) GetTotalCargoAvailable(operationID, good string) int {
	return r.available[operationID][good]
}
func (r *stubStockReader) GetCostBasis(operationID, good string) (int, bool) {
	if byGood, ok := r.basis[operationID]; ok {
		if b, ok := byGood[good]; ok {
			return b, true
		}
	}
	return 0, false
}

type stubStockReservations map[string]int // "waypoint|good" -> outstanding

func (s stubStockReservations) OutstandingStock(waypoint, good string) int {
	return s[waypoint+"|"+good]
}

func mustWarehouse(t *testing.T, id, waypoint string, goods []string) *storage.StorageOperation {
	t.Helper()
	op, err := storage.NewWarehouseOperation(id, 1, waypoint, []string{"HULL-1"}, goods, nil)
	if err != nil {
		t.Fatalf("NewWarehouseOperation: %v", err)
	}
	return op
}

// Only goods with a KNOWN basis and available units are offered; an unknown-basis
// good (contract/gas stock) is fail-closed.
func TestBuildStockSources_KnownBasisOnly(t *testing.T) {
	op := mustWarehouse(t, "wh-1", "X1-FAC-A1", []string{"CLOTHING", "EQUIPMENT"})
	finder := &stubStockFinder{ops: []*storage.StorageOperation{op}}
	reader := &stubStockReader{
		basis:     map[string]map[string]int{"wh-1": {"CLOTHING": 100}}, // EQUIPMENT unknown
		available: map[string]map[string]int{"wh-1": {"CLOTHING": 40, "EQUIPMENT": 30}},
	}

	got := BuildStockSources(context.Background(), finder, reader, nil, []string{"X1-FAC"}, 1)
	if len(got) != 1 {
		t.Fatalf("expected 1 stock source (CLOTHING only), got %d: %+v", len(got), got)
	}
	s := got[0]
	if s.Good != "CLOTHING" || s.UnitsAvailable != 40 || s.UnitAsk != 100 ||
		s.StorageWaypoint != "X1-FAC-A1" || s.StorageSystem != "X1-FAC" {
		t.Fatalf("stock source wrong: %+v", s)
	}
}

// A warehouse outside the tour graph is excluded (unreachable, fail closed).
func TestBuildStockSources_OutOfGraphExcluded(t *testing.T) {
	op := mustWarehouse(t, "wh-1", "X1-OTHER-B2", []string{"CLOTHING"})
	finder := &stubStockFinder{ops: []*storage.StorageOperation{op}}
	reader := &stubStockReader{
		basis:     map[string]map[string]int{"wh-1": {"CLOTHING": 100}},
		available: map[string]map[string]int{"wh-1": {"CLOTHING": 40}},
	}

	got := BuildStockSources(context.Background(), finder, reader, nil, []string{"X1-FAC"}, 1)
	if len(got) != 0 {
		t.Fatalf("expected no stock sources for an out-of-graph warehouse, got %+v", got)
	}
}

// Outstanding cross-tour reservations are netted out of the offered units; a fully
// reserved good is dropped.
func TestBuildStockSources_NetsReservations(t *testing.T) {
	op := mustWarehouse(t, "wh-1", "X1-FAC-A1", []string{"CLOTHING", "MEDICINE"})
	finder := &stubStockFinder{ops: []*storage.StorageOperation{op}}
	reader := &stubStockReader{
		basis:     map[string]map[string]int{"wh-1": {"CLOTHING": 100, "MEDICINE": 200}},
		available: map[string]map[string]int{"wh-1": {"CLOTHING": 40, "MEDICINE": 10}},
	}
	res := stubStockReservations{
		"X1-FAC-A1|CLOTHING": 15, // 40 - 15 = 25 offered
		"X1-FAC-A1|MEDICINE": 10, // fully reserved -> dropped
	}

	got := BuildStockSources(context.Background(), finder, reader, res, []string{"X1-FAC"}, 1)
	if len(got) != 1 || got[0].Good != "CLOTHING" || got[0].UnitsAvailable != 25 {
		t.Fatalf("expected CLOTHING with 25 units after netting, got %+v", got)
	}
}

// A finder error yields no sources (fail closed), never a panic.
func TestBuildStockSources_FinderError(t *testing.T) {
	finder := &stubStockFinder{err: context.DeadlineExceeded}
	reader := &stubStockReader{}
	got := BuildStockSources(context.Background(), finder, reader, nil, []string{"X1-FAC"}, 1)
	if len(got) != 0 {
		t.Fatalf("expected no sources on finder error, got %+v", got)
	}
}
