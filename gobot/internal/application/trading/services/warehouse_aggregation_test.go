package services

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// coLocatedWarehouse builds a RUNNING warehouse op created at createdAt (MockClock,
// so the newest tie-break is controllable) with a single storage hull of the given
// capacity pre-seeded with stocked, and returns the op plus its storage ship. Wire
// the ships into a fakeSpaceReader keyed by op ID to read aggregate free space /
// stock across a co-located group.
func coLocatedWarehouse(t *testing.T, id, waypoint string, createdAt time.Time, capacity int, goods []string, stocked map[string]int) (*storage.StorageOperation, *storage.StorageShip) {
	t.Helper()
	op, err := storage.NewWarehouseOperation(id, 1, waypoint, []string{id + "-WH"}, goods, &shared.MockClock{CurrentTime: createdAt})
	if err != nil {
		t.Fatalf("warehouse op %s: %v", id, err)
	}
	if err := op.Start(); err != nil {
		t.Fatalf("start %s: %v", id, err)
	}
	ship, err := storage.NewStorageShip(id+"-WH", waypoint, id, capacity, stocked)
	if err != nil {
		t.Fatalf("storage ship %s: %v", id, err)
	}
	return op, ship
}

func readerFor(ships ...*storage.StorageShip) *fakeSpaceReader {
	m := map[string][]*storage.StorageShip{}
	for _, s := range ships {
		m[s.OperationID()] = append(m[s.OperationID()], s)
	}
	return &fakeSpaceReader{ships: m}
}

// TestTotalFreeSpace_SumsAcrossCoLocatedGroup pins the additive-capacity core
// (sp-5q2c): two warehouses at one waypoint (light-12's 80 + heavy-4B's 225) read as
// 305 aggregate slots, not the newest-only 225.
func TestTotalFreeSpace_SumsAcrossCoLocatedGroup(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	light, lightShip := coLocatedWarehouse(t, "light-12", "X1-KA42-E42", t0, 80, allGoods, nil)
	heavy, heavyShip := coLocatedWarehouse(t, "heavy-4B", "X1-KA42-E42", t0.Add(time.Hour), 225, allGoods, nil)
	reader := readerFor(lightShip, heavyShip)

	group := []*storage.StorageOperation{light, heavy}
	if got := TotalFreeSpace(reader, group); got != 305 {
		t.Fatalf("aggregate free space must sum both warehouses (80+225=305), got %d", got)
	}
}

// TestTotalFreeSpace_ZombieContributesZero confirms a stale sp-3lj5 zombie row (its
// storage ship unregistered, so the reader returns no ships for it) adds 0 — the sum
// is the live hull's capacity, never inflated by the dead operation.
func TestTotalFreeSpace_ZombieContributesZero(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	zombie, _ := coLocatedWarehouse(t, "warehouse-TORWIND-12-bad719ff", "X1-TORWIND-12", t0, 80, allGoods, nil)
	live, liveShip := coLocatedWarehouse(t, "warehouse-TORWIND-12-3477282e", "X1-TORWIND-12", t0.Add(2*time.Hour), 80, allGoods, nil)
	reader := readerFor(liveShip) // zombie ship deliberately NOT registered

	group := []*storage.StorageOperation{zombie, live}
	if got := TotalFreeSpace(reader, group); got != 80 {
		t.Fatalf("a zombie op with no registered ship must contribute 0 (want 80), got %d", got)
	}
}

// TestTotalCargoAvailable_SumsAcrossGroup pins the fill-target netting: on-hand stock
// of a good is the sum across every co-located warehouse, so a sibling's stock is
// never invisible to the units-short math.
func TestTotalCargoAvailable_SumsAcrossGroup(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	a, aShip := coLocatedWarehouse(t, "wh-a", "X1-KA42-E42", t0, 500, allGoods, map[string]int{"ELECTRONICS": 120})
	b, bShip := coLocatedWarehouse(t, "wh-b", "X1-KA42-E42", t0.Add(time.Hour), 500, allGoods, map[string]int{"ELECTRONICS": 80})
	reader := readerFor(aShip, bShip)

	group := []*storage.StorageOperation{a, b}
	if got := TotalCargoAvailable(reader, group, "ELECTRONICS"); got != 200 {
		t.Fatalf("aggregate on-hand must sum both warehouses (120+80=200), got %d", got)
	}
}

