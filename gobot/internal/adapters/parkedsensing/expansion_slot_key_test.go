package parkedsensing_test

// sp-dpfp8 — THE SEED-STAGING FREEZE, proved against a store that ENFORCES the key.
//
// WHY THIS FILE EXISTS SEPARATELY FROM THE ENGINE'S OWN TESTS. The expansion
// engine is tested against an in-memory fake ledger, and a fake decides for itself
// what "one placement per waypoint" means. The claim being made here — that a yard
// can carry a MARKET placement and a SPARE placement AT THE SAME TIME — is a claim
// about the TABLE. Asserted against a map, it proves only that the map was written
// to allow it. So this file wires the REAL stack:
//
//	AdvanceExpansion -> LedgerPort -> SensingLedgerRepository -> sqlite
//
// The sqlite schema is built by GORM AutoMigrate straight from the primaryKey tags
// on SensingSlotModel, so the composite key is a real constraint enforced by the
// database: a second row of the same kind at a waypoint cannot exist, and an
// upsert whose conflict target does not match a real unique index fails outright.
// TestSlotKey_TheStoreActuallyEnforcesTheKey below pins that the fixture has teeth,
// so the tests after it cannot pass by permissiveness.
//
// THE LIVE SHAPE THIS REPRODUCES. Expansion sat at two charting seeds and could
// not order a third, ever. The fleet's only probe-selling yards, X1-KP23-A2 and
// X1-KP23-C38, each held a parked MARKET placement; stagingYardFor would only pick
// a yard carrying no placement, so every tick found no free yard and wrote no SPARE
// want. sensing_slots held zero (SPARE, WANTED) rows for the whole life of the
// fleet, and with no spare there was no hull to send.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	adapterSensing "github.com/andrescamacho/spacetraders-go/internal/adapters/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	appSensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
)

// --- the ports the engine needs that are NOT the ledger -----------------------
//
// Deliberately inert. Everything these return is fixed, because the only moving
// part under test is what the engine writes to the real table.

type keyTestGates struct{ adjacency map[string][]string }

func (g keyTestGates) Neighbours(_ context.Context, system string) ([]string, error) {
	return g.adjacency[system], nil
}

// Mapped reports every system as already mapped — neutral for the gate-priority tier.
func (g keyTestGates) Mapped(_ context.Context, _ string) (bool, error) { return true, nil }

type keyTestYards struct{ bySystem map[string][]string }

func (y keyTestYards) ListProbeYards(_ context.Context, system string) ([]string, error) {
	return y.bySystem[system], nil
}

// PassableGraph mirrors this fake's own adjacency as a single snapshot. Mapped enumerates exactly
// the systems this fake HOLDS: a bulk snapshot cannot express the per-system Mapped's "true for
// anything you ask", and enumerating what is actually stored is the truthful reading — a system
// with no entry here has not been read, which is what the reachability filter treats as unknown
// rather than as a dead end.
func (g keyTestGates) PassableGraph(_ context.Context) (appSensing.GateGraph, error) {
	graph := appSensing.GateGraph{Passable: map[string][]string{}, Mapped: map[string]bool{}}
	for system, neighbours := range g.adjacency {
		graph.Mapped[system] = true
		graph.Passable[system] = append([]string(nil), neighbours...)
	}
	return graph, nil
}

// keyTestScreen leaves every system PENDING: a verdict is not what these tests
// are about, and PENDING is what keeps X1-B a charting target.
func keyTestScreen(_ context.Context, _ string) (appSensing.ScreenResult, error) {
	return appSensing.ScreenResult{Verdict: appSensing.VerdictPending}, nil
}

type keyTestShips struct{}

func (keyTestShips) DockedProbeAt(_ context.Context, _ int, _ string) (string, bool, error) {
	return "", false, nil
}

// Nobody stands anywhere and nothing is lendable: this fixture is about the
// placement KEY, so the borrow reads answer the neutral "no".
func (keyTestShips) DockedBuyerAt(_ context.Context, _ int, _ string) (string, bool, error) {
	return "", false, nil
}

