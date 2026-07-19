package capacity_test

// Integration tests for the capacity SENSE adapter.
//
// The Sensor is an ADAPTER (hexagonal driven side): per the testing mandates it
// gets integration tests against a REAL database (the sqlite :memory: harness
// every persistence repository test uses) — mocking the DB here would test the
// mock, not the adapter. Test doubles appear ONLY at the two non-DB boundaries:
// the live-API treasury reader (fakeTreasury) and the in-memory duty-cycle KPI
// seam (an injected report func; the real sampler only accumulates from a live
// daemon ticker).
//
// Test budget: 9 distinct behaviors × 2 = 18 max tests. 9 written:
//  1. Demand      — contract history aggregates into per-hub frequency, mean
//                   payment, and per-good mix.
//  2. Performance — accept/fulfill ledger events aggregate into per-hub mean
//                   cycle time.
//  3. Topology    — contract depots project into cluster states with
//                   event-sourced warehouse buffers and active-container caps.
//  4. Utilization — ships rows project into per-hull utilization.
//  5. Economics   — treasury + trailing-window income velocity + per-hull rate
//                   with FleetHullCount ≡ len(Utilization.Hulls).
//  6. Distances   — per (hub, good) source-distance resolution (in-system
//                   Euclidean / cross-system tier / no-source drop are input
//                   variations of the one resolution behavior).
//  7. Graceful    — empty sources yield empty families + no error; a failing
//                   treasury fails CLOSED to 0; snapshot is always stamped.
//  8. Scoping     — another player's rows never leak into the snapshot.
//  9. Reuse       — Topology.IdleHulls carries the reuse-eligible idle subset of
//                   the SAME Utilization.Hulls snapshot (idle && undedicated &&
//                   not already serving a cluster role) so DIFF's tier-1 rung has
//                   a free lever.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	capacityAdapters "github.com/andrescamacho/spacetraders-go/internal/adapters/capacity"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	"github.com/andrescamacho/spacetraders-go/internal/domain/dutycycle"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// t0 is the frozen "now" of every test (injected MockClock).
var t0 = time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)

const (
	hubWaypoint    = "X1-TT77-H1"
	sourceWaypoint = "X1-TT77-S1" // at (30,40) from hub (0,0): distance exactly 50
)

// fakeTreasury doubles the ONLY live-API boundary the sensor touches.
type fakeTreasury struct {
	credits int
	err     error
}

func (f fakeTreasury) LiveCredits(context.Context, shared.PlayerID) (int, error) {
	return f.credits, f.err
}

// --- fixture ----------------------------------------------------------------

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	return db
}

func createPlayer(t *testing.T, db *gorm.DB, agentSymbol string) int {
	t.Helper()
	player := persistence.PlayerModel{AgentSymbol: agentSymbol, Token: "tok", CreatedAt: t0}
	require.NoError(t, db.Create(&player).Error)
	return player.ID
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return string(raw)
}

func rfc3339(ts time.Time) string { return ts.UTC().Format(time.RFC3339) }

func seedContract(t *testing.T, db *gorm.DB, playerID int, id string, deliveries []contract.Delivery, onAccepted, onFulfilled int, lastUpdated time.Time) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.ContractModel{
		ID:                 id,
		PlayerID:           playerID,
		FactionSymbol:      "COSMIC",
		Type:               "PROCUREMENT",
		Accepted:           true,
		Fulfilled:          true,
		DeadlineToAccept:   rfc3339(t0.Add(24 * time.Hour)),
		Deadline:           rfc3339(t0.Add(48 * time.Hour)),
		PaymentOnAccepted:  onAccepted,
		PaymentOnFulfilled: onFulfilled,
		DeliveriesJSON:     mustJSON(t, deliveries),
		LastUpdated:        rfc3339(lastUpdated),
	}).Error)
}

func seedTransaction(t *testing.T, db *gorm.DB, playerID int, id, txType, relatedType, relatedID string, amount int, at time.Time) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID:                id,
		PlayerID:          playerID,
		Timestamp:         at,
		TransactionType:   txType,
		Category:          "test",
		Amount:            amount,
		RelatedEntityType: relatedType,
		RelatedEntityID:   relatedID,
	}).Error)
}

