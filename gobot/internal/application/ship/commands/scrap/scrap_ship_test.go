package scrap

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// scrapFakeAPIClient stubs the four APIClient methods a scrap reaches: GetShip
// (the live cargo read), DockShip, ScrapShip, and nothing else.
type scrapFakeAPIClient struct {
	ports.APIClient

	shipData    *navigation.ShipData
	shipErr     error
	scrapResult *ports.ScrapResult
	scrapErr    error

	scrapCalls int
	dockCalls  int
}

func (f *scrapFakeAPIClient) GetShip(_ context.Context, _, _ string) (*navigation.ShipData, error) {
	if f.shipErr != nil {
		return nil, f.shipErr
	}
	return f.shipData, nil
}

func (f *scrapFakeAPIClient) DockShip(_ context.Context, _, _ string) error {
	f.dockCalls++
	return nil
}

func (f *scrapFakeAPIClient) ScrapShip(_ context.Context, _, _ string) (*ports.ScrapResult, error) {
	f.scrapCalls++
	if f.scrapErr != nil {
		return nil, f.scrapErr
	}
	return f.scrapResult, nil
}

type scrapFakePlayerRepo struct {
	player.PlayerRepository
	p *player.Player
}

func (f *scrapFakePlayerRepo) FindByID(_ context.Context, _ shared.PlayerID) (*player.Player, error) {
	return f.p, nil
}

type scrapFakeWaypointProvider struct{}

func (scrapFakeWaypointProvider) GetWaypoint(_ context.Context, _, _ string, _ int) (*shared.Waypoint, error) {
	return nil, errors.New("stub: waypoint lookup not needed by this test")
}

const scrapHull = "TORWIND-446"

// newScrapHarness wires the REAL ship repository, container repository and ledger
// recorder over a real sqlite DB, so claim, delete and recording run end to end.
func newScrapHarness(t *testing.T, fake *scrapFakeAPIClient) (*ScrapShipHandler, *gorm.DB, int) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	playerID := shared.MustNewPlayerID(playerRow.ID)

	playerRepo := &scrapFakePlayerRepo{p: &player.Player{ID: playerID, AgentSymbol: "TORWIND", Token: "tok"}}
	shipRepo := api.NewShipRepository(fake, playerRepo, nil, scrapFakeWaypointProvider{}, db, nil)

	med := mediator.NewMediator()
	require.NoError(t, mediator.RegisterHandler[*ledgerCommands.RecordTransactionCommand](
		med, ledgerCommands.NewRecordTransactionHandler(persistence.NewGormTransactionRepository(db), nil)))

	handler := NewScrapShipHandler(shipRepo, playerRepo, fake, persistence.NewContainerRepository(db), med, nil)
	return handler, db, playerRow.ID
}

// liveShip is what the API reports for the hull; cargoUnits is the load the guard reads.
func liveShip(cargoUnits int) *navigation.ShipData {
	return &navigation.ShipData{
		Symbol: scrapHull, Location: "X1-JP61-A1", NavStatus: "DOCKED",
		CargoCapacity: 240, CargoUnits: cargoUnits, EngineSpeed: 10,
		FrameSymbol: "FRAME_HEAVY_FREIGHTER", Role: "HAULER",
		Cargo: &navigation.CargoData{Capacity: 240, Units: cargoUnits, Inventory: cargoInventory(cargoUnits)},
	}
}

func cargoInventory(units int) []shared.CargoItem {
	if units == 0 {
		return nil
	}
	return []shared.CargoItem{{Symbol: "IRON_ORE", Name: "Iron Ore", Description: "ore", Units: units}}
}

func seedShipRow(t *testing.T, db *gorm.DB, pid, cargoUnits int) {
	t.Helper()
	inventory := "[]"
	if cargoUnits > 0 {
		inventory = `[{"symbol":"IRON_ORE","name":"Iron Ore","description":"ore","units":` + itoa(cargoUnits) + `}]`
	}
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: scrapHull, PlayerID: pid,
		NavStatus: "DOCKED", LocationSymbol: "X1-JP61-A1", SystemSymbol: "X1-JP61", EngineSpeed: 10,
		CargoCapacity: 240, CargoUnits: cargoUnits, CargoInventory: inventory,
		Modules: "[]", AssignmentStatus: "idle",
	}).Error)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func shipRowCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&persistence.ShipModel{}).Where("ship_symbol = ?", scrapHull).Count(&n).Error)
	return n
}

func ledgerRowCount(t *testing.T, db *gorm.DB, pid int) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&persistence.TransactionModel{}).Where("player_id = ?", pid).Count(&n).Error)
	return n
}

func latestLedgerRow(t *testing.T, db *gorm.DB, pid int) *ledger.Transaction {
	t.Helper()
	playerID, err := shared.NewPlayerID(pid)
	require.NoError(t, err)
	txs, err := persistence.NewGormTransactionRepository(db).FindByPlayer(context.Background(), playerID, ledger.QueryOptions{
		Limit: 1, OrderBy: "timestamp DESC, created_at DESC, id DESC",
	})
	require.NoError(t, err)
	require.Len(t, txs, 1, "ledger is empty: the scrap proceeds were never recorded")
	return txs[0]
}

