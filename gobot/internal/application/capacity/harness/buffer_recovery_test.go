package harness

// Acceptance harness for the buffer value-density score. It seeds the era-3
// demand shape the economy-analyst validated — the far-sourced home hub J58 and
// the mixed hub A1 — into a REAL test DB, drives the REAL SENSE adapter ->
// HeuristicPlanner, and reads the DesiredTopology buffer each hub wants: J58
// must recover a non-empty buffer (an empty buffer means 0 warehouses desired,
// so arming would strip the home hub) while the mixed hub A1 stays correct.
// Test doubles: only the sensor's live-API treasury boundary (the package's
// fakeTreasury). Everything else is the production read path over seeded rows.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
)

// Era-3 hubs and their source markets. Both hubs are COVERED (a live depot) so
// the coverage gate keeps them and the assertion is purely about the buffer.
const (
	era3HomeHub    = "X1-J58-A1" // far-sourced home hub (57 contracts in prod)
	era3HomeSource = "X1-J58-F1" // one far market at (420,560) -> distance 700
	era3MixedHub   = "X1-QA1-H1" // the mixed "A1" hub
	era3MixedSrc   = "X1-QA1-S1" // in-system market at (120,160) -> distance 200
)

// goodUnits is one contract good and its per-contract required units (== the
// AvgUnits the sensor derives, since every seeded contract carries the same
// amount). Chosen to match the analyst's per-hub buffered volume (J58 Σ=195,
// A1 Σ=143), both under the 240-unit default budget so the whole mix selects.
type goodUnits struct {
	good  string
	units int
}

// era3FarGoods is J58's good-mix: 8 goods ALL sourced ~700 away — the class the
// pre-fix score floored out. Σ avg units = 195.
var era3FarGoods = []goodUnits{
	{"MEDICINE", 28}, {"EQUIPMENT", 26}, {"POLYNUCLEOTIDES", 26}, {"ASSAULT_RIFLES", 25},
	{"FIREARMS", 24}, {"EXPLOSIVES", 24}, {"CLOTHING", 22}, {"AMMONIA_ICE", 20},
}

// era3MixedGoods is A1's good-mix: 6 goods including MEDICINE. Σ avg units = 143.
var era3MixedGoods = []goodUnits{
	{"ANTIMATTER", 30}, {"CLOTHING", 24}, {"JEWELRY", 20},
	{"MEDICINE", 25}, {"FOOD", 22}, {"EQUIPMENT", 22},
}

// TestHarness_CorrectedBufferScoreRecoversFarSourcedHomeHub drives the real
// sensor -> planner over the seeded era-3 world and asserts the analyst's
// validated recovery: J58 goes from EMPTY to a full 8-good buffer dominated by
// its far-sourced goods (MEDICINE included), and A1 keeps its 6-good buffer with
// MEDICINE still in it.
func TestHarness_CorrectedBufferScoreRecoversFarSourcedHomeHub(t *testing.T) {
	db := newScenarioDB(t)
	playerID := seedEra3DemandWorld(t, db)

	signals, err := newRealSensor(db).Sense(context.Background(), playerID)
	require.NoError(t, err)
	desired, err := capacity.NewHeuristicPlanner().ComputeDesired(context.Background(), signals, capacity.DefaultCalibration())
	require.NoError(t, err)

	// J58 RECOVERS: non-empty buffer holding every far-sourced good, MEDICINE in.
	j58 := findDesiredHub(t, desired, era3HomeHub)
	j58Buffer := bufferedGoodSymbols(j58.BufferedGoods)
	require.NotEmpty(t, j58Buffer,
		"the far-sourced home hub must recover a NON-EMPTY buffer (pre-fix: empty -> 0 warehouses -> arming strips the hub)")
	for _, g := range era3FarGoods {
		require.Containsf(t, j58Buffer, g.good, "far-sourced %s must be buffered under the corrected value-density score", g.good)
	}
	require.Contains(t, j58Buffer, "MEDICINE", "the analyst's headline recovery good must be present")
	require.Len(t, j58.BufferedGoods, len(era3FarGoods),
		"all 8 far goods (Σ195 avg-units) fit the 240 budget and select — the analyst's J58 target")
	require.Positive(t, j58.WarehouseCount, "a recovered buffer means warehouses are DESIRED again, not surplus")

	// A1 STAYS CORRECT: its 6-good buffer, MEDICINE still in it.
	a1 := findDesiredHub(t, desired, era3MixedHub)
	a1Buffer := bufferedGoodSymbols(a1.BufferedGoods)
	require.Len(t, a1.BufferedGoods, len(era3MixedGoods), "A1's 6-good buffer (Σ143 avg-units) is unchanged by the fix")
	for _, g := range era3MixedGoods {
		require.Containsf(t, a1Buffer, g.good, "%s must remain in A1's buffer", g.good)
	}
	require.Contains(t, a1Buffer, "MEDICINE", "MEDICINE must still be buffered at A1")
}

