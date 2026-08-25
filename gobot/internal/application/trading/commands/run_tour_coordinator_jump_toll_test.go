package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// --- sp-3x143: the solve request carries the fleet's MEASURED per-gate-hop toll ---

type fakeJumpTollReader struct {
	seconds int
	players []int
}

func (r *fakeJumpTollReader) PerHopTollSeconds(_ context.Context, playerID int) int {
	r.players = append(r.players, playerID)
	return r.seconds
}

// A wired estimator with enough samples puts its measured seconds on the solve request, and
// scopes the read to the planning player.
func TestTourPerHopToll_ForwardsTheMeasuredSeconds(t *testing.T) {
	reader := &fakeJumpTollReader{seconds: 1180}
	h := &RunTourCoordinatorHandler{jumpTolls: reader}

	got := h.tourPerHopToll(context.Background(), &RunTourCoordinatorCommand{PlayerID: 5})

	require.Equal(t, 1180, got)
	require.Equal(t, []int{5}, reader.players)
}

// THE FAIL-OPEN PIN, both directions. An unwired reader and a reader with too few samples
// must both leave 0 on the request, which serializes to nothing and leaves the solver on its
// fitted default — byte-identical to a binary that predates the estimator.
func TestTourPerHopToll_IsZeroWhenNothingHasBeenMeasured(t *testing.T) {
	unwired := &RunTourCoordinatorHandler{}
	require.Zero(t, unwired.tourPerHopToll(context.Background(), &RunTourCoordinatorCommand{PlayerID: 5}))

	belowFloor := &RunTourCoordinatorHandler{jumpTolls: &fakeJumpTollReader{seconds: 0}}
	require.Zero(t, belowFloor.tourPerHopToll(context.Background(), &RunTourCoordinatorCommand{PlayerID: 5}))
}

// A negative reading is not a travel time. It can only come from a broken estimator, and
// letting it through would price a crossing as if it gave time BACK — the one direction that
// makes every distant candidate look free.
func TestTourPerHopToll_RefusesANonPositiveReading(t *testing.T) {
	h := &RunTourCoordinatorHandler{jumpTolls: &fakeJumpTollReader{seconds: -400}}
	require.Zero(t, h.tourPerHopToll(context.Background(), &RunTourCoordinatorCommand{PlayerID: 5}))
}

// The setter is what the daemon boot calls; without it the field stays nil and the tour
// plans exactly as it does today.
func TestSetJumpTollReader_WiresTheEstimator(t *testing.T) {
	h := &RunTourCoordinatorHandler{}
	require.Zero(t, h.tourPerHopToll(context.Background(), &RunTourCoordinatorCommand{PlayerID: 5}))

	h.SetJumpTollReader(&fakeJumpTollReader{seconds: 940})
	require.Equal(t, 940, h.tourPerHopToll(context.Background(), &RunTourCoordinatorCommand{PlayerID: 5}))
}