func (keyTestShips) LendableHulls(_ context.Context, _ int, _ int) ([]appSensing.LendableHull, error) {
	return nil, nil
}

func (keyTestShips) ShipAt(_ context.Context, _ int, _ string) (appSensing.ShipPos, error) {
	return appSensing.ShipPos{}, nil
}

type keyTestMarkets struct{}

func (keyTestMarkets) GoodsAt(_ context.Context, _ int, _ string) ([]string, bool, error) {
	return nil, false, nil
}

func (keyTestMarkets) DepthRowsAt(_ context.Context, _ int, _ string) ([]scouting.MarketDepthRow, error) {
	return nil, nil
}

type keyTestUncharted struct{}

func (keyTestUncharted) UnchartedWaypoints(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

type keyTestSeedCommander struct{}

func (keyTestSeedCommander) JumpTo(_ context.Context, _ int, _, _, _ string) error  { return nil }
func (keyTestSeedCommander) NavigateTo(_ context.Context, _ int, _, _ string) error { return nil }
func (keyTestSeedCommander) Dock(_ context.Context, _ int, _ string) error          { return nil }
func (keyTestSeedCommander) Chart(_ context.Context, _ int, _ string) error         { return nil }
func (keyTestSeedCommander) ReadMarketAt(_ context.Context, _ int, _ string) error  { return nil }
func (keyTestSeedCommander) SyncWaypoints(_ context.Context, _ int, _ string) error { return nil }
func (keyTestSeedCommander) RefreshWaypoint(_ context.Context, _ int, _, _ string) (bool, error) {
	return false, nil
}

// realLedgerPorts assembles the engine over the REAL repository.
func realLedgerPorts(db *gorm.DB, gates map[string][]string, yards map[string][]string) appSensing.ExpandPorts {
	return appSensing.ExpandPorts{
		Gates:       keyTestGates{adjacency: gates},
		Ledger:      adapterSensing.NewLedgerPort(persistence.NewSensingLedgerRepository(db)),
		Screen:      keyTestScreen,
		SeedShip:    keyTestSeedCommander{},
		Ships:       keyTestShips{},
		MarketGoods: keyTestMarkets{},
		Yards:       keyTestYards{bySystem: yards},
		Uncharted:   keyTestUncharted{},
	}
}

func strPtr(s string) *string { return &s }

// slotRows reads the table directly — not through the repository — so an assertion
// about how many rows exist cannot be filtered by the code under test.
func slotRows(t *testing.T, db *gorm.DB, waypoint string) []persistence.SensingSlotModel {
	t.Helper()
	var rows []persistence.SensingSlotModel
	require.NoError(t, db.Where("player_id = ? AND waypoint_symbol = ?", testPlayerID, waypoint).
		Order("slot_kind").Find(&rows).Error)
	return rows
}

// THE FIXTURE HAS TEETH. Everything below rests on this store genuinely enforcing
// (player, waypoint, slot_kind), so that is established first and directly.
//
// Two halves, and BOTH matter. A store that allowed unlimited rows per waypoint
// would let a broken kind-blind delete look correct; a store still keyed on the
// waypoint alone would make the co-location tests silently unreachable.
func TestSlotKey_TheStoreActuallyEnforcesTheKey(t *testing.T) {
	db := newShipPortsDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	market := persistence.SensingSlotModel{
		PlayerID: testPlayerID, WaypointSymbol: "X1-A-YARD", SystemSymbol: "X1-A",
		SlotKind: appSensing.SlotKindMarket, State: appSensing.SlotStateParked,
		AssignedShip: strPtr("PROBE-1"), WhitelistGoods: "[]",
	}
	require.NoError(t, repo.UpsertSpareSlot(ctx, market))

	// DIFFERENT KIND, same waypoint: a second row.
	spare := persistence.SensingSlotModel{
		PlayerID: testPlayerID, WaypointSymbol: "X1-A-YARD", SystemSymbol: "X1-A",
		SlotKind: appSensing.SlotKindSpare, State: appSensing.SlotStateWanted,
		WhitelistGoods: "[]",
	}
	require.NoError(t, repo.UpsertSlotMetadata(ctx, spare))
	require.Len(t, slotRows(t, db, "X1-A-YARD"), 2,
		"a waypoint must be able to carry one MARKET row and one SPARE row at once — that is the whole point of the wider key")

	// SAME KIND, same waypoint: still ONE row. The key is a constraint, not a
	// suggestion, so this is an upsert rather than a third row.
	spare.State = appSensing.SlotStateQueued
	require.NoError(t, repo.UpsertSlotMetadata(ctx, spare))
	rows := slotRows(t, db, "X1-A-YARD")
	require.Len(t, rows, 2, "a second SPARE at the same waypoint must CONFLICT, never accumulate")

	require.Equal(t, appSensing.SlotKindMarket, rows[0].SlotKind)
	require.Equal(t, "PROBE-1", *rows[0].AssignedShip,
		"the market row's hull must survive a spare write at the same waypoint")
}

// THE HEADLINE, end to end against the real table.
//
// One system we hold (X1-A) borders an unscreened one (X1-B) that needs charting.
// The single probe-selling yard in X1-A is ALREADY a parked market placement —
// the live fleet's exact shape. The engine must stage a SPARE want there anyway.
//
// Before sp-dpfp8 this produced no write at all: stagingYardFor found the yard
// occupied, requestSeeds got yard=="" and skipped, and the fleet stayed frozen at
// the seeds it already had.
func TestExpansion_StagesASeedAtAYardThatIsAlreadyAParkedMarket(t *testing.T) {
	db := newShipPortsDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.UpsertSystem(ctx, persistence.SensingSystemModel{
		PlayerID: testPlayerID, SystemSymbol: "X1-A", Verdict: appSensing.VerdictInScope,
	}))
	require.NoError(t, repo.UpsertSystem(ctx, persistence.SensingSystemModel{
		PlayerID: testPlayerID, SystemSymbol: "X1-B", Verdict: appSensing.VerdictPending,
		UnchartedCount: 3,
	}))
	// The yard we would buy at is occupied by a probe parked there scanning.
	require.NoError(t, repo.UpsertSpareSlot(ctx, persistence.SensingSlotModel{
		PlayerID: testPlayerID, WaypointSymbol: "X1-A-YARD", SystemSymbol: "X1-A",
		SlotKind: appSensing.SlotKindMarket, State: appSensing.SlotStateParked,
		AssignedShip: strPtr("PROBE-1"), WhitelistGoods: `["FUEL"]`,
	}))

	ports := realLedgerPorts(db,
		map[string][]string{"X1-A": {"X1-B"}},
		map[string][]string{"X1-A": {"X1-A-YARD"}})

	rep, err := appSensing.AdvanceExpansion(ctx, ports, testPlayerID, appSensing.ExpandKnobs{
		SpendEnabled: true, MinBudgetRate: 0.05, Whitelist: map[string]bool{"FUEL": true},
	}, 1.0)
	require.NoError(t, err)

	require.Equal(t, 1, rep.SeedsRequested,
		"the only bordering yard being a parked market is the live fleet's shape, and it froze expansion outright")

	rows := slotRows(t, db, "X1-A-YARD")
	require.Len(t, rows, 2, "the yard now carries BOTH its market placement and the staged seed")

	require.Equal(t, appSensing.SlotKindMarket, rows[0].SlotKind)
	require.Equal(t, appSensing.SlotStateParked, rows[0].State,
		"the scanning probe's placement must be untouched — evicting it would drop a hull we paid for out of the probe cap")
	require.Equal(t, "PROBE-1", *rows[0].AssignedShip)
	require.Equal(t, `["FUEL"]`, rows[0].WhitelistGoods,
		"the seed request measures nothing and must not erase the market's goods list")

	require.Equal(t, appSensing.SlotKindSpare, rows[1].SlotKind)
	require.Equal(t, appSensing.SlotStateWanted, rows[1].State,
		"a SPARE/WANTED row is what the buy queue drains to actually purchase the seed")
	require.Nil(t, rows[1].AssignedShip, "a want names no hull until one is bought")

	// The probe cap must still see exactly the one hull we own. A staged want is
	// an intent, not a purchase.
	owned, err := repo.CountOwnedProbes(ctx, testPlayerID)
	require.NoError(t, err)
	require.Equal(t, int64(1), owned)
}

