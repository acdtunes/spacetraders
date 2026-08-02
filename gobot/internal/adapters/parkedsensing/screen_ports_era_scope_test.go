package parkedsensing_test

// Integration tests (real GORM/sqlite, no mocks) for the ERA SCOPE of the three
// yard reads on WaypointCatalogPort: OutstandingYards, ListProbeYards and
// ListHeavyYards.
//
// THE BLEED THESE PIN, measured in production over ten hours, flat and not
// converging: ~290 failures/hour of
//
//	"Failed to read the shipyard catalogue at X1-AF2-A2; it stays outstanding:
//	 API error (status 404): {"error":{"message":"System X1-AF2 not found."}}"
//
// OutstandingYards built its work list from the `waypoints` table with no era
// filter. That table holds 1,772 SHIPYARD-trait rows across 862 systems, of which
// only 1,219 across 587 systems carry the OPEN era's stamp; the rest were written
// in universes that no longer exist, so the API cannot answer for them at all.
// X1-AF2 was stamped in era 2, X1-A27 in era 3, X1-A19/X1-AA14/X1-AC37 in era 4.
//
// It was not merely wasteful. The per-tick bound counts ATTEMPTS, not successes
// (deliberately — see ReadYardCatalogues, so a refusing API cannot become an
// unbounded retry storm), so every dead-era yard consumed a slot a live yard
// needed: the sweep that exists to FIND heavy shipyards spent most of its budget
// on systems that no longer exist, while API utilisation sat at 88% against an
// 85% ceiling.
//
// Two properties therefore matter more than the row filtering itself:
//
//   - the three reads must agree about which era they answer under. Era-scoping
//     one alone trades a cross-package inconsistency for a worse in-file one,
//     which is the bead's actual ask.
//   - an unresolvable open era must read NOTHING rather than fall back to
//     unscoped, because unscoped IS the bug. A missing yard costs discovery
//     latency; a dead-era yard costs API budget the live fleet needs.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

// eraWaypointRow writes a waypoint STAMPED with the era it was observed in, which
// is what GormWaypointRepository.Add does in production: era_id records the
// universe whose API answered for this waypoint, so it is direct evidence the
// system still exists rather than a judgement derived from it.
func eraWaypointRow(symbol, system string, traits []string, eraID int) persistence.WaypointModel {
	row := waypointRow(symbol, system, traits)
	row.EraID = &eraID
	return row
}

// eraInventoryRow writes one priced shipyard listing stamped with its era.
func eraInventoryRow(system, waypoint, shipType string, price, eraID int) persistence.ShipyardInventoryModel {
	return persistence.ShipyardInventoryModel{
		PlayerID:       testPlayerID,
		SystemSymbol:   system,
		WaypointSymbol: waypoint,
		ShipType:       shipType,
		PurchasePrice:  price,
		LastScanned:    time.Now().UTC(),
		EraID:          &eraID,
	}
}

func outstandingSymbols(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	outstanding, err := newCatalogPort(db).OutstandingYards(context.Background(), testPlayerID)
	require.NoError(t, err)
	symbols := make([]string, 0, len(outstanding))
	for _, yard := range outstanding {
		symbols = append(symbols, yard.Waypoint)
	}
	return symbols
}

// THE PRODUCTION REPRODUCTION, with the real symbols.
//
// A `waypoints` table holding SHIPYARD rows from dead eras alongside the live one
// must yield a work list containing ONLY the live-era yards. The dead-era systems
// here are the five the daemon was actually burning its budget on; X1-QR78-AE4F is
// a live era-5 yard that must survive, because a filter that read nothing would
// "fix" the bleed by turning the sweep off.
func TestOutstandingYards_DeadEraYardsAreNotWorkToDo(t *testing.T) {
	db := newShipPortsDB(t)
	era := eras(t, db)
	require.NoError(t, db.Create(&[]persistence.WaypointModel{
		// The live universe: these are the only yards the API can answer for.
		eraWaypointRow("X1-QR78-AE4F", "X1-QR78", []string{"SHIPYARD", "MARKETPLACE"}, era.Live),
		eraWaypointRow("X1-QR78-FE8C", "X1-QR78", []string{"SHIPYARD"}, era.Live),
		// Dead universes, spread across BOTH closed generations so that a predicate
		// written as "not the most recent dead era" cannot pass this test.
		eraWaypointRow("X1-AF2-A2", "X1-AF2", []string{"SHIPYARD"}, era.FirstDead),
		eraWaypointRow("X1-A27-CD9Z", "X1-A27", []string{"SHIPYARD"}, era.FirstDead),
		eraWaypointRow("X1-A19-CZ8X", "X1-A19", []string{"SHIPYARD"}, era.SecondDead),
		eraWaypointRow("X1-AA14-A18A", "X1-AA14", []string{"SHIPYARD"}, era.SecondDead),
		eraWaypointRow("X1-AC37-EB4C", "X1-AC37", []string{"SHIPYARD"}, era.SecondDead),
	}).Error)

	require.ElementsMatch(t, []string{"X1-QR78-AE4F", "X1-QR78-FE8C"}, outstandingSymbols(t, db),
		"a waypoint stamped by a universe that no longer exists is not outstanding work — "+
			"the API answers 404 for its whole system and the attempt still spends the tick's budget")
}

