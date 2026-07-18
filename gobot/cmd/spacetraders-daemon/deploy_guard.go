package main

import (
	"context"

	watchkeeper "github.com/andrescamacho/spacetraders-go/internal/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/buildinfo"
)

// playerLookup is the narrow slice of player.PlayerRepository the deploy-event
// guard needs. *persistence.GormPlayerRepository satisfies it.
type playerLookup interface {
	FindByID(ctx context.Context, playerID shared.PlayerID) (*player.Player, error)
}

// deployEventStore mirrors the (unexported) store interface
// watchkeeper.RecordDeployIfChanged requires, so the guard can forward the very
// same captain-event repository straight through to it. A value of this
// interface type also satisfies watchkeeper's identical method set.
type deployEventStore interface {
	Record(ctx context.Context, e *captain.Event) error
	LatestByType(ctx context.Context, playerID int, t captain.EventType) (*captain.Event, error)
}

// recordDeployIfPlayerExists guards RecordDeployIfChanged for the fresh-DB
// cold-boot path (sp-7pri). captain_events.player_id is an FK onto players.id,
// so on first boot against an empty database — before genesis registration
// commits a player row — recording deploy.completed violates
// fk_captain_events_player (SQLSTATE 23503). The event is best-effort and the
// violation was only logged "continuing", but it polluted the first-boot log.
//
// This resolves the target player first and emits only when it exists. The
// deploy signal is re-evaluated on every boot (RecordDeployIfChanged compares
// the running commit to the last recorded one), so deferring past a player-less
// boot loses nothing — the first boot after registration emits it.
//
// Normal path (a DB with the configured player) is byte-identical: FindByID
// returns the player and the call forwards to RecordDeployIfChanged exactly as
// before. Only the no-player branch changes. A FindByID error (fresh DB reports
// "player not found") is treated as "no player yet" and skips — safe for a
// self-healing, best-effort boot signal, and strictly quieter than the FK error
// it replaces.
func recordDeployIfPlayerExists(ctx context.Context, players playerLookup, store deployEventStore, playerID int, info buildinfo.Info, beadID func() string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return nil // playerID <= 0: no valid player can exist yet (genesis cold-boot).
	}
	if p, err := players.FindByID(ctx, pid); err != nil || p == nil {
		return nil // no player row yet (fresh DB); re-evaluated on the next boot.
	}
	return watchkeeper.RecordDeployIfChanged(ctx, store, playerID, info, beadID)
}
