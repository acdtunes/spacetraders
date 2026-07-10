package services

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// --- fakes ------------------------------------------------------------------

type fakeDepositMiner struct {
	rows      []persistence.DemandCandidate
	err       error
	gotHome   string
	gotMinRec int
}

func (f *fakeDepositMiner) Mine(_ context.Context, home string, _ int, _ *int, opts persistence.DemandMinerOptions) ([]persistence.DemandCandidate, error) {
	f.gotHome = home
	f.gotMinRec = opts.MinRecurrence
	return f.rows, f.err
}

type fakeWarehouseFinder struct {
	ops []*storage.StorageOperation
	err error
}

func (f *fakeWarehouseFinder) FindRunning(_ context.Context, _ int) ([]*storage.StorageOperation, error) {
	return f.ops, f.err
}

type fakeSpaceReader struct {
	ships map[string][]*storage.StorageShip
}

func (f *fakeSpaceReader) GetStorageShipsForOperation(opID string) []*storage.StorageShip {
	return f.ships[opID]
}

func (f *fakeSpaceReader) GetTotalCargoAvailable(opID, good string) int {
	total := 0
	for _, s := range f.ships[opID] {
		total += s.GetAvailableCargo(good)
	}
	return total
}

// --- helpers ----------------------------------------------------------------

func demandRow(good string, units, homeAsk, foreignAsk int) persistence.DemandCandidate {
	sav := homeAsk - foreignAsk
	return persistence.DemandCandidate{
		Good: good, DemandUnits: units, ForeignAsk: foreignAsk,
		HomeAsk: homeAsk, HomeAskKnown: true,
		ProjectedSavingsPerUnit: sav, StockEligible: sav > 0,
	}
}

// runningWarehouse builds a RUNNING warehouse op at waypoint with the given
// supported goods and a single storage hull of the given capacity, pre-seeded
// with `stocked`. Returns the op and a space reader backed by the real hull.
func runningWarehouse(t *testing.T, id, waypoint string, capacity int, goods []string, stocked map[string]int) (*storage.StorageOperation, *fakeSpaceReader) {
	t.Helper()
	op, err := storage.NewWarehouseOperation(id, 1, waypoint, []string{id + "-WH"}, goods, shared.NewRealClock())
	if err != nil {
		t.Fatalf("warehouse op: %v", err)
	}
	if err := op.Start(); err != nil {
		t.Fatalf("warehouse start: %v", err)
	}
	ship, err := storage.NewStorageShip(id+"-WH", waypoint, id, capacity, stocked)
	if err != nil {
		t.Fatalf("storage ship: %v", err)
	}
	return op, &fakeSpaceReader{ships: map[string][]*storage.StorageShip{id: {ship}}}
}

func candByGood(cands []routing.TourDepositCandidate) map[string]routing.TourDepositCandidate {
	m := map[string]routing.TourDepositCandidate{}
	for _, c := range cands {
		m[c.Good] = c
	}
	return m
}

var allGoods = []string{"ELECTRONICS", "EQUIPMENT", "FOOD", "IRON"}

// --- tests ------------------------------------------------------------------

func TestBuildDepositCandidates_EligibleRowsBecomeCandidates(t *testing.T) {
	op, reader := runningWarehouse(t, "wh1", "X1-KA42-H1", 2000, allGoods, nil)
	miner := &fakeDepositMiner{rows: []persistence.DemandCandidate{
		demandRow("ELECTRONICS", 404, 3000, 744), // savings 2256
		demandRow("EQUIPMENT", 592, 1500, 422),   // savings 1078
	}}
	finder := &fakeWarehouseFinder{ops: []*storage.StorageOperation{op}}
	cfg := DepositCandidateConfig{Enabled: true, TopN: 5, MinRecurrence: 2, MinSavingsPerUnit: 1}

	out := BuildDepositCandidates(context.Background(), miner, finder, reader,
		[]string{"X1-KA42"}, 1, 1_000_000_000, true, cfg)

	if len(out) != 2 {
		t.Fatalf("expected 2 candidates, got %d: %+v", len(out), out)
	}
	// The miner was scoped to the WAREHOUSE's system and the config's minRecurrence.
	if miner.gotHome != "X1-KA42" || miner.gotMinRec != 2 {
		t.Fatalf("miner scope wrong: home=%q minRec=%d", miner.gotHome, miner.gotMinRec)
	}
	by := candByGood(out)
	e := by["ELECTRONICS"]
	if e.UnitsWanted != 404 || e.SyntheticBid != 3000 || e.StorageWaypoint != "X1-KA42-H1" || e.StorageSystem != "X1-KA42" {
		t.Fatalf("ELECTRONICS candidate wrong: %+v", e)
	}
	if by["EQUIPMENT"].UnitsWanted != 592 || by["EQUIPMENT"].SyntheticBid != 1500 {
		t.Fatalf("EQUIPMENT candidate wrong: %+v", by["EQUIPMENT"])
	}
}