func seedWaypoint(t *testing.T, db *gorm.DB, symbol, system string, x, y float64) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.WaypointModel{
		WaypointSymbol: symbol,
		SystemSymbol:   system,
		Type:           "PLANET",
		X:              x,
		Y:              y,
		Traits:         "[]",
		Orbitals:       "[]",
	}).Error)
}

func seedShip(t *testing.T, db *gorm.DB, playerID int, symbol, location, system string, containerID *string, assignmentStatus, dedicatedFleet string) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       symbol,
		PlayerID:         playerID,
		LocationSymbol:   location,
		SystemSymbol:     system,
		ContainerID:      containerID,
		AssignmentStatus: assignmentStatus,
		DedicatedFleet:   dedicatedFleet,
		CargoCapacity:    80,
	}).Error)
}

// seedWarehouseContainer persists a warehouse container THROUGH the production
// repository (ContainerRepositoryGORM.Add + UpdateStatus) instead of
// hand-inserting a ContainerModel, so the stored container_type is exactly what
// production writers store (uppercase container.ContainerTypeWarehouse) and the
// seed can never silently drift from the write path again — a hand-typed
// lowercase "warehouse" seed once masked a sensor query that matched zero
// production rows.
func seedWarehouseContainer(t *testing.T, db *gorm.DB, playerID int, id string, config map[string]interface{}, status container.ContainerStatus) {
	t.Helper()
	repo := persistence.NewContainerRepository(db)
	entity := container.NewContainer(id, container.ContainerTypeWarehouse, playerID, -1, nil, config, nil)
	require.NoError(t, repo.Add(context.Background(), entity, "warehouse"))
	require.NoError(t, repo.UpdateStatus(context.Background(), id, playerID, status, nil, nil, ""))
}