// TestSelectDepositWarehouse_PicksNewestWithSpace: with both members holding space,
// the deposit lands on the newest (co-located hulls share a waypoint, so "nearest" is
// degenerate and newest — the zombie-avoidance order — decides).
func TestSelectDepositWarehouse_PicksNewestWithSpace(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	older, olderShip := coLocatedWarehouse(t, "wh-older", "X1-KA42-E42", t0, 100, allGoods, nil)
	newer, newerShip := coLocatedWarehouse(t, "wh-newer", "X1-KA42-E42", t0.Add(time.Hour), 100, allGoods, nil)
	reader := readerFor(olderShip, newerShip)

	got := SelectDepositWarehouse(reader, []*storage.StorageOperation{older, newer}, "ELECTRONICS")
	if got == nil || got.ID() != "wh-newer" {
		t.Fatalf("want newest member wh-newer, got %v", got)
	}
}

// TestSelectDepositWarehouse_SwitchesWhenNewestFull: the newest member is full (0
// free), so the deposit spills to the older member with space — the switch that makes
// capacity horizontal.
func TestSelectDepositWarehouse_SwitchesWhenNewestFull(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	older, olderShip := coLocatedWarehouse(t, "wh-older", "X1-KA42-E42", t0, 100, allGoods, nil)
	// newest is full: capacity 100, stocked 100 -> 0 free.
	newer, newerShip := coLocatedWarehouse(t, "wh-newer", "X1-KA42-E42", t0.Add(time.Hour), 100, allGoods, map[string]int{"IRON": 100})
	reader := readerFor(olderShip, newerShip)

	got := SelectDepositWarehouse(reader, []*storage.StorageOperation{older, newer}, "ELECTRONICS")
	if got == nil || got.ID() != "wh-older" {
		t.Fatalf("newest is full — deposit must switch to wh-older, got %v", got)
	}
}

// TestSelectDepositWarehouse_NilWhenAllFull: "full" is true ONLY when every member is
// saturated — the sole condition a caller may report the warehouse full.
func TestSelectDepositWarehouse_NilWhenAllFull(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	a, aShip := coLocatedWarehouse(t, "wh-a", "X1-KA42-E42", t0, 100, allGoods, map[string]int{"IRON": 100})
	b, bShip := coLocatedWarehouse(t, "wh-b", "X1-KA42-E42", t0.Add(time.Hour), 100, allGoods, map[string]int{"IRON": 100})
	reader := readerFor(aShip, bShip)

	if got := SelectDepositWarehouse(reader, []*storage.StorageOperation{a, b}, "ELECTRONICS"); got != nil {
		t.Fatalf("all members full — must return nil (warehouse full), got %v", got)
	}
}

// TestSelectDepositWarehouse_SkipsUnsupportedMember: a member that does not buffer the
// good is never chosen even with free space; the deposit lands on the one that does.
func TestSelectDepositWarehouse_SkipsUnsupportedMember(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	// newest supports only FOOD; older supports ELECTRONICS.
	newer, newerShip := coLocatedWarehouse(t, "wh-food", "X1-KA42-E42", t0.Add(time.Hour), 100, []string{"FOOD"}, nil)
	older, olderShip := coLocatedWarehouse(t, "wh-elec", "X1-KA42-E42", t0, 100, []string{"ELECTRONICS"}, nil)
	reader := readerFor(newerShip, olderShip)

	got := SelectDepositWarehouse(reader, []*storage.StorageOperation{newer, older}, "ELECTRONICS")
	if got == nil || got.ID() != "wh-elec" {
		t.Fatalf("must skip the member that does not support ELECTRONICS, got %v", got)
	}
}