// A dead-era yard sitting in a system the LIVE universe also contains.
//
// This is the case that decides the scoping MECHANISM, and it is not
// hypothetical: measured live, 11 dead-era-stamped shipyard waypoints sit in
// systems that are present in the current era's sensing ledger. Scoping by
// ledger membership alone would therefore admit exactly the class of row being
// removed. The row's OWN era stamp is the only evidence that survives.
func TestOutstandingYards_ADeadEraRowInALiveSystemIsStillDead(t *testing.T) {
	db := newShipPortsDB(t)
	era := eras(t, db)
	require.NoError(t, db.Create(&[]persistence.WaypointModel{
		eraWaypointRow("X1-DV50-EE7D", "X1-DV50", []string{"SHIPYARD"}, era.Live),
		eraWaypointRow("X1-DV50-D13D", "X1-DV50", []string{"SHIPYARD"}, era.SecondDead),
	}).Error)
	// The system IS in this era's ledger, and watched. Neither fact rehabilitates
	// the dead-era row inside it.
	require.NoError(t, db.Create(&persistence.SensingSystemModel{
		PlayerID: testPlayerID, SystemSymbol: "X1-DV50", Verdict: "IN_SCOPE", EraID: &era.Live,
	}).Error)

	require.Equal(t, []string{"X1-DV50-EE7D"}, outstandingSymbols(t, db),
		"era scope is a property of the ROW, not of the system it sits in")
}

// THE BEAD'S ACTUAL ASK: the three yard reads must answer under ONE era rule.
//
// One fixture, one system, dead-era rows inside it. Every read that can see a yard
// at all must refuse the dead-era ones, and each must still return the live yards
// it is contracted to return — so this fails both if a read stays unscoped AND if
// a read over-scopes itself into silence.
//
// TWO dead yards, and the second is not redundant. A dead yard that carries a
// shipyard_inventory row is excluded from OutstandingYards by the already-held
// half of its set difference no matter what era it was stamped in, so asserting
// against that yard alone would make the OutstandingYards leg pass for a reason
// that has nothing to do with era scope. X1-QR78-GONE carries no reading at all,
// so era scope is the ONLY thing that can keep it out — verified by mutation:
// stripping the era filter puts it back in the work list and fails this test.
func TestYardReads_AgreeOnTheOpenEra(t *testing.T) {
	db := newShipPortsDB(t)
	era := eras(t, db)
	require.NoError(t, db.Create(&[]persistence.WaypointModel{
		// Priced in the live era: visible to all three reads' priced halves.
		eraWaypointRow("X1-QR78-AE4F", "X1-QR78", []string{"SHIPYARD"}, era.Live),
		// Charted in the live era but never priced: the trait-fallback half.
		eraWaypointRow("X1-QR78-FE8C", "X1-QR78", []string{"SHIPYARD"}, era.Live),
		// Charted in a dead era, and priced there too. Invisible to all three.
		eraWaypointRow("X1-QR78-DEAD", "X1-QR78", []string{"SHIPYARD"}, era.FirstDead),
		// Charted in a dead era and NEVER read. Nothing but era scope excludes it.
		eraWaypointRow("X1-QR78-GONE", "X1-QR78", []string{"SHIPYARD"}, era.SecondDead),
	}).Error)
	require.NoError(t, db.Create(&[]persistence.ShipyardInventoryModel{
		eraInventoryRow("X1-QR78", "X1-QR78-AE4F", "SHIP_PROBE", 40_000, era.Live),
		eraInventoryRow("X1-QR78", "X1-QR78-AE4F", "SHIP_HEAVY_FREIGHTER", 2_400_000, era.Live),
		eraInventoryRow("X1-QR78", "X1-QR78-DEAD", "SHIP_PROBE", 10, era.FirstDead),
		eraInventoryRow("X1-QR78", "X1-QR78-DEAD", "SHIP_HEAVY_FREIGHTER", 20, era.FirstDead),
	}).Error)

	port := newCatalogPort(db)
	ctx := context.Background()

	probeYards, err := port.ListProbeYards(ctx, "X1-QR78")
	require.NoError(t, err)
	heavyYards, err := port.ListHeavyYards(ctx, "X1-QR78")
	require.NoError(t, err)
	outstanding := outstandingSymbols(t, db)

	for _, read := range []struct {
		name  string
		yards []string
	}{
		{"ListProbeYards", probeYards},
		{"ListHeavyYards", heavyYards},
		{"OutstandingYards", outstanding},
	} {
		for _, dead := range []string{"X1-QR78-DEAD", "X1-QR78-GONE"} {
			require.NotContains(t, read.yards, dead,
				read.name+" still answers under a dead era; the three reads must agree or "+
					"one of them plans a hull against a universe that no longer exists")
		}
	}

	// And each read still returns what it is FOR. A dead-era filter that also
	// silenced the live answers would stop the bleed by stopping the engine.
	require.Equal(t, []string{"X1-QR78-AE4F", "X1-QR78-FE8C"}, probeYards,
		"the priced live yard leads, the never-priced live yard is still a candidate")
	require.Equal(t, []string{"X1-QR78-AE4F"}, heavyYards,
		"only PRICED live heavy rows are evidence; an unscanned yard asserts nothing")
	require.Equal(t, []string{"X1-QR78-FE8C"}, outstanding,
		"the live yard whose catalogue we do not hold, and only it")
}