// seedWorld builds the canonical one-hub world every populated-family test
// reads. Player A runs one contract depot anchored at hubWaypoint, has two
// completed contracts there, a warehouse with event-sourced buffer, three
// hulls, and market data for two of the three demanded goods.
func seedWorld(t *testing.T, db *gorm.DB) int {
	t.Helper()
	playerID := createPlayer(t, db, "AGENT-A")

	// Contract history: two completed contracts at the hub, 2h observation window.
	seedContract(t, db, playerID, "c1",
		[]contract.Delivery{{TradeSymbol: "IRON", DestinationSymbol: hubWaypoint, UnitsRequired: 60, UnitsFulfilled: 60}},
		10000, 30000, t0.Add(-2*time.Hour))
	seedContract(t, db, playerID, "c2",
		[]contract.Delivery{
			{TradeSymbol: "IRON", DestinationSymbol: hubWaypoint, UnitsRequired: 40, UnitsFulfilled: 40},
			{TradeSymbol: "COPPER", DestinationSymbol: hubWaypoint, UnitsRequired: 100, UnitsFulfilled: 100},
			{TradeSymbol: "GOLD", DestinationSymbol: hubWaypoint, UnitsRequired: 10, UnitsFulfilled: 10},
		},
		20000, 40000, t0)

	// Ledger: accept→fulfill event pairs (cycle times 1800s and 900s) plus a
	// fuel expense inside the trailing 1h income window.
	seedTransaction(t, db, playerID, "tx-a1", "CONTRACT_ACCEPTED", "contract", "c1", 10000, t0.Add(-2*time.Hour))
	seedTransaction(t, db, playerID, "tx-f1", "CONTRACT_FULFILLED", "contract", "c1", 30000, t0.Add(-90*time.Minute))
	seedTransaction(t, db, playerID, "tx-a2", "CONTRACT_ACCEPTED", "contract", "c2", 20000, t0.Add(-30*time.Minute))
	seedTransaction(t, db, playerID, "tx-f2", "CONTRACT_FULFILLED", "contract", "c2", 40000, t0.Add(-15*time.Minute))
	seedTransaction(t, db, playerID, "tx-fuel", "PURCHASE_FUEL", "", "", -1000, t0.Add(-20*time.Minute))

	// Active warehouse container carrying the per-good caps, plus a STOPPED one
	// whose caps must NOT leak into the snapshot. Seeded through the production
	// container repository so the row matches production casing ("WAREHOUSE").
	seedWarehouseContainer(t, db, playerID, "warehouse-1", map[string]interface{}{
		"ship_symbol":     "WH-1",
		"waypoint_symbol": hubWaypoint,
		"supported_goods": []string{"IRON", "COPPER"},
		"target_units":    map[string]int{"COPPER": 60, "IRON": 120},
		"operation":       "warehouse",
	}, container.ContainerStatusRunning)
	seedWarehouseContainer(t, db, playerID, "warehouse-old", map[string]interface{}{
		"ship_symbol":     "WH-1",
		"waypoint_symbol": hubWaypoint,
		"target_units":    map[string]int{"IRON": 999},
		"operation":       "warehouse",
	}, container.ContainerStatusStopped)

	// Fleet: warehouse hull flying its container, stocker + worker idle.
	warehouseContainer := "warehouse-1"
	seedShip(t, db, playerID, "WH-1", hubWaypoint, "X1-TT77", &warehouseContainer, "active", "contract")
	seedShip(t, db, playerID, "ST-1", sourceWaypoint, "X1-TT77", nil, "idle", "contract")
	seedShip(t, db, playerID, "DL-1", hubWaypoint, "X1-TT77", nil, "idle", "")

	// One contract depot anchored at the hub: crewed warehouse, one crewed + one
	// declared-but-uncrewed stocker, one delivery worker.
	require.NoError(t, db.Create(&persistence.ContractDepotModel{
		ID:            "depot-1",
		PlayerID:      playerID,
		Warehouses:    mustJSON(t, []depot.Element{{Waypoint: hubWaypoint, ShipSymbol: "WH-1"}}),
		Stockers:      mustJSON(t, []depot.Element{{Waypoint: sourceWaypoint, ShipSymbol: "ST-1"}, {Waypoint: sourceWaypoint, ShipSymbol: ""}}),
		DeliveryHulls: mustJSON(t, []depot.Element{{Waypoint: hubWaypoint, ShipSymbol: "DL-1"}}),
		SourceHubs:    mustJSON(t, []depot.Element{{Waypoint: sourceWaypoint, ShipSymbol: ""}}),
	}).Error)

	// Event-sourced warehouse fill: +80 IRON, +20 IRON, +50 COPPER, −30 IRON.
	stockings := []persistence.WarehouseStockingModel{
		{Good: "IRON", Units: 80, WarehouseWaypoint: hubWaypoint, SourceWaypoint: sourceWaypoint, ShipSymbol: "ST-1", PlayerID: playerID, DepositedAt: t0.Add(-3 * time.Hour)},
		{Good: "IRON", Units: 20, WarehouseWaypoint: hubWaypoint, SourceWaypoint: sourceWaypoint, ShipSymbol: "ST-1", PlayerID: playerID, DepositedAt: t0.Add(-2 * time.Hour)},
		{Good: "COPPER", Units: 50, WarehouseWaypoint: hubWaypoint, SourceWaypoint: sourceWaypoint, ShipSymbol: "ST-1", PlayerID: playerID, DepositedAt: t0.Add(-time.Hour)},
	}
	for i := range stockings {
		require.NoError(t, db.Create(&stockings[i]).Error)
	}
	require.NoError(t, db.Create(&persistence.WarehouseWithdrawalModel{
		Good: "IRON", Units: 30, Waypoint: hubWaypoint, ShipSymbol: "DL-1", ContractID: "c1", PlayerID: playerID, WithdrawnAt: t0.Add(-30 * time.Minute),
	}).Error)

	// Geometry + markets: IRON sold in-system at (30,40) → distance 50 from the
	// hub at (0,0); COPPER sold only cross-system; GOLD sold nowhere.
	seedWaypoint(t, db, hubWaypoint, "X1-TT77", 0, 0)
	seedWaypoint(t, db, sourceWaypoint, "X1-TT77", 30, 40)
	require.NoError(t, db.Create(&persistence.MarketData{
		WaypointSymbol: sourceWaypoint, GoodSymbol: "IRON", PurchasePrice: 90, SellPrice: 100, TradeVolume: 60, LastUpdated: t0, PlayerID: playerID,
	}).Error)
	require.NoError(t, db.Create(&persistence.MarketData{
		WaypointSymbol: "X9-ZZ99-A1", GoodSymbol: "COPPER", PurchasePrice: 40, SellPrice: 55, TradeVolume: 35, LastUpdated: t0, PlayerID: playerID,
	}).Error)

	return playerID
}

