package commands

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// fakeRepositioner records the travel delegation and can inject a failure — the reposition
// worker must forward exactly one move to the shared travel machinery and surface its error.
type fakeRepositioner struct {
	calls []string // "ship->destination"
	err   error
}

func (f *fakeRepositioner) RepositionToWaypoint(_ context.Context, shipSymbol, destinationWaypoint string, _ int) error {
	f.calls = append(f.calls, shipSymbol+"->"+destinationWaypoint)
	return f.err
}

// The worker delegates the whole relay to the shared travel machinery (the trade-route
// coordinator's RepositionToWaypoint) — it writes no jump logic of its own (sp-s232).
func TestScoutReposition_DelegatesToTravel(t *testing.T) {
	rep := &fakeRepositioner{}
	handler := NewScoutRepositionHandler(rep)
	cmd := &ScoutRepositionCommand{
		PlayerID:            shared.MustNewPlayerID(1),
		ShipSymbol:          "SAT-1",
		DestinationWaypoint: "X1-FAR-A1",
	}

	resp, err := handler.Handle(context.Background(), cmd)

	require.NoError(t, err)
	require.Equal(t, []string{"SAT-1->X1-FAR-A1"}, rep.calls, "the relay delegates exactly one move to shared travel()")
	r, ok := resp.(*ScoutRepositionResponse)
	require.True(t, ok, "returns a ScoutRepositionResponse")
	require.Equal(t, "SAT-1", r.ShipSymbol)
	require.Equal(t, "X1-FAR-A1", r.DestinationWaypoint)
}

// A travel failure is surfaced as an error so the container FAILS honestly (the runner
// releases the claim and the coordinator re-parks the post for a bounded retry) rather
// than reporting a false success on a stranded hull (sp-s232 fail-closed).
func TestScoutReposition_TravelError_FailsHonestly(t *testing.T) {
	rep := &fakeRepositioner{err: fmt.Errorf("destination jump gate under construction")}
	handler := NewScoutRepositionHandler(rep)
	cmd := &ScoutRepositionCommand{
		PlayerID:            shared.MustNewPlayerID(1),
		ShipSymbol:          "SAT-1",
		DestinationWaypoint: "X1-FAR-A1",
	}

	_, err := handler.Handle(context.Background(), cmd)

	require.Error(t, err, "a travel failure surfaces — never a false success on a stranded hull")
	require.Contains(t, err.Error(), "destination jump gate under construction", "the underlying cause is preserved verbatim")
}
