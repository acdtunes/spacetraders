package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/buildinfo"
)

// recordDeployIfPlayerExists must resolve the target player first and skip the
// deploy.completed emit entirely when it is absent: captain_events.player_id is
// an FK onto players.id, so emitting before a player row exists (fresh-DB first
// boot) would violate fk_captain_events_player.

type fakePlayerLookup struct {
	player *player.Player
	err    error
}

func (f *fakePlayerLookup) FindByID(ctx context.Context, playerID shared.PlayerID) (*player.Player, error) {
	return f.player, f.err
}

// recordingDeployStore satisfies the store the guard forwards to
// watchkeeper.RecordDeployIfChanged and records whether an insert was attempted.
type recordingDeployStore struct {
	recordCalled bool
	latest       *captain.Event
}

func (s *recordingDeployStore) Record(ctx context.Context, e *captain.Event) error {
	s.recordCalled = true
	return nil
}

func (s *recordingDeployStore) LatestByType(ctx context.Context, playerID int, t captain.EventType) (*captain.Event, error) {
	return s.latest, nil
}

func TestRecordDeployIfPlayerExists_SkipsWhenNoPlayerRow(t *testing.T) {
	players := &fakePlayerLookup{err: errors.New("player not found: 1")} // fresh DB
	store := &recordingDeployStore{}

	err := recordDeployIfPlayerExists(context.Background(), players, store, 1, buildinfo.Info{Commit: "abc"}, nil)

	require.NoError(t, err)
	require.False(t, store.recordCalled,
		"must not attempt the FK-violating deploy.completed insert when no player row exists")
}

func TestRecordDeployIfPlayerExists_SkipsWhenPlayerIDNonPositive(t *testing.T) {
	players := &fakePlayerLookup{err: errors.New("FindByID must not be consulted for a non-positive id")}
	store := &recordingDeployStore{}

	err := recordDeployIfPlayerExists(context.Background(), players, store, 0, buildinfo.Info{Commit: "abc"}, nil)

	require.NoError(t, err)
	require.False(t, store.recordCalled)
}

func TestRecordDeployIfPlayerExists_EmitsWhenPlayerPresent(t *testing.T) {
	// Normal path: the configured player exists, so the guard forwards to
	// RecordDeployIfChanged, which (no prior deploy.completed event on record ->
	// treated as changed) records exactly once. Proves the guard is a no-op for
	// a DB that has the player — the fresh-DB branch is the only behavior change.
	players := &fakePlayerLookup{player: &player.Player{AgentSymbol: "ORION"}}
	store := &recordingDeployStore{} // latest nil -> first boot ever -> emit

	err := recordDeployIfPlayerExists(context.Background(), players, store, 1, buildinfo.Info{Commit: "abc"}, nil)

	require.NoError(t, err)
	require.True(t, store.recordCalled,
		"with the player present the deploy signal must still be recorded (normal path unchanged)")
}