func newSensorUnderTest(db *gorm.DB, treasury capacityAdapters.TreasuryReader) *capacityAdapters.Sensor {
	return capacityAdapters.NewSensor(db, treasury,
		capacityAdapters.WithSensorClock(&shared.MockClock{CurrentTime: t0}),
		capacityAdapters.WithDutyCycleReport(func() dutycycle.Report {
			return dutycycle.Report{Hulls: []dutycycle.HullDutyCycle{
				{Hull: "WH-1", EarningPct: 90},
				{Hull: "ST-1", EarningPct: 25},
			}}
		}),
	)
}

// --- behaviors ---------------------------------------------------------------

// Behavior 1: contract history aggregates into per-hub demand — contracts/hour
// over the observed window (earliest LastUpdated → now), mean total payment,
// and per-good frequency + mean units.
func TestSense_AggregatesHubDemandFromContractHistory(t *testing.T) {
	db := newTestDB(t)
	playerID := seedWorld(t, db)

	signals, err := newSensorUnderTest(db, fakeTreasury{credits: 123456}).Sense(context.Background(), playerID)

	require.NoError(t, err)
	require.Len(t, signals.Demand.Hubs, 1)
	hub := signals.Demand.Hubs[0]
	require.Equal(t, hubWaypoint, hub.HubSymbol)
	// 2 contracts over the 2h window (earliest LastUpdated t0−2h → now t0).
	require.InDelta(t, 1.0, hub.ContractFrequency, 1e-9)
	// Mean of (10000+30000) and (20000+40000).
	require.InDelta(t, 50000.0, hub.AvgPaymentCredits, 1e-9)
	require.Equal(t, []capacity.GoodDemand{
		{Good: "COPPER", Frequency: 0.5, AvgUnits: 100},
		{Good: "GOLD", Frequency: 0.5, AvgUnits: 10},
		{Good: "IRON", Frequency: 1.0, AvgUnits: 50},
	}, hub.GoodMix)
}

// Behavior 1 (anti-dilution): demand frequency is measured over
// the recent-N COUNT window — the most recent N contracts and the span THEY
// occupy — not the ever-growing wall-clock gap back to the player's first-ever
// contract. An established hub whose ancient history has accumulated far past N
// keeps a healthy, un-diluted frequency: the old contracts age out of both the
// numerator and the denominator instead of stretching the window toward zero.
//
// Seed 3 recent IRON contracts spanning 2h plus 2 ANCIENT (t0−1000h) OLD_GOOD
// contracts, and cap the window at N=3. The recent 3 define a 2h window ⇒
// 3/2h = 1.5/hr; the ancient pair is excluded entirely (OLD_GOOD never appears).
// The pre-fix wall-clock window would have spanned 1000h+ ⇒ ≈0.005/hr and still
// listed OLD_GOOD — so this asserts the count window is load-bearing, not cosmetic.
func TestSense_MeasuresDemandOverRecentContractCountWindow_NotDilutedByAncientHistory(t *testing.T) {
	db := newTestDB(t)
	playerID := createPlayer(t, db, "AGENT-ESTABLISHED")

	ironTo := func(id string, at time.Time) {
		seedContract(t, db, playerID, id,
			[]contract.Delivery{{TradeSymbol: "IRON", DestinationSymbol: hubWaypoint, UnitsRequired: 30, UnitsFulfilled: 30}},
			5000, 10000, at)
	}
	ironTo("recent-0", t0) // newest
	ironTo("recent-1", t0.Add(-1*time.Hour))
	ironTo("recent-2", t0.Add(-2*time.Hour)) // oldest of the recent 3 ⇒ 2h window
	// Ancient history far beyond the count window — the old wall-clock denominator.
	for _, ancient := range []struct {
		id string
		at time.Time
	}{
		{"ancient-0", t0.Add(-1000 * time.Hour)},
		{"ancient-1", t0.Add(-1001 * time.Hour)},
	} {
		seedContract(t, db, playerID, ancient.id,
			[]contract.Delivery{{TradeSymbol: "OLD_GOOD", DestinationSymbol: hubWaypoint, UnitsRequired: 30, UnitsFulfilled: 30}},
			5000, 10000, ancient.at)
	}

	sensor := capacityAdapters.NewSensor(db, fakeTreasury{credits: 1},
		capacityAdapters.WithSensorClock(&shared.MockClock{CurrentTime: t0}),
		capacityAdapters.WithDemandWindowContractCount(3),
	)
	signals, err := sensor.Sense(context.Background(), playerID)

	require.NoError(t, err)
	require.Len(t, signals.Demand.Hubs, 1)
	hub := signals.Demand.Hubs[0]
	require.InDelta(t, 1.5, hub.ContractFrequency, 1e-9,
		"3 recent contracts over their own 2h span = 1.5/hr — NOT 5 contracts over 1000h+ ≈ 0.005/hr")
	require.Equal(t, []capacity.GoodDemand{{Good: "IRON", Frequency: 1.5, AvgUnits: 30}}, hub.GoodMix,
		"the ancient OLD_GOOD contracts aged out of the count window entirely")
}

