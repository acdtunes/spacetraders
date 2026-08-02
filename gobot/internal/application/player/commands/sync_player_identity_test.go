package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// This handler is the ONLY writer of players.metadata.headquarters, and until now
// nothing dispatched it — the daemon boot hook does. These pin what that boot sync must do:
// seed the key, preserve the rest of the row, and stay quiet when there is nothing to change.

// ---- fakes -----------------------------------------------------------------

type fakeAgentReader struct {
	agent *player.AgentData
	err   error
	calls int
}

// GetAgent is ADVERSARIAL on the error path: it returns a fully-populated agent ALONGSIDE its
// error, so a handler that ignored the error and used the value would silently pass a test that
// is supposed to prove it fails closed.
func (f *fakeAgentReader) GetAgent(_ context.Context, _ string) (*player.AgentData, error) {
	f.calls++
	if f.err != nil {
		return &player.AgentData{Headquarters: "X1-WRONG-Z9", StartingFaction: "PHANTOM"}, f.err
	}
	return f.agent, nil
}

type fakePlayerRepo struct {
	stored   *player.Player
	findErr  error
	addErr   error
	addCalls int
	lastAdd  *player.Player
}

func (f *fakePlayerRepo) FindByID(_ context.Context, _ shared.PlayerID) (*player.Player, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	return f.stored, nil
}
func (f *fakePlayerRepo) FindByAgentSymbol(_ context.Context, _ string) (*player.Player, error) {
	return f.stored, nil
}
func (f *fakePlayerRepo) ListAll(_ context.Context) ([]*player.Player, error) {
	return []*player.Player{f.stored}, nil
}
func (f *fakePlayerRepo) Add(_ context.Context, p *player.Player) error {
	f.addCalls++
	f.lastAdd = p
	return f.addErr
}

// ctxWithToken mirrors what PlayerTokenMiddleware injects when the command is dispatched through
// the mediator, which is how the daemon boot hook reaches this handler.
func ctxWithToken(t *testing.T) context.Context {
	t.Helper()
	return auth.WithPlayerToken(context.Background(), "test-token")
}

// ---- tests -----------------------------------------------------------------

// The core of the fix: a live row carrying only starting_faction gains headquarters, and keeps
// everything it already had. This is the exact shape every players row was in.
func TestSyncPlayer_SeedsHeadquartersOntoALiveRow_PreservingOtherKeys(t *testing.T) {
	repo := &fakePlayerRepo{stored: &player.Player{
		ID:          shared.MustNewPlayerID(5),
		AgentSymbol: "TORWIND",
		Token:       "test-token",
		Metadata:    map[string]interface{}{"starting_faction": "COSMIC"},
	}}
	api := &fakeAgentReader{agent: &player.AgentData{
		AccountID: "acct-9", Symbol: "TORWIND", Headquarters: "X1-KP23-A1",
		Credits: 95_918, StartingFaction: "COSMIC",
	}}
	h := &SyncPlayerHandler{playerRepo: repo, apiClient: api}

	resp, err := h.Handle(ctxWithToken(t), &SyncPlayerCommand{PlayerID: 5})
	require.NoError(t, err)

	out := resp.(*SyncPlayerResponse)
	require.True(t, out.Updated)
	require.Equal(t, 1, repo.addCalls, "the seeded row is persisted exactly once")
	require.Equal(t, "X1-KP23-A1", repo.lastAdd.Metadata["headquarters"])
	require.Equal(t, "COSMIC", repo.lastAdd.Metadata["starting_faction"], "faction must survive the merge")
	require.Equal(t, "acct-9", repo.lastAdd.Metadata["account_id"])
}

// Requirement 3: idempotent. The boot hook runs on EVERY daemon start, so a row that is already
// correct must produce NO write at all — not a write that happens to be equal.
func TestSyncPlayer_WritesNothingWhenIdentityIsAlreadyCorrect(t *testing.T) {
	repo := &fakePlayerRepo{stored: &player.Player{
		ID:          shared.MustNewPlayerID(5),
		AgentSymbol: "TORWIND",
		Credits:     95_918,
		Metadata: map[string]interface{}{
			"starting_faction": "COSMIC",
			"headquarters":     "X1-KP23-A1",
			"account_id":       "acct-9",
		},
	}}
	api := &fakeAgentReader{agent: &player.AgentData{
		AccountID: "acct-9", Headquarters: "X1-KP23-A1",
		Credits: 95_918, StartingFaction: "COSMIC",
	}}
	h := &SyncPlayerHandler{playerRepo: repo, apiClient: api}

	resp, err := h.Handle(ctxWithToken(t), &SyncPlayerCommand{PlayerID: 5})
	require.NoError(t, err)

	require.False(t, resp.(*SyncPlayerResponse).Updated, "an unchanged row reports no update")
	require.Zero(t, repo.addCalls, "an unchanged row must not be rewritten on every boot")
}

// Fail closed on an unreadable agent: the row is left exactly as it was. The fake hands back a
// WRONG headquarters with its error, so a swallowed error would corrupt the row and fail here.
func TestSyncPlayer_UnreadableAgentLeavesTheRowUntouched(t *testing.T) {
	repo := &fakePlayerRepo{stored: &player.Player{
		ID:       shared.MustNewPlayerID(5),
		Metadata: map[string]interface{}{"starting_faction": "COSMIC", "headquarters": "X1-KP23-A1"},
	}}
	api := &fakeAgentReader{err: errors.New("api unavailable")}
	h := &SyncPlayerHandler{playerRepo: repo, apiClient: api}

	_, err := h.Handle(ctxWithToken(t), &SyncPlayerCommand{PlayerID: 5})

	require.Error(t, err, "an unreadable agent must surface, not be swallowed")
	require.Zero(t, repo.addCalls, "nothing is written when the agent could not be read")
	require.Equal(t, "X1-KP23-A1", repo.stored.Metadata["headquarters"],
		"the stored headquarters must not be replaced by the failed read's value")
}

// Credits still sync independently of identity — a row whose identity is already correct but
// whose credits moved must still be persisted.
func TestSyncPlayer_PersistsACreditsChangeEvenWhenIdentityIsUnchanged(t *testing.T) {
	repo := &fakePlayerRepo{stored: &player.Player{
		ID:      shared.MustNewPlayerID(5),
		Credits: 1,
		Metadata: map[string]interface{}{
			"starting_faction": "COSMIC", "headquarters": "X1-KP23-A1", "account_id": "acct-9",
		},
	}}
	api := &fakeAgentReader{agent: &player.AgentData{
		AccountID: "acct-9", Headquarters: "X1-KP23-A1", Credits: 95_918, StartingFaction: "COSMIC",
	}}
	h := &SyncPlayerHandler{playerRepo: repo, apiClient: api}

	resp, err := h.Handle(ctxWithToken(t), &SyncPlayerCommand{PlayerID: 5})
	require.NoError(t, err)

	require.True(t, resp.(*SyncPlayerResponse).Updated)
	require.Equal(t, 1, repo.addCalls)
	require.Equal(t, 95_918, repo.lastAdd.Credits)
}
