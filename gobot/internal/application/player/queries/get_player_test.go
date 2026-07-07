package queries

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

type stubPlayerRepo struct {
	player *player.Player
}

func (s *stubPlayerRepo) FindByID(_ context.Context, _ shared.PlayerID) (*player.Player, error) {
	return s.player, nil
}

func (s *stubPlayerRepo) FindByAgentSymbol(_ context.Context, _ string) (*player.Player, error) {
	return s.player, nil
}

func (s *stubPlayerRepo) ListAll(_ context.Context) ([]*player.Player, error) { return nil, nil }

func (s *stubPlayerRepo) Add(_ context.Context, _ *player.Player) error { return nil }

// stubAPIClient satisfies the full domainPorts.APIClient via the embedded interface;
// only GetAgent is implemented (the sole method GetPlayerHandler exercises).
type stubAPIClient struct {
	domainPorts.APIClient
	agent *player.AgentData
	err   error
}

func (s *stubAPIClient) GetAgent(_ context.Context, _ string) (*player.AgentData, error) {
	return s.agent, s.err
}

// TestGetPlayerReturnsLiveCreditsWhenTokenPresent locks the credits contract that
// `player info` relies on: given a token in context, the handler fetches live
// credits from the API (the truthful source for a fresh agent, whose DB row and
// ledger carry no credits yet).
func TestGetPlayerReturnsLiveCreditsWhenTokenPresent(t *testing.T) {
	repo := &stubPlayerRepo{player: player.NewPlayer(shared.MustNewPlayerID(2), "ENDURANCE", "TOKEN-2")}
	apiClient := &stubAPIClient{agent: &player.AgentData{Credits: 175000}}
	handler := NewGetPlayerHandler(repo, apiClient)

	id := 2
	ctx := auth.WithPlayerToken(context.Background(), "TOKEN-2")

	resp, err := handler.Handle(ctx, &GetPlayerQuery{PlayerID: &id})

	require.NoError(t, err)
	result := resp.(*GetPlayerResponse)
	require.Equal(t, 175000, result.Player.Credits)
}

// TestGetPlayerFailsWhenTokenMissing characterizes the original sp-900v defect at the
// handler boundary: without a token in context (as when the CLI bypassed the injector),
// the handler cannot reach the agent API and returns "player token not found in context".
func TestGetPlayerFailsWhenTokenMissing(t *testing.T) {
	repo := &stubPlayerRepo{player: player.NewPlayer(shared.MustNewPlayerID(2), "ENDURANCE", "TOKEN-2")}
	apiClient := &stubAPIClient{agent: &player.AgentData{Credits: 1}}
	handler := NewGetPlayerHandler(repo, apiClient)

	id := 2

	_, err := handler.Handle(context.Background(), &GetPlayerQuery{PlayerID: &id})

	require.Error(t, err)
	require.Contains(t, err.Error(), "player token not found in context")
}