// Behavior 2: CONTRACT_ACCEPTED→CONTRACT_FULFILLED ledger pairs aggregate into
// the hub's mean cycle time (c1: 1800s, c2: 900s → 1350s).
func TestSense_MeasuresAcceptToFulfillCycleTimePerHub(t *testing.T) {
	db := newTestDB(t)
	playerID := seedWorld(t, db)

	signals, err := newSensorUnderTest(db, fakeTreasury{credits: 123456}).Sense(context.Background(), playerID)

	require.NoError(t, err)
	require.Len(t, signals.Performance.Hubs, 1)
	hub := signals.Performance.Hubs[0]
	require.Equal(t, hubWaypoint, hub.HubSymbol)
	require.InDelta(t, 1350.0, hub.CycleTimeSeconds, 1e-9)
	require.Zero(t, hub.StallEvents) // no persisted stall source exists yet
}

// Behavior 3: contract depots project into cluster states — hub = the depot's
// anchor warehouse waypoint, warehouse buffer is the event-sourced net of
// stockings − withdrawals, and caps come from the ACTIVE warehouse container
// only (the stopped container's caps must not leak).
func TestSense_ProjectsDepotTopologyWithBufferAndCaps(t *testing.T) {
	db := newTestDB(t)
	playerID := seedWorld(t, db)

	signals, err := newSensorUnderTest(db, fakeTreasury{credits: 123456}).Sense(context.Background(), playerID)

	require.NoError(t, err)
	require.Len(t, signals.Topology.Clusters, 1)
	cluster := signals.Topology.Clusters[0]
	require.Equal(t, hubWaypoint, cluster.HubSymbol)
	require.Equal(t, []capacity.WarehouseState{{
		ShipSymbol: "WH-1",
		Waypoint:   hubWaypoint,
		Buffer: []capacity.BufferedStock{
			{Good: "COPPER", Units: 50},
			{Good: "IRON", Units: 70}, // 80+20 stocked − 30 withdrawn
		},
		GoodCaps: map[string]int{"COPPER": 60, "IRON": 120},
	}}, cluster.Warehouses)
	require.Equal(t, []capacity.StockerState{
		{ShipSymbol: "ST-1", Waypoint: sourceWaypoint},
		{ShipSymbol: "", Waypoint: sourceWaypoint}, // declared-but-uncrewed slot preserved
	}, cluster.Stockers)
	require.Equal(t, []capacity.WorkerState{{ShipSymbol: "DL-1", Waypoint: hubWaypoint}}, cluster.Workers)
}

// Behavior 4: ships rows project into per-hull utilization — dedication tag,
// position, idle = no container flying the hull, duty-cycle pct from the KPI
// seam (0 for a hull the sampler has not observed).
func TestSense_ReportsPerHullUtilization(t *testing.T) {
	db := newTestDB(t)
	playerID := seedWorld(t, db)

	signals, err := newSensorUnderTest(db, fakeTreasury{credits: 123456}).Sense(context.Background(), playerID)

	require.NoError(t, err)
	require.Equal(t, []capacity.HullUtilization{
		{ShipSymbol: "DL-1", DedicatedFleet: "", Waypoint: hubWaypoint, DutyCyclePct: 0, Idle: true},
		{ShipSymbol: "ST-1", DedicatedFleet: "contract", Waypoint: sourceWaypoint, DutyCyclePct: 25, Idle: true},
		{ShipSymbol: "WH-1", DedicatedFleet: "contract", Waypoint: hubWaypoint, DutyCyclePct: 90, Idle: false},
	}, signals.Utilization.Hulls)
}