func TestBuildDepositCandidates_FailsClosedOnUnreadableCeiling(t *testing.T) {
	op, reader := runningWarehouse(t, "wh1", "X1-KA42-H1", 2000, allGoods, nil)
	miner := &fakeDepositMiner{rows: []persistence.DemandCandidate{demandRow("ELECTRONICS", 404, 3000, 744)}}
	finder := &fakeWarehouseFinder{ops: []*storage.StorageOperation{op}}
	cfg := DepositCandidateConfig{Enabled: true, TopN: 5}

	// ceilingKnown=false => the live balance was unreadable => NO candidates (RULINGS #4).
	out := BuildDepositCandidates(context.Background(), miner, finder, reader,
		[]string{"X1-KA42"}, 1, 0, false, cfg)
	if len(out) != 0 {
		t.Fatalf("unreadable ceiling must yield zero candidates (fail closed), got %+v", out)
	}
}

func TestBuildDepositCandidates_DisabledYieldsNothing(t *testing.T) {
	op, reader := runningWarehouse(t, "wh1", "X1-KA42-H1", 2000, allGoods, nil)
	miner := &fakeDepositMiner{rows: []persistence.DemandCandidate{demandRow("ELECTRONICS", 404, 3000, 744)}}
	finder := &fakeWarehouseFinder{ops: []*storage.StorageOperation{op}}

	out := BuildDepositCandidates(context.Background(), miner, finder, reader,
		[]string{"X1-KA42"}, 1, 1_000_000, true, DepositCandidateConfig{Enabled: false})
	if len(out) != 0 {
		t.Fatalf("disabled pre-positioning must yield nothing, got %+v", out)
	}
}

func TestBuildDepositCandidates_NoWarehouseInGraph(t *testing.T) {
	op, reader := runningWarehouse(t, "wh1", "X1-OTHER-H1", 2000, allGoods, nil) // warehouse in X1-OTHER
	miner := &fakeDepositMiner{rows: []persistence.DemandCandidate{demandRow("ELECTRONICS", 404, 3000, 744)}}
	finder := &fakeWarehouseFinder{ops: []*storage.StorageOperation{op}}
	cfg := DepositCandidateConfig{Enabled: true, TopN: 5}

	// The tour graph is X1-KA42 only; the warehouse's system (X1-OTHER) is outside it.
	out := BuildDepositCandidates(context.Background(), miner, finder, reader,
		[]string{"X1-KA42"}, 1, 1_000_000, true, cfg)
	if len(out) != 0 {
		t.Fatalf("warehouse outside the tour graph must yield no candidates, got %+v", out)
	}
}

func TestBuildDepositCandidates_WarehouseFullYieldsNothing(t *testing.T) {
	// capacity == stocked => zero free space.
	op, reader := runningWarehouse(t, "wh1", "X1-KA42-H1", 100, allGoods, map[string]int{"IRON": 100})
	miner := &fakeDepositMiner{rows: []persistence.DemandCandidate{demandRow("ELECTRONICS", 404, 3000, 744)}}
	finder := &fakeWarehouseFinder{ops: []*storage.StorageOperation{op}}
	cfg := DepositCandidateConfig{Enabled: true, TopN: 5}

	out := BuildDepositCandidates(context.Background(), miner, finder, reader,
		[]string{"X1-KA42"}, 1, 1_000_000, true, cfg)
	if len(out) != 0 {
		t.Fatalf("full warehouse must yield no candidates, got %+v", out)
	}
}

