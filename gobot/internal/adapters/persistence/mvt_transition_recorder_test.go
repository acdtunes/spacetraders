package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// reasonYieldBelow mirrors mvt.ReasonYieldBelow, which the departure rule (a sibling task)
// owns. Spelled literally here so this table's round-trip test does not depend on that file.
const reasonYieldBelow = "yield_below_alternative"

func TestMVTTransitionRecorder_RoundTrip(t *testing.T) {
	var _ mvt.TransitionRecorder = (*persistence.MVTTransitionRecorderGORM)(nil)
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "MVT-T", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	rec := persistence.NewMVTTransitionRecorder(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	want := mvt.Transition{PlayerID: player.ID, Hull: "H1", From: mvt.StateTrade, To: mvt.StateClaim, System: "X1-A",
		YieldHere: 12.5, BestAlternative: 40, TravelCost: 3.25, Reason: reasonYieldBelow, At: now}
	require.NoError(t, rec.Record(ctx, want))
	require.NoError(t, rec.Record(ctx, mvt.Transition{PlayerID: player.ID, Hull: "H1", From: mvt.StateClaim, To: mvt.StateTravel, System: "X1-B", Reason: "claim", At: now.Add(time.Second)}))
	got, err := rec.ListSince(ctx, player.ID, now.Add(-time.Minute))
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, want, got[0])
	none, err := rec.ListSince(ctx, player.ID+1, now.Add(-time.Minute))
	require.NoError(t, err)
	require.Empty(t, none)
}