// Behavior 5: economics — live treasury, net income velocity over the trailing
// 1h ledger window (20000+40000−1000), per-hull rate = velocity ÷ hull count,
// FleetHullCount ≡ len(Utilization.Hulls), and per-hub crewed-stocker load.
func TestSense_CollectsEconomics(t *testing.T) {
	db := newTestDB(t)
	playerID := seedWorld(t, db)

	signals, err := newSensorUnderTest(db, fakeTreasury{credits: 123456}).Sense(context.Background(), playerID)

	require.NoError(t, err)
	require.Equal(t, int64(123456), signals.Economics.TreasuryCredits)
	require.InDelta(t, 59000.0, signals.Economics.IncomeVelocityPerHour, 1e-9)
	require.Equal(t, 3, signals.Economics.FleetHullCount)
	require.Equal(t, len(signals.Utilization.Hulls), signals.Economics.FleetHullCount)
	require.InDelta(t, 59000.0/3.0, signals.Economics.FleetPerHullCrHr, 1e-9)
	require.Equal(t, []capacity.StockerLoad{
		{HubSymbol: hubWaypoint, ActiveStockers: 1, LoadPct: 0}, // uncrewed slot doesn't count; no load source yet
	}, signals.Economics.StockerLoad)
}

// Behavior 6: source-distance resolution per (hub, good in demand mix) —
// in-system source → Euclidean hub→market distance; cross-system-only source →
// the coarse cross-system tier; a good sold nowhere is dropped (fail-closed,
// mirroring the demand miner).
func TestSense_ResolvesSourceDistances(t *testing.T) {
	db := newTestDB(t)
	playerID := seedWorld(t, db)

	signals, err := newSensorUnderTest(db, fakeTreasury{credits: 123456}).Sense(context.Background(), playerID)

	require.NoError(t, err)
	require.Equal(t, []capacity.GoodSourceDistance{
		{HubSymbol: hubWaypoint, Good: "COPPER", Distance: capacityAdapters.DefaultCrossSystemSourceDistance},
		{HubSymbol: hubWaypoint, Good: "IRON", Distance: 50}, // (0,0)→(30,40)
	}, signals.Economics.SourceDistances)
}

// Behavior 7: graceful partial — with NO seeded sources every family comes back
// empty WITHOUT an error (partial real signal beats a blocked engine), a
// failing live-treasury read fails CLOSED to 0 credits, and the snapshot is
// still stamped with player + collection time.
func TestSense_EmptySourcesYieldEmptyFamiliesWithoutError(t *testing.T) {
	db := newTestDB(t)
	playerID := createPlayer(t, db, "AGENT-EMPTY")

	signals, err := newSensorUnderTest(db, fakeTreasury{err: context.DeadlineExceeded}).Sense(context.Background(), playerID)

	require.NoError(t, err)
	require.Equal(t, playerID, signals.PlayerID)
	require.Equal(t, t0, signals.CollectedAt)
	require.Empty(t, signals.Demand.Hubs)
	require.Empty(t, signals.Performance.Hubs)
	require.Empty(t, signals.Topology.Clusters)
	require.Empty(t, signals.Utilization.Hulls)
	require.Equal(t, int64(0), signals.Economics.TreasuryCredits)
	require.Zero(t, signals.Economics.IncomeVelocityPerHour)
	require.Zero(t, signals.Economics.FleetPerHullCrHr)
	require.Zero(t, signals.Economics.FleetHullCount)
	require.Empty(t, signals.Economics.SourceDistances)
	require.Empty(t, signals.Economics.StockerLoad)
}