// TestRunningWarehousesAtWaypoint_FiltersWaypointAndType keeps only warehouse ops at
// the target waypoint (a co-located group), excluding other waypoints and non-warehouse
// storage ops.
func TestRunningWarehousesAtWaypoint_FiltersWaypointAndType(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	here1, _ := coLocatedWarehouse(t, "here-1", "X1-KA42-E42", t0, 100, allGoods, nil)
	here2, _ := coLocatedWarehouse(t, "here-2", "X1-KA42-E42", t0, 100, allGoods, nil)
	elsewhere, _ := coLocatedWarehouse(t, "elsewhere", "X1-KA42-Z9", t0, 100, allGoods, nil)
	gas, err := storage.NewStorageOperation("gas-here", 1, "X1-KA42-E42", storage.OperationTypeGasSiphon,
		[]string{"EXT"}, []string{"STORE"}, allGoods, &shared.MockClock{CurrentTime: t0})
	if err != nil {
		t.Fatalf("gas op: %v", err)
	}
	if err := gas.Start(); err != nil {
		t.Fatalf("gas start: %v", err)
	}

	group := RunningWarehousesAtWaypoint([]*storage.StorageOperation{here1, elsewhere, gas, here2}, "X1-KA42-E42")
	if len(group) != 2 {
		t.Fatalf("want the 2 co-located warehouse ops (excluding other waypoint + gas), got %d: %+v", len(group), group)
	}
}

// TestBuildDepositCandidates_AggregatesCoLocatedCapacityAndStock is the Lane C
// multi-warehouse pin: two warehouses at one waypoint, one already holding stock. The
// candidate's remaining demand must net the SUM of both hulls' stock, and the ceiling
// (here non-binding) must let the full aggregate through — proving the second hull's
// capacity is priced, not orphaned.
func TestBuildDepositCandidates_AggregatesCoLocatedCapacityAndStock(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	// wh-a holds 120 ELECTRONICS, wh-b holds 80: aggregate on-hand 200, demand 404 ->
	// remaining 204. Both have ample free space (500 each -> 800 aggregate).
	a, aShip := coLocatedWarehouse(t, "wh-a", "X1-KA42-E42", t0, 500, allGoods, map[string]int{"ELECTRONICS": 120})
	b, bShip := coLocatedWarehouse(t, "wh-b", "X1-KA42-E42", t0.Add(time.Hour), 500, allGoods, map[string]int{"ELECTRONICS": 80})
	reader := readerFor(aShip, bShip)
	miner := &fakeDepositMiner{rows: []persistence.DemandCandidate{demandRow("ELECTRONICS", 404, 3000, 744)}}
	finder := &fakeWarehouseFinder{ops: []*storage.StorageOperation{a, b}}
	cfg := DepositCandidateConfig{Enabled: true, TopN: 5, MinSavingsPerUnit: 1}

	out := BuildDepositCandidates(context.Background(), miner, finder, reader,
		[]string{"X1-KA42"}, 1, 1_000_000_000, true, cfg)
	if len(out) != 1 {
		t.Fatalf("expected 1 candidate, got %d: %+v", len(out), out)
	}
	if out[0].UnitsWanted != 204 {
		t.Fatalf("remaining demand must net the SUMMED stock of both hulls (404-200=204), got %d", out[0].UnitsWanted)
	}
	// The candidate is anchored at the shared co-located waypoint.
	if out[0].StorageWaypoint != "X1-KA42-E42" {
		t.Fatalf("candidate must be anchored at the co-located waypoint, got %q", out[0].StorageWaypoint)
	}
}

// TestBuildDepositCandidates_AggregateFreeSpaceCapsUnits proves the deposit units are
// capped by the AGGREGATE free space across the group, not one hull's: two hulls with
// 60 free each yield a 120 cap (a single-hull view would cap at 60).
func TestBuildDepositCandidates_AggregateFreeSpaceCapsUnits(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	// Each hull: capacity 60, empty -> 60 free; aggregate 120. Demand 404, ceiling huge.
	a, aShip := coLocatedWarehouse(t, "wh-a", "X1-KA42-E42", t0, 60, allGoods, nil)
	b, bShip := coLocatedWarehouse(t, "wh-b", "X1-KA42-E42", t0.Add(time.Hour), 60, allGoods, nil)
	reader := readerFor(aShip, bShip)
	miner := &fakeDepositMiner{rows: []persistence.DemandCandidate{demandRow("ELECTRONICS", 404, 3000, 744)}}
	finder := &fakeWarehouseFinder{ops: []*storage.StorageOperation{a, b}}
	cfg := DepositCandidateConfig{Enabled: true, TopN: 5, MinSavingsPerUnit: 1}

	out := BuildDepositCandidates(context.Background(), miner, finder, reader,
		[]string{"X1-KA42"}, 1, 1_000_000_000, true, cfg)
	if len(out) != 1 || out[0].UnitsWanted != 120 {
		t.Fatalf("units must be capped by AGGREGATE free space (60+60=120), got %+v", out)
	}
}