func scrap(t *testing.T, handler *ScrapShipHandler, pid int) (*ScrapShipResponse, error) {
	t.Helper()
	playerID := pid
	result, err := handler.Handle(context.Background(), &ScrapShipCommand{ShipSymbol: scrapHull, PlayerID: &playerID})
	if err != nil {
		return nil, err
	}
	resp, ok := result.(*ScrapShipResponse)
	require.True(t, ok, "expected *ScrapShipResponse")
	return resp, nil
}

// Acceptance: an empty hull is sold, the credits land in the ledger re-anchored to
// the API's in-band balance, and the ships row is gone so `fleet list` stays honest.
func TestScrapShipBanksTheProceedsAndRemovesTheHullFromTheFleet(t *testing.T) {
	const proceeds = 733517
	postCredits := 313_000_000 + proceeds

	fake := &scrapFakeAPIClient{
		shipData: liveShip(0),
		scrapResult: &ports.ScrapResult{
			ShipSymbol: scrapHull, WaypointSymbol: "X1-JP61-A1",
			TotalPrice: proceeds, AgentCredits: &postCredits,
		},
	}
	handler, db, pid := newScrapHarness(t, fake)
	seedShipRow(t, db, pid, 0)

	resp, err := scrap(t, handler, pid)
	require.NoError(t, err)

	require.True(t, resp.Success)
	require.Equal(t, proceeds, resp.TotalPrice)
	require.Equal(t, 1, fake.scrapCalls, "the scrap endpoint must be called exactly once")
	require.GreaterOrEqual(t, fake.dockCalls, 1, "scrapping requires a docked hull")

	row := latestLedgerRow(t, db, pid)
	require.Equal(t, string(ledger.TransactionTypeScrapShip), string(row.TransactionType()))
	require.Equal(t, proceeds, row.Amount(), "scrap proceeds are income, not an expense")
	require.Equal(t, postCredits, row.BalanceAfter(),
		"balance_after must re-anchor to the agent's live post-scrap credits")

	require.Zero(t, shipRowCount(t, db), "a scrapped hull must not survive in the ships table")
}

// The money guard (RULINGS #4). Cargo is destroyed, not compensated, so a laden
// hull is refused before the irreversible API call.
func TestScrapShipRefusesAHullThatStillHoldsCargo(t *testing.T) {
	fake := &scrapFakeAPIClient{shipData: liveShip(225)}
	handler, db, pid := newScrapHarness(t, fake)
	seedShipRow(t, db, pid, 225)

	_, err := scrap(t, handler, pid)

	require.Error(t, err)
	require.Contains(t, err.Error(), "225", "the refusal must name the load it is protecting")
	require.Zero(t, fake.scrapCalls, "the irreversible scrap must never be reached with cargo aboard")
	require.Equal(t, int64(1), shipRowCount(t, db), "a refused scrap leaves the hull in the fleet")
	require.Zero(t, ledgerRowCount(t, db, pid), "a refused scrap moves no credits")
}

// Fail closed: an unreadable live cargo state is a refusal, never a fallback to the
// possibly-stale persisted row.
func TestScrapShipRefusesWhenTheLiveCargoStateCannotBeRead(t *testing.T) {
	fake := &scrapFakeAPIClient{shipErr: errors.New("api unreachable")}
	handler, db, pid := newScrapHarness(t, fake)
	seedShipRow(t, db, pid, 0)

	_, err := scrap(t, handler, pid)

	require.Error(t, err)
	require.Zero(t, fake.scrapCalls, "an unreadable hold must fail closed, not scrap on a stale zero")
	require.Equal(t, int64(1), shipRowCount(t, db))
	require.Zero(t, ledgerRowCount(t, db, pid))
}

// 4263 is surfaced as a sentence naming the condition, not as the raw API envelope.
func TestScrapShipSurfacesTheNotAtAShipyardRefusalCleanly(t *testing.T) {
	fake := &scrapFakeAPIClient{
		shipData: liveShip(0),
		scrapErr: &ports.APIError{
			StatusCode: 400,
			Body:       `{"error":{"message":"Ship is not docked at a waypoint with a shipyard.","code":4263}}`,
		},
	}
	handler, db, pid := newScrapHarness(t, fake)
	seedShipRow(t, db, pid, 0)

	_, err := scrap(t, handler, pid)

	require.Error(t, err)
	require.Contains(t, err.Error(), "SHIPYARD", "the refusal must name the trait the waypoint lacks")
	require.NotContains(t, err.Error(), `{"error"`, "the raw API envelope must not leak to the operator")
	require.Equal(t, int64(1), shipRowCount(t, db), "a refused scrap leaves the hull in the fleet")
	require.Zero(t, ledgerRowCount(t, db, pid))
}