// THE RELEASE, which is where a kind-blind write would have been catastrophic.
//
// A parked SPARE at a yard is claimed as a charting seed, and claimSpares deletes
// its placement row. That yard is ALSO a parked market. A delete keyed on the
// waypoint alone takes both rows, so the probe scanning there stops being counted
// by CountOwnedProbes while it is still on station — and the cap then authorises
// buying a replacement for a hull we already own (RULINGS #4).
//
// This is the failure the narrow key used to make impossible for free.
func TestExpansion_ClaimingASpareLeavesTheMarketPlacementSharingItsWaypoint(t *testing.T) {
	db := newShipPortsDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.UpsertSystem(ctx, persistence.SensingSystemModel{
		PlayerID: testPlayerID, SystemSymbol: "X1-A", Verdict: appSensing.VerdictInScope,
	}))
	require.NoError(t, repo.UpsertSystem(ctx, persistence.SensingSystemModel{
		PlayerID: testPlayerID, SystemSymbol: "X1-B", Verdict: appSensing.VerdictPending,
		UnchartedCount: 3,
	}))
	// One waypoint, two placements, two DIFFERENT hulls.
	require.NoError(t, repo.UpsertSpareSlot(ctx, persistence.SensingSlotModel{
		PlayerID: testPlayerID, WaypointSymbol: "X1-A-YARD", SystemSymbol: "X1-A",
		SlotKind: appSensing.SlotKindMarket, State: appSensing.SlotStateParked,
		AssignedShip: strPtr("PROBE-SCANNING"), WhitelistGoods: `["FUEL"]`,
	}))
	require.NoError(t, repo.UpsertSpareSlot(ctx, persistence.SensingSlotModel{
		PlayerID: testPlayerID, WaypointSymbol: "X1-A-YARD", SystemSymbol: "X1-A",
		SlotKind: appSensing.SlotKindSpare, State: appSensing.SlotStateParked,
		AssignedShip: strPtr("PROBE-SPARE"), WhitelistGoods: "[]",
	}))

	owned, err := repo.CountOwnedProbes(ctx, testPlayerID)
	require.NoError(t, err)
	require.Equal(t, int64(2), owned, "both hulls are ours before the claim")

	ports := realLedgerPorts(db,
		map[string][]string{"X1-A": {"X1-B"}},
		map[string][]string{"X1-A": {"X1-A-YARD"}})

	rep, err := appSensing.AdvanceExpansion(ctx, ports, testPlayerID, appSensing.ExpandKnobs{
		SpendEnabled: true, MinBudgetRate: 0.05, Whitelist: map[string]bool{"FUEL": true},
	}, 1.0)
	require.NoError(t, err)
	require.Equal(t, 1, rep.SeedsClaimed, "the parked spare borders X1-B and is claimed for it")

	rows := slotRows(t, db, "X1-A-YARD")
	require.Len(t, rows, 1, "the SPARE row is released to the mission; the MARKET row is not the mission's to take")
	require.Equal(t, appSensing.SlotKindMarket, rows[0].SlotKind)
	require.Equal(t, "PROBE-SCANNING", *rows[0].AssignedShip,
		"the scanning probe must still be named by a row — a waypoint-wide delete drops it out of the probe cap and authorises re-buying a hull we own")

	// Neither hull leaves the count: the scanner by its MARKET row, the claimed
	// spare by the errand the count now unions in.
	owned, err = repo.CountOwnedProbes(ctx, testPlayerID)
	require.NoError(t, err)
	require.Equal(t, int64(2), owned,
		"losing either would be the money-unsafe direction — an under-count authorises re-buying a hull we own")
}

