package navigation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// jumpGateWaypointProvider returns every requested waypoint as a JUMP_GATE so a
// DB-seeded ship reconstructs with a jump-gate location (the denormalized
// fallback leaves Type empty, which is not a gate).
type jumpGateWaypointProvider struct{}

func (jumpGateWaypointProvider) GetWaypoint(_ context.Context, symbol, systemSymbol string, _ int) (*shared.Waypoint, error) {
	return &shared.Waypoint{Symbol: symbol, SystemSymbol: systemSymbol, Type: "JUMP_GATE"}, nil
}

// jumpReapplyAPIClient drives GetJumpGate + JumpShip and, on the first JumpShip
// call, fires onJump to model a concurrent writer committing a fresh cargo update
// on the same hull during the jump's API window.
type jumpReapplyAPIClient struct {
	ports.APIClient
	gate      *ports.JumpGateData
	result    *ports.JumpResult
	onJump    func()
	jumpCalls int
}

func (c *jumpReapplyAPIClient) GetJumpGate(_ context.Context, _, _, _ string) (*ports.JumpGateData, error) {
	return c.gate, nil
}

func (c *jumpReapplyAPIClient) JumpShip(_ context.Context, _, _, _ string) (*ports.JumpResult, error) {
	c.jumpCalls++
	if c.jumpCalls == 1 && c.onJump != nil {
		c.onJump()
	}
	return c.result, nil
}

// This proves the sp-wa7c fix for jump_ship's post-jump nav write against a real,
// version-guarded repository: a concurrent writer commits a fresh cargo update on
// the same hull during the jump API call. The migrated SaveWithRetry persist
// re-applies ONLY the destination location + jump cooldown on the fresh row, so
// the concurrent cargo update survives instead of being last-write-wins clobbered
// by the handler's pre-jump snapshot.
func TestJumpShip_PostJumpNavWriteDoesNotClobberFreshCargo(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	pid := shared.MustNewPlayerID(playerRow.ID)

	// Driveless hull IN_ORBIT on a complete source gate, cargo 100, v1.
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "JUMPER-1",
		PlayerID:         playerRow.ID,
		AssignmentStatus: "idle",
		NavStatus:        "IN_ORBIT",
		LocationSymbol:   "X1-SRC-GATE",
		SystemSymbol:     "X1-SRC",
		FuelCurrent:      500,
		FuelCapacity:     1000,
		CargoCapacity:    200,
		CargoUnits:       100,
		CargoInventory:   `[{"symbol":"IRON_ORE","name":"Iron Ore","description":"x","units":100}]`,
		Modules:          "[]",
		EngineSpeed:      10,
		Version:          1,
	}).Error)

	shipRepo := api.NewShipRepository(nil, nil, nil, jumpGateWaypointProvider{}, db, nil)

	concurrentCargoWriter := func() {
		other, ferr := shipRepo.FindBySymbol(context.Background(), "JUMPER-1", pid)
		require.NoError(t, ferr)
		require.NoError(t, other.RemoveCargo("IRON_ORE", 50))
		require.NoError(t, shipRepo.Save(context.Background(), other))
	}

	apiClient := &jumpReapplyAPIClient{
		gate:   &ports.JumpGateData{Symbol: "X1-SRC-GATE", Connections: []string{"X1-DST-GATE"}},
		result: &ports.JumpResult{DestinationSystem: "X1-DST", DestinationWaypoint: "X1-DST-GATE", CooldownSeconds: 60},
		onJump: concurrentCargoWriter,
	}
	playerRepo := &stubJumpPlayerRepo{playerEntity: player.NewPlayer(pid, "TORWIND", "tok")}

	// nil constructionRepo -> source-gate completeness check fails open; nil
	// mediator/containerRepo are unused on this driveless-at-gate + SkipClaim path.
	handler := NewJumpShipHandler(shipRepo, playerRepo, apiClient, nil, nil, nil, shared.NewRealClock())

	pidInt := pid.Value()
	resp, err := handler.Handle(context.Background(), &JumpShipCommand{
		ShipSymbol:        "JUMPER-1",
		DestinationSystem: "X1-DST",
		PlayerID:          &pidInt,
		SkipClaim:         true,
	})
	require.NoError(t, err)
	jumpResp, ok := resp.(*JumpShipResponse)
	require.True(t, ok)
	require.True(t, jumpResp.Success)
	require.Equal(t, 1, apiClient.jumpCalls)

	var row persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "JUMPER-1").First(&row).Error)

	// The concurrent cargo write SURVIVED the post-jump nav persist (no clobber).
	require.Equal(t, 50, row.CargoUnits, "concurrent cargo unload must survive the post-jump nav persist")
	// The nav state was still applied on the fresh row.
	require.Equal(t, "X1-DST-GATE", row.LocationSymbol, "destination location re-applied on fresh state")
	require.NotNil(t, row.CooldownExpiration, "jump cooldown persisted")
}