func TestBuildDepositCandidates_DropsIneligibleAndUnsupportedAndBlocked(t *testing.T) {
	// Warehouse supports only ELECTRONICS + EQUIPMENT.
	op, reader := runningWarehouse(t, "wh1", "X1-KA42-H1", 5000, []string{"ELECTRONICS", "EQUIPMENT"}, nil)
	miner := &fakeDepositMiner{rows: []persistence.DemandCandidate{
		demandRow("ELECTRONICS", 404, 3000, 744), // eligible + supported + not blocked -> KEEP
		demandRow("EQUIPMENT", 592, 1500, 422),   // eligible + supported but BLOCKED -> drop
		demandRow("FOOD", 1089, 1000, 112),       // eligible but UNSUPPORTED by warehouse -> drop
		{Good: "IRON", DemandUnits: 50, ForeignAsk: 900, HomeAsk: 800, HomeAskKnown: true, ProjectedSavingsPerUnit: -100, StockEligible: false}, // home-cheaper -> not eligible
	}}
	finder := &fakeWarehouseFinder{ops: []*storage.StorageOperation{op}}
	cfg := DepositCandidateConfig{Enabled: true, TopN: 5, MinSavingsPerUnit: 1, Blocklist: []string{"EQUIPMENT"}}

	out := BuildDepositCandidates(context.Background(), miner, finder, reader,
		[]string{"X1-KA42"}, 1, 1_000_000_000, true, cfg)
	if len(out) != 1 || out[0].Good != "ELECTRONICS" {
		t.Fatalf("only ELECTRONICS should survive (eligible+supported+unblocked), got %+v", out)
	}
}

func TestBuildDepositCandidates_AllowlistRestricts(t *testing.T) {
	op, reader := runningWarehouse(t, "wh1", "X1-KA42-H1", 5000, allGoods, nil)
	miner := &fakeDepositMiner{rows: []persistence.DemandCandidate{
		demandRow("ELECTRONICS", 404, 3000, 744),
		demandRow("EQUIPMENT", 592, 1500, 422),
	}}
	finder := &fakeWarehouseFinder{ops: []*storage.StorageOperation{op}}
	cfg := DepositCandidateConfig{Enabled: true, TopN: 5, MinSavingsPerUnit: 1, Allowlist: []string{"EQUIPMENT"}}

	out := BuildDepositCandidates(context.Background(), miner, finder, reader,
		[]string{"X1-KA42"}, 1, 1_000_000_000, true, cfg)
	if len(out) != 1 || out[0].Good != "EQUIPMENT" {
		t.Fatalf("allowlist should restrict to EQUIPMENT, got %+v", out)
	}
}

func TestBuildDepositCandidates_CeilingCapsUnits(t *testing.T) {
	op, reader := runningWarehouse(t, "wh1", "X1-KA42-H1", 100_000, allGoods, nil) // space non-binding
	miner := &fakeDepositMiner{rows: []persistence.DemandCandidate{
		demandRow("ELECTRONICS", 404, 3000, 744), // foreign 744
		demandRow("EQUIPMENT", 592, 1500, 422),   // foreign 422
	}}
	finder := &fakeWarehouseFinder{ops: []*storage.StorageOperation{op}}
	cfg := DepositCandidateConfig{Enabled: true, TopN: 5, MinSavingsPerUnit: 1}

	// Ceiling 500_000: ELECTRONICS demand 404 fits (404*744=300_576 spent); remaining
	// 199_424 credits cap EQUIPMENT at 199_424/422=472 < its 592 demand.
	out := BuildDepositCandidates(context.Background(), miner, finder, reader,
		[]string{"X1-KA42"}, 1, 500_000, true, cfg)
	by := candByGood(out)
	if by["ELECTRONICS"].UnitsWanted != 404 {
		t.Fatalf("ELECTRONICS should fit full demand, got %+v", by["ELECTRONICS"])
	}
	if by["EQUIPMENT"].UnitsWanted != 472 {
		t.Fatalf("EQUIPMENT should be ceiling-capped at 472, got %d", by["EQUIPMENT"].UnitsWanted)
	}
}

func TestBuildDepositCandidates_RemainingDemandSubtractsStocked(t *testing.T) {
	// Warehouse already holds 300 ELECTRONICS: remaining demand = 404-300 = 104.
	op, reader := runningWarehouse(t, "wh1", "X1-KA42-H1", 5000, allGoods, map[string]int{"ELECTRONICS": 300})
	miner := &fakeDepositMiner{rows: []persistence.DemandCandidate{demandRow("ELECTRONICS", 404, 3000, 744)}}
	finder := &fakeWarehouseFinder{ops: []*storage.StorageOperation{op}}
	cfg := DepositCandidateConfig{Enabled: true, TopN: 5, MinSavingsPerUnit: 1}

	out := BuildDepositCandidates(context.Background(), miner, finder, reader,
		[]string{"X1-KA42"}, 1, 1_000_000_000, true, cfg)
	if len(out) != 1 || out[0].UnitsWanted != 104 {
		t.Fatalf("remaining demand should net out stocked units (104), got %+v", out)
	}
}
