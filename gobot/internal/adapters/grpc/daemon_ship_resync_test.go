package grpc

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// recordingSyncShipRepo records every player the resync path drives
// SyncAllFromAPI for, so a test can assert WHICH players are synced. It embeds
// the ShipRepository interface (nil) and overrides only SyncAllFromAPI — the
// only method syncAllShips calls — mirroring recoveryStubShipRepo.
type recordingSyncShipRepo struct {
	navigation.ShipRepository
	mu    sync.Mutex
	calls []int // playerID values passed to SyncAllFromAPI, in order
	count int
	err   error
}

func (r *recordingSyncShipRepo) SyncAllFromAPI(_ context.Context, playerID shared.PlayerID) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, playerID.Value())
	return r.count, r.err
}

func (r *recordingSyncShipRepo) syncedPlayers() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]int, len(r.calls))
	copy(out, r.calls)
	return out
}

// sp-ig6x: syncAllShips must re-sync ONLY the live/open-era player, never every
// player row. A universe reset leaves dead prior-era player rows behind (empty
// or reset-date-mismatched tokens); the old playerRepo.ListAll loop synced them
// too, and each dead row's 401 burned the ONE shared 60s deadline so the live
// player's ships never landed fresh — fleet-wide synced_at froze 12h+. The fix
// targets s.primaryPlayerID (the canonical open-era resolver every other
// boot-scoped path already uses).
func TestSyncAllShips_SyncsOnlyOpenEraPlayer(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	// Three rows sharing an agent symbol across universe resets (AgentSymbol is
	// intentionally non-unique). Only the third is the live/open-era player.
	dead1 := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&dead1).Error)
	dead2 := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "prior-universe", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&dead2).Error)
	live := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "live", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&live).Error)

	// The open era (ClosedAt nil) is owned by the live player, so primaryPlayerID
	// resolves it — not the first (dead) row.
	require.NoError(t, db.Create(&persistence.EraModel{
		Name: "torwind-open", AgentSymbol: "TORWIND", PlayerID: live.ID,
	}).Error)

	shipRepo := &recordingSyncShipRepo{count: 7}
	s := &DaemonServer{
		db:         db,
		shipRepo:   shipRepo,
		playerRepo: persistence.NewGormPlayerRepository(db),
	}

	require.NoError(t, s.syncAllShips(context.Background()),
		"a clean single-player resync must return nil")

	require.Equal(t, []int{live.ID}, shipRepo.syncedPlayers(),
		"syncAllShips must sync ONLY the open-era player %d, never the dead prior-universe rows %d/%d",
		live.ID, dead1.ID, dead2.ID)
}

// sp-ig6x: with no open-era player (genesis / fully-closed eras and no player
// rows), syncAllShips must skip cleanly and NOT sync anything — never fall back
// to iterating dead rows. primaryPlayerID returns 0 in that state.
func TestSyncAllShips_SkipsWhenNoPrimaryPlayer(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	shipRepo := &recordingSyncShipRepo{count: 3}
	s := &DaemonServer{
		db:         db,
		shipRepo:   shipRepo,
		playerRepo: persistence.NewGormPlayerRepository(db),
	}

	require.NoError(t, s.syncAllShips(context.Background()),
		"no open-era player must be a clean skip, not an error")
	require.Empty(t, shipRepo.syncedPlayers(),
		"with no primary player, syncAllShips must not sync any player")
}