// seedEra3DemandWorld builds one player owning both hubs: each hub gets a covered
// depot, an in-system source market pricing every contract good, and three
// completed contracts over a 2h window carrying that good-mix.
func seedEra3DemandWorld(t *testing.T, db *gorm.DB) int {
	t.Helper()
	playerID := seedPlayer(t, db, "AGENT-ERA3")

	// J58: home hub at the origin, one FAR market at distance 700 selling all 8 goods.
	seedWaypoint(t, db, era3HomeHub, "X1-J58", 0, 0)
	seedWaypoint(t, db, era3HomeSource, "X1-J58", 420, 560) // hypot(420,560) == 700
	seedHubGoods(t, db, playerID, era3HomeHub, era3HomeSource, "j58", era3FarGoods)
	seedCoveredDepot(t, db, playerID, "depot-j58", era3HomeHub, era3HomeSource, "J58")

	// A1: mixed hub at the origin, one nearer market at distance 200 selling its 6 goods.
	seedWaypoint(t, db, era3MixedHub, "X1-QA1", 0, 0)
	seedWaypoint(t, db, era3MixedSrc, "X1-QA1", 120, 160) // hypot(120,160) == 200
	seedHubGoods(t, db, playerID, era3MixedHub, era3MixedSrc, "a1", era3MixedGoods)
	seedCoveredDepot(t, db, playerID, "depot-a1", era3MixedHub, era3MixedSrc, "A1")

	return playerID
}

// seedHubGoods prices every good in-system and seeds three completed contracts
// carrying the whole mix, each good at its fixed unit count so AvgUnits resolves
// to that count. The contracts span t0 → t0-12h, so the observation window is 12h
// and every good's frequency is a DILUTED 3/12h = 0.25/hr. J58's far
// (700-distance) goods and A1's near (200-distance) goods sit at opposite ends of
// the distance/frequency spectrum the value-density score must treat alike: J58
// is the RECOVERY case, A1 the CONTROL. The score has no floor and selects both
// hubs' full mix regardless of the diluted frequency.
func seedHubGoods(t *testing.T, db *gorm.DB, playerID int, hub, source, idPrefix string, mix []goodUnits) {
	t.Helper()
	for _, g := range mix {
		seedMarketSelling(t, db, playerID, source, g.good)
	}
	for i := 0; i < 3; i++ {
		deliveries := make([]contract.Delivery, 0, len(mix))
		for _, g := range mix {
			deliveries = append(deliveries, contract.Delivery{
				TradeSymbol: g.good, DestinationSymbol: hub, UnitsRequired: g.units, UnitsFulfilled: g.units,
			})
		}
		seedContract(t, db, playerID, fmt.Sprintf("%s-%d", idPrefix, i), deliveries,
			50000, 100000, t0.Add(-time.Duration(i*6)*time.Hour))
	}
}

// seedCoveredDepot stands the hub up as a live cluster (>=1 hull) so the coverage
// gate keeps it — one warehouse on the anchor, one stocker at the source, one worker.
func seedCoveredDepot(t *testing.T, db *gorm.DB, playerID int, id, hub, source, tag string) {
	t.Helper()
	seedDepot(t, db, playerID, id,
		[]depot.Element{{Waypoint: hub, ShipSymbol: "WH-" + tag}},
		[]depot.Element{{Waypoint: source, ShipSymbol: "ST-" + tag}},
		[]depot.Element{{Waypoint: hub, ShipSymbol: "DL-" + tag}})
}

// findDesiredHub returns the planned hub by symbol, failing if the planner did
// not keep it (a dropped hub is a coverage-gate failure, not a buffer result).
func findDesiredHub(t *testing.T, desired capacity.DesiredTopology, hubSymbol string) capacity.DesiredHub {
	t.Helper()
	for _, hub := range desired.Hubs {
		if hub.HubSymbol == hubSymbol {
			return hub
		}
	}
	require.Failf(t, "hub not planned", "%s missing from DesiredTopology %v", hubSymbol, desired.Hubs)
	return capacity.DesiredHub{}
}

func bufferedGoodSymbols(goods []capacity.DesiredBufferedGood) []string {
	names := make([]string, 0, len(goods))
	for _, g := range goods {
		names = append(names, g.Good)
	}
	return names
}
