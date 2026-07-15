package persistence_test

// Integration test for the sp-42ow live regression (shipped-but-never-emits):
// the scout's piggybacked shipyard scan silently no-ops at virtually every
// shipyard because the immutable SHIPYARD trait was read through FindBySymbol,
// whose era-scope + 24h-TTL gates are correct only for VOLATILE price/nav data.
// These tests wire the REAL GormWaypointRepository and the REAL shipyard
// inventory store (the harness that hid the bug: the scanner unit tests stubbed
// the trait reader and never hit the real gate) to prove a stale/prior-era
// shipyard now scans and persists.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// stubShipyardAPI is the driven-port stub for the ONE live shipyard read the
// scanner makes once its trait gate opens. gets records the call count so the
// test can prove the no-op gate was actually passed (a real scan proceeded)
// rather than an inventory row arriving by some other route.
type stubShipyardAPI struct {
	data *domainPorts.ShipyardData
	gets int
}

func (s *stubShipyardAPI) GetShipyard(context.Context, string, string, string) (*domainPorts.ShipyardData, error) {
	s.gets++
	return s.data, nil
}

// TestShipyardScan_StaleAndPriorEraShipyard_StillScansAndPersists reproduces the
// live incident at the REAL persistence boundary: a SHIPYARD-trait waypoint
// whose cached row is BOTH (a) from a prior (closed) era than the open era AND
// (b) synced >24h ago — exactly the shape of ~97 of the 108 real SHIPYARD
// waypoints (only 11 era-current, only 8 fresh). The scout is physically AT the
// waypoint, so its immutable SHIPYARD trait must open the scan gate regardless
// of era-scope or cache TTL. Before the fix the era+TTL gates reject the row,
// isShipyardWaypoint reads "not a shipyard", and the scan is a silent no-op so
// shipyard_inventory stays empty forever. After the fix the scan proceeds and
// persists a row readable via the same era-scoped path the fleet autosizer uses.
func TestShipyardScan_StaleAndPriorEraShipyard_StillScansAndPersists(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	// A closed prior era and the live open era (open = highest era_id, no closed_at).
	closedAt := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "torwind", AgentSymbol: "TORWIND", PlayerID: 1, ClosedAt: &closedAt}).Error)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 2}).Error)
	var priorEra persistence.EraModel
	require.NoError(t, db.Where("name = ?", "torwind").First(&priorEra).Error)
	priorID := priorEra.EraID

	// The incident row: SHIPYARD trait, PRIOR era, synced 48h ago (past the TTL).
	staleSync := time.Now().Add(-48 * time.Hour).Format(time.RFC3339)
	require.NoError(t, db.Create(&persistence.WaypointModel{
		WaypointSymbol: "X1-STALE-YARD",
		SystemSymbol:   "X1-STALE",
		Type:           "ORBITAL_STATION",
		X:              1, Y: 1,
		Traits:   `["SHIPYARD"]`,
		SyncedAt: staleSync,
		EraID:    &priorID,
	}).Error)

	waypointRepo := persistence.NewGormWaypointRepository(db)
	inventoryRepo := persistence.NewShipyardInventoryRepository(db)
	api := &stubShipyardAPI{data: &domainPorts.ShipyardData{
		Symbol:    "X1-STALE-YARD",
		ShipTypes: []domainPorts.ShipTypeInfo{{Type: "SHIP_BULK_FREIGHTER"}},
		Ships: []domainPorts.ShipListingData{
			{Type: "SHIP_BULK_FREIGHTER", PurchasePrice: 2_000_000, Supply: "LOW"},
		},
	}}
	scanner := ship.NewShipyardScanner(api, inventoryRepo, waypointRepo, nil, shipyard.NewHeavyShipTypeSet(nil))

	const playerID = 2
	ctx := common.WithPlayerToken(context.Background(), "test-token")
	require.NoError(t, scanner.ScanAndSaveShipyard(ctx, playerID, "X1-STALE-YARD"))

	// The trait gate opened: the one live shipyard read happened.
	require.Equal(t, 1, api.gets, "the immutable SHIPYARD trait must open the scan gate despite prior-era + stale cache")

	// And the scan persisted into the LIVE era inventory, readable via the same
	// era-scoped read the fleet autosizer's heavy branch consumes.
	rows, err := inventoryRepo.ListByTypes(ctx, playerID, []string{"SHIP_BULK_FREIGHTER"})
	require.NoError(t, err)
	require.Len(t, rows, 1, "a stale/prior-era shipyard visit must persist a shipyard_inventory row")
	require.Equal(t, "X1-STALE-YARD", rows[0].WaypointSymbol)
	require.Equal(t, 2_000_000, rows[0].PurchasePrice)
}

// TestHasWaypointTrait_ReadsImmutableTraitIgnoringEraAndTTL pins the dedicated
// immutable-trait read directly at the real repository boundary. It must answer
// true for a SHIPYARD row that is BOTH prior-era AND stale (the exact rows
// FindBySymbol's era+TTL gates were filtering out), false for a cached
// non-shipyard row (so a market-only visit stays a no-op and spends no shipyard
// API budget — the invariant the fix must preserve), and false — never an error —
// for an uncached symbol.
func TestHasWaypointTrait_ReadsImmutableTraitIgnoringEraAndTTL(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	closedAt := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "torwind", AgentSymbol: "TORWIND", PlayerID: 1, ClosedAt: &closedAt}).Error)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 2}).Error)
	var priorEra persistence.EraModel
	require.NoError(t, db.Where("name = ?", "torwind").First(&priorEra).Error)
	priorID := priorEra.EraID

	// Both rows are prior-era AND stale (past the 24h TTL): the shape FindBySymbol rejects.
	staleSync := time.Now().Add(-48 * time.Hour).Format(time.RFC3339)
	require.NoError(t, db.Create(&persistence.WaypointModel{
		WaypointSymbol: "X1-OLD-YARD", SystemSymbol: "X1-OLD", Type: "ORBITAL_STATION",
		Traits: `["MARKETPLACE","SHIPYARD"]`, SyncedAt: staleSync, EraID: &priorID,
	}).Error)
	require.NoError(t, db.Create(&persistence.WaypointModel{
		WaypointSymbol: "X1-OLD-MKT", SystemSymbol: "X1-OLD", Type: "PLANET",
		Traits: `["MARKETPLACE"]`, SyncedAt: staleSync, EraID: &priorID,
	}).Error)

	repo := persistence.NewGormWaypointRepository(db)
	cases := []struct {
		name     string
		symbol   string
		expected bool
	}{
		{"prior-era stale shipyard reads as a shipyard", "X1-OLD-YARD", true},
		{"cached non-shipyard stays a no-op (no API budget)", "X1-OLD-MKT", false},
		{"uncached symbol is not a shipyard (nil error)", "X1-OLD-UNKNOWN", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			has, err := repo.HasWaypointTrait(context.Background(), tc.symbol, "SHIPYARD")
			require.NoError(t, err)
			require.Equal(t, tc.expected, has)
		})
	}
}
