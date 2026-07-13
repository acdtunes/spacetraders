package cargo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/strategies"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// This is the regression test for the reported sp-wa7c cargo desync, exercised at
// the CargoTransactionHandler boundary against a REAL, version-guarded
// ShipRepository (sqlite) rather than a fake. A concurrent writer commits a fresh
// FUEL update on the same hull while this handler is mid-transaction (during the
// strategy's API call window). Under the migrated SaveWithRetry persist the
// handler re-loads the FRESH row and re-applies ONLY its own cargo delta, so the
// concurrent writer's fuel survives AND the sold units are deducted. Under the old
// last-write-wins Save the handler's stale pre-loaded snapshot (fuel 100) would
// have clobbered the concurrent fuel (500) back down — the exact desync.

// reapplyStubWaypoints forces modelToDomain's denormalized-coordinate fallback so
// the test needs no waypoint rows (mirrors the api-package stubWaypoints).
type reapplyStubWaypoints struct{}

func (reapplyStubWaypoints) GetWaypoint(_ context.Context, _, _ string, _ int) (*shared.Waypoint, error) {
	return nil, errors.New("stub: use denormalized fallback")
}

// reapplyFakeStrategy stands in for the sell/purchase strategy so the test controls
// both the transacted units and the injection of a concurrent writer. onFirst fires
// exactly once, before the first tranche's result is returned, to model a colliding
// writer committing between the handler's ship load and its persist.
type reapplyFakeStrategy struct {
	txType  string
	result  *strategies.TransactionResult
	onFirst func()
	calls   int
}

func (s *reapplyFakeStrategy) Execute(_ context.Context, _, _ string, _ int, _ string) (*strategies.TransactionResult, error) {
	s.calls++
	if s.calls == 1 && s.onFirst != nil {
		s.onFirst()
	}
	return s.result, nil
}

func (s *reapplyFakeStrategy) ValidatePreconditions(_ *navigation.Ship, _ string, _ int) error {
	return nil
}

func (s *reapplyFakeStrategy) GetTransactionType() string { return s.txType }

func TestCargoTransaction_ConcurrentFuelWriteSurvivesCargoSell_NoClobber(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	pid := shared.MustNewPlayerID(playerRow.ID)

	// Seed a docked hull at its market with 100 IRON_ORE and 100 fuel, at row
	// version 1 so the version-guarded path engages (a v0 row would take the
	// unconditional insert branch and never guard against a concurrent writer).
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "CLOBBER-1",
		PlayerID:         playerRow.ID,
		AssignmentStatus: "idle",
		NavStatus:        "DOCKED",
		LocationSymbol:   testBuyWaypoint,
		SystemSymbol:     "X1-TEST",
		FuelCurrent:      100,
		FuelCapacity:     1000,
		CargoCapacity:    200,
		CargoUnits:       100,
		CargoInventory:   `[{"symbol":"IRON_ORE","name":"Iron Ore","description":"x","units":100}]`,
		EngineSpeed:      10,
		Version:          1,
	}).Error)

	shipRepo := api.NewShipRepository(nil, nil, nil, reapplyStubWaypoints{}, db, nil)

	// The colliding writer: load the hull fresh, refuel +400, persist. This bumps
	// the row's version and fuel behind the handler's back — the fresh cargo field
	// (still 100) is untouched, so a correct cargo persist must preserve fuel=500.
	concurrentFuelWriter := func() {
		other, err := shipRepo.FindBySymbol(context.Background(), "CLOBBER-1", pid)
		require.NoError(t, err)
		require.NoError(t, other.Refuel(400))
		require.NoError(t, shipRepo.Save(context.Background(), other))
	}

	strategy := &reapplyFakeStrategy{
		txType:  "sell",
		result:  &strategies.TransactionResult{TotalAmount: 300, UnitsProcessed: 30},
		onFirst: concurrentFuelWriter,
	}

	playerRepo := &buyFakePlayerRepo{player: player.NewPlayer(pid, "TORWIND", "tok")}
	handler := NewCargoTransactionHandler(
		strategy, shipRepo, playerRepo, &buyFakeMarketRepo{}, nil, &buyRecordingMediator{}, nil,
	)

	ctx := auth.WithPlayerToken(context.Background(), "tok")
	_, err = handler.Handle(ctx, &CargoTransactionCommand{
		ShipSymbol: "CLOBBER-1",
		GoodSymbol: "IRON_ORE",
		Units:      30,
		PlayerID:   pid,
	})
	require.NoError(t, err)
	require.Equal(t, 1, strategy.calls, "single tranche (nil market → one transaction)")

	var row persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "CLOBBER-1").First(&row).Error)

	// The concurrent writer's fuel SURVIVED — the fix. The old last-write-wins Save
	// would have written the handler's stale snapshot (fuel 100) here.
	require.Equal(t, 500, row.FuelCurrent, "concurrent fuel write must survive the cargo persist (no clobber)")
	// This op's own cargo delta was applied on top of the fresh row: 100 - 30 = 70.
	require.Equal(t, 70, row.CargoUnits, "sold 30 of 100 units, re-applied on fresh state")
}

// A transaction that processes zero units (e.g. a floor/ceiling abort before any
// tranche) must not persist, so it can never clobber a concurrent writer and never
// bumps the version spuriously.
func TestCargoTransaction_ZeroUnits_SkipsPersist(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	pid := shared.MustNewPlayerID(playerRow.ID)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "NOOP-1",
		PlayerID:         playerRow.ID,
		AssignmentStatus: "idle",
		NavStatus:        "DOCKED",
		LocationSymbol:   testBuyWaypoint,
		SystemSymbol:     "X1-TEST",
		FuelCurrent:      100,
		FuelCapacity:     1000,
		CargoCapacity:    200,
		CargoUnits:       100,
		CargoInventory:   `[{"symbol":"IRON_ORE","name":"Iron Ore","description":"x","units":100}]`,
		EngineSpeed:      10,
		Version:          1,
	}).Error)

	shipRepo := api.NewShipRepository(nil, nil, nil, reapplyStubWaypoints{}, db, nil)

	// A sell strategy that processes nothing (0 units) — models the floor-abort path.
	strategy := &reapplyFakeStrategy{
		txType: "sell",
		result: &strategies.TransactionResult{TotalAmount: 0, UnitsProcessed: 0},
	}

	playerRepo := &buyFakePlayerRepo{player: player.NewPlayer(pid, "TORWIND", "tok")}
	handler := NewCargoTransactionHandler(
		strategy, shipRepo, playerRepo, &buyFakeMarketRepo{}, nil, &buyRecordingMediator{}, nil,
	)

	ctx := auth.WithPlayerToken(context.Background(), "tok")
	_, err = handler.Handle(ctx, &CargoTransactionCommand{
		ShipSymbol: "NOOP-1",
		GoodSymbol: "IRON_ORE",
		Units:      30,
		PlayerID:   pid,
	})
	require.NoError(t, err)

	var row persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "NOOP-1").First(&row).Error)
	require.Equal(t, 1, row.Version, "a zero-unit transaction must not bump the row version")
	require.Equal(t, 100, row.CargoUnits, "cargo untouched")
}