// Behavior 8: player scoping — a second player's contracts, ships, depots, and
// ledger rows never leak into the requested player's snapshot.
func TestSense_ScopesToRequestedPlayer(t *testing.T) {
	db := newTestDB(t)
	playerID := seedWorld(t, db)
	otherID := createPlayer(t, db, "AGENT-B")
	seedContract(t, db, otherID, "cb-1",
		[]contract.Delivery{{TradeSymbol: "IRON", DestinationSymbol: "X1-TT77-H9", UnitsRequired: 5, UnitsFulfilled: 5}},
		1000, 2000, t0)
	seedTransaction(t, db, otherID, "tx-b1", "CONTRACT_FULFILLED", "contract", "cb-1", 77777, t0.Add(-10*time.Minute))
	seedShip(t, db, otherID, "B-1", "X1-TT77-H9", "X1-TT77", nil, "idle", "")
	require.NoError(t, db.Create(&persistence.ContractDepotModel{
		ID:            "depot-b",
		PlayerID:      otherID,
		Warehouses:    mustJSON(t, []depot.Element{{Waypoint: "X1-TT77-H9", ShipSymbol: "B-WH"}}),
		Stockers:      mustJSON(t, []depot.Element{}),
		DeliveryHulls: mustJSON(t, []depot.Element{}),
		SourceHubs:    mustJSON(t, []depot.Element{}),
	}).Error)

	signals, err := newSensorUnderTest(db, fakeTreasury{credits: 123456}).Sense(context.Background(), playerID)

	require.NoError(t, err)
	require.Len(t, signals.Demand.Hubs, 1)
	require.Equal(t, hubWaypoint, signals.Demand.Hubs[0].HubSymbol)
	require.Len(t, signals.Topology.Clusters, 1)
	require.Equal(t, hubWaypoint, signals.Topology.Clusters[0].HubSymbol)
	require.Equal(t, 3, signals.Economics.FleetHullCount) // B-1 excluded
	require.InDelta(t, 59000.0, signals.Economics.IncomeVelocityPerHour, 1e-9)
}

// Behavior 9: the SENSE lane fills Topology.IdleHulls with the tier-1
// REUSE-ELIGIBLE subset of the SAME hull snapshot Utilization carries — idle AND
// undedicated AND not already serving a cluster role. Diff receives ONLY
// TopologySignals, so an unfilled slice silently starves the reuse-first rung and
// every hull gap escalates straight to tier-4 capital. All four eligibility cases
// are drawn from one real snapshot:
//
//	(a) FREE-1 idle, undedicated, in no depot     -> PRESENT (the free lever);
//	(b) DED-1  idle, DEDICATED, in no depot        -> absent (never poach a pin);
//	(c) DL-1   idle, undedicated, a cluster worker -> absent (already serving);
//	(d) WH-1   flying its container (busy)         -> absent (not idle).
//
// ST-1 (idle, dedicated, AND a cluster stocker) is excluded on both counts.
func TestSense_FillsIdleHullsWithReuseEligibleSubset(t *testing.T) {
	db := newTestDB(t)
	playerID := seedWorld(t, db)
	// Two hulls the base world lacks isolate the remaining cases: DED-1 is
	// dedicated but in NO cluster (pure dedication exclusion), and FREE-1 is the
	// sole genuinely reuse-eligible hull.
	seedShip(t, db, playerID, "DED-1", hubWaypoint, "X1-TT77", nil, "idle", "contract")
	seedShip(t, db, playerID, "FREE-1", sourceWaypoint, "X1-TT77", nil, "idle", "")

	// A duty-cycle entry for FREE-1 proves the IdleHulls entry is the SAME struct
	// the Utilization pass built (a second, independent read would not carry it).
	sensor := capacityAdapters.NewSensor(db, fakeTreasury{credits: 1},
		capacityAdapters.WithSensorClock(&shared.MockClock{CurrentTime: t0}),
		capacityAdapters.WithDutyCycleReport(func() dutycycle.Report {
			return dutycycle.Report{Hulls: []dutycycle.HullDutyCycle{{Hull: "FREE-1", EarningPct: 42}}}
		}),
	)
	signals, err := sensor.Sense(context.Background(), playerID)

	require.NoError(t, err)
	require.Equal(t, []capacity.HullUtilization{
		{ShipSymbol: "FREE-1", DedicatedFleet: "", Waypoint: sourceWaypoint, DutyCyclePct: 42, Idle: true},
	}, signals.Topology.IdleHulls, "only the idle, undedicated, non-cluster hull is reuse-eligible")
	// Same tick, same snapshot: every IdleHulls entry is field-for-field one of
	// Utilization.Hulls (not a second DB read that could diverge).
	require.Subset(t, signals.Utilization.Hulls, signals.Topology.IdleHulls)
}