// FAIL CLOSED. An unresolvable open era must read NOTHING.
//
// The fixture is the genuine post-reset shape: the universe has reset and no new
// era has been registered yet, so there is no open era to scope against. Falling
// back to unscoped there is precisely the bug — it would sweep every yard from
// every era that ever existed, which is the state that produced ~290 API failures
// an hour. Falling back to "the newest closed era" would be the same mistake with
// a smaller radius.
//
// Reading nothing is the safe direction and the engine above already handles it:
// ReadYardCatalogues treats a failure to ENUMERATE as fatal to the pass and
// reports an empty backlog rather than proceeding (pinned by
// TestReadYardCatalogues_AnUnreadableWorkListFailsThePass), and the reconcile
// collects that failure alongside the others rather than aborting the tick.
func TestYardReads_ReadNothingWhenTheOpenEraCannotBeResolved(t *testing.T) {
	for _, tc := range []struct {
		name string
		read func(*gorm.DB) (int, error)
	}{
		{"OutstandingYards", func(db *gorm.DB) (int, error) {
			yards, err := newCatalogPort(db).OutstandingYards(context.Background(), testPlayerID)
			return len(yards), err
		}},
		{"ListProbeYards", func(db *gorm.DB) (int, error) {
			yards, err := newCatalogPort(db).ListProbeYards(context.Background(), "X1-QR78")
			return len(yards), err
		}},
		{"ListHeavyYards", func(db *gorm.DB) (int, error) {
			yards, err := newCatalogPort(db).ListHeavyYards(context.Background(), "X1-QR78")
			return len(yards), err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newShipPortsDB(t)
			era := eras(t, db)
			// A world full of readable yards, so "nothing" cannot come from an
			// empty fixture. Some carry the era stamp that WAS live, and one
			// carries no stamp at all — the pre-backfill shape an unscoped
			// fallback would happily sweep.
			require.NoError(t, db.Create(&[]persistence.WaypointModel{
				eraWaypointRow("X1-QR78-AE4F", "X1-QR78", []string{"SHIPYARD"}, era.Live),
				eraWaypointRow("X1-QR78-DEAD", "X1-QR78", []string{"SHIPYARD"}, era.FirstDead),
				waypointRow("X1-QR78-NULL", "X1-QR78", []string{"SHIPYARD"}),
			}).Error)
			require.NoError(t, db.Create(&[]persistence.ShipyardInventoryModel{
				eraInventoryRow("X1-QR78", "X1-QR78-AE4F", "SHIP_PROBE", 40_000, era.Live),
				eraInventoryRow("X1-QR78", "X1-QR78-AE4F", "SHIP_HEAVY_FREIGHTER", 2_400_000, era.Live),
			}).Error)

			closeEveryEra(t, db)

			count, err := tc.read(db)
			require.Error(t, err, tc.name+" must refuse to answer when the open era cannot be resolved; "+
				"an unscoped fallback is the bug this closes")
			require.Zero(t, count, tc.name+" returned yards it could not era-scope")
		})
	}
}