// THE GHOST ROW'S RELEASE, against the real table and the real probe cap.
//
// A SPARE row naming a hull a system row already has out charting is the invariant
// in states.go broken. Expansion must delete it — nothing else can, and while it
// stands the yard is unstageable — and the fleet size must not move when it does:
// the hull is accounted for by its errand either side of the write.
func TestExpansion_ReleasingAGhostSpareLeavesTheProbeCapWhereItWas(t *testing.T) {
	db := newShipPortsDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.UpsertSystem(ctx, persistence.SensingSystemModel{
		PlayerID: testPlayerID, SystemSymbol: "X1-A", Verdict: appSensing.VerdictInScope,
	}))
	require.NoError(t, repo.UpsertSystem(ctx, persistence.SensingSystemModel{
		PlayerID: testPlayerID, SystemSymbol: "X1-B", Verdict: appSensing.VerdictPending,
		UnchartedCount: 3,
	}))
	require.NoError(t, repo.UpsertSystem(ctx, persistence.SensingSystemModel{
		PlayerID: testPlayerID, SystemSymbol: "X1-C", Verdict: appSensing.VerdictPending,
		UnchartedCount: 5,
	}))
	require.NoError(t, repo.SetSeed(ctx, testPlayerID, "X1-C", "PROBE-SPARE", appSensing.SeedStateCharting))

	// The yard: a probe scanning it, and the ghost row for a hull that is not there.
	require.NoError(t, repo.UpsertSpareSlot(ctx, persistence.SensingSlotModel{
		PlayerID: testPlayerID, WaypointSymbol: "X1-A-YARD", SystemSymbol: "X1-A",
		SlotKind: appSensing.SlotKindMarket, State: appSensing.SlotStateParked,
		AssignedShip: strPtr("PROBE-SCANNING"), WhitelistGoods: `["FUEL"]`,
	}))
	require.NoError(t, repo.UpsertSpareSlot(ctx, persistence.SensingSlotModel{
		PlayerID: testPlayerID, WaypointSymbol: "X1-A-YARD", SystemSymbol: "X1-A",
		SlotKind: appSensing.SlotKindSpare, State: appSensing.SlotStateParked,
		AssignedShip: strPtr("PROBE-SPARE"), WhitelistGoods: "[]",
	}))

	before, err := repo.CountOwnedProbes(ctx, testPlayerID)
	require.NoError(t, err)
	require.Equal(t, int64(2), before, "two hulls: one scanning, one charting X1-C")

	ports := realLedgerPorts(db,
		map[string][]string{"X1-A": {"X1-B"}},
		map[string][]string{"X1-A": {"X1-A-YARD"}})

	rep, err := appSensing.AdvanceExpansion(ctx, ports, testPlayerID, appSensing.ExpandKnobs{
		SpendEnabled: true, MinBudgetRate: 0.05, Whitelist: map[string]bool{"FUEL": true},
	}, 1.0)
	require.NoError(t, err)
	require.Equal(t, 1, rep.SpareGhostsReleased)
	require.Equal(t, 0, rep.SeedsClaimed, "a hull already charting X1-C must not be stamped onto X1-B as well")

	rows := slotRows(t, db, "X1-A-YARD")
	require.Len(t, rows, 2, "the ghost is gone and the freed yard now carries the SPARE want it could not stage before")
	require.Equal(t, appSensing.SlotKindMarket, rows[0].SlotKind)
	require.Equal(t, "PROBE-SCANNING", *rows[0].AssignedShip, "the scanning probe's placement is not the release's to take")
	require.Equal(t, appSensing.SlotKindSpare, rows[1].SlotKind)
	require.Equal(t, appSensing.SlotStateWanted, rows[1].State)
	require.Nil(t, rows[1].AssignedShip, "the ghost's hull is not carried into the want")

	after, err := repo.CountOwnedProbes(ctx, testPlayerID)
	require.NoError(t, err)
	require.Equal(t, before, after,
		"the fleet did not shrink: the errand accounts for the released hull, and a shrinking cap is what buys a probe we own")
}
