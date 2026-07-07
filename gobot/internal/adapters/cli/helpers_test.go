package cli

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// fakePlayerRepo is an in-memory player.PlayerRepository for resolution tests.
type fakePlayerRepo struct {
	byID     map[int]*player.Player
	bySymbol map[string]*player.Player
}

func (f *fakePlayerRepo) FindByID(_ context.Context, id shared.PlayerID) (*player.Player, error) {
	if p, ok := f.byID[id.Value()]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("player %d not found", id.Value())
}

func (f *fakePlayerRepo) FindByAgentSymbol(_ context.Context, symbol string) (*player.Player, error) {
	if p, ok := f.bySymbol[symbol]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("agent %s not found", symbol)
}

func (f *fakePlayerRepo) ListAll(_ context.Context) ([]*player.Player, error) { return nil, nil }

func (f *fakePlayerRepo) Add(_ context.Context, _ *player.Player) error { return nil }

// setPlayerFlags sets the package-level CLI flag vars for a test and restores
// them afterward, so resolution tests don't leak state into each other.
func setPlayerFlags(t *testing.T, id int, agent string) {
	t.Helper()
	prevID, prevAgent := playerID, agentSymbol
	playerID = id
	agentSymbol = agent
	t.Cleanup(func() {
		playerID = prevID
		agentSymbol = prevAgent
	})
}

func TestResolveDefaultPlayerUsesPlayerIDFlag(t *testing.T) {
	setPlayerFlags(t, 2, "")
	repo := &fakePlayerRepo{byID: map[int]*player.Player{
		2: player.NewPlayer(shared.MustNewPlayerID(2), "ENDURANCE", "TOKEN-2"),
	}}

	p, err := resolveDefaultPlayer(context.Background(), repo)

	require.NoError(t, err)
	require.Equal(t, 2, p.ID.Value())
	require.Equal(t, "TOKEN-2", p.Token, "resolved player must carry its token for context injection")
}

func TestResolveDefaultPlayerUsesAgentFlag(t *testing.T) {
	setPlayerFlags(t, 0, "ENDURANCE")
	repo := &fakePlayerRepo{bySymbol: map[string]*player.Player{
		"ENDURANCE": player.NewPlayer(shared.MustNewPlayerID(5), "ENDURANCE", "TOKEN-5"),
	}}

	p, err := resolveDefaultPlayer(context.Background(), repo)

	require.NoError(t, err)
	require.Equal(t, 5, p.ID.Value())
	require.Equal(t, "TOKEN-5", p.Token)
}

// TestResolveDefaultPlayerFallsBackToPersistedDefault reproduces the ledger/contract
// bug: with no flags but a default persisted via `config set-player --player-id`,
// resolution must succeed instead of failing with "--player-id flag is required".
func TestResolveDefaultPlayerFallsBackToPersistedDefault(t *testing.T) {
	setPlayerFlags(t, 0, "")
	t.Setenv("HOME", t.TempDir())

	handler, err := config.NewUserConfigHandler()
	require.NoError(t, err)
	require.NoError(t, handler.SetDefaultPlayer(2))

	repo := &fakePlayerRepo{byID: map[int]*player.Player{
		2: player.NewPlayer(shared.MustNewPlayerID(2), "ENDURANCE", "TOKEN-2"),
	}}

	p, err := resolveDefaultPlayer(context.Background(), repo)

	require.NoError(t, err)
	require.Equal(t, 2, p.ID.Value())
}

// TestResolveDefaultPlayerFallsBackToPersistedAgent covers `config set-player --agent`
// where only the agent symbol is persisted; it must be resolved to an entity via repo.
func TestResolveDefaultPlayerFallsBackToPersistedAgent(t *testing.T) {
	setPlayerFlags(t, 0, "")
	t.Setenv("HOME", t.TempDir())

	handler, err := config.NewUserConfigHandler()
	require.NoError(t, err)
	require.NoError(t, handler.SetDefaultAgent("ENDURANCE"))

	repo := &fakePlayerRepo{bySymbol: map[string]*player.Player{
		"ENDURANCE": player.NewPlayer(shared.MustNewPlayerID(5), "ENDURANCE", "TOKEN-5"),
	}}

	p, err := resolveDefaultPlayer(context.Background(), repo)

	require.NoError(t, err)
	require.Equal(t, 5, p.ID.Value())
	require.Equal(t, "TOKEN-5", p.Token)
}

// TestResolveDefaultPlayerErrorsWhenNoPlayer confirms the helpful error remains when
// neither flags nor a persisted default identify a player.
func TestResolveDefaultPlayerErrorsWhenNoPlayer(t *testing.T) {
	setPlayerFlags(t, 0, "")
	t.Setenv("HOME", t.TempDir())

	repo := &fakePlayerRepo{}

	_, err := resolveDefaultPlayer(context.Background(), repo)

	require.Error(t, err)
}
