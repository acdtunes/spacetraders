package commands

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// fakeFerryRepositioner records the travel delegation and can inject a failure — the
// ferry worker must forward exactly one move to the shared travel machinery (the
// trade-route coordinator's RepositionToWaypoint) and surface its error honestly.
type fakeFerryRepositioner struct {
	calls []string // "ship->destination"
	err   error
}

func (f *fakeFerryRepositioner) RepositionToWaypoint(_ context.Context, shipSymbol, destinationWaypoint string, _ int) error {
	f.calls = append(f.calls, shipSymbol+"->"+destinationWaypoint)
	return f.err
}

// The ferry worker delegates the whole cross-system relay to the shared travel
// machinery — it writes no jump logic of its own (sp-f5pr, twin of scout_reposition).
func TestWorkerFerry_DelegatesToTravel(t *testing.T) {
	rep := &fakeFerryRepositioner{}
	handler := NewWorkerFerryHandler(rep)
	cmd := &WorkerFerryCommand{
		PlayerID:            shared.MustNewPlayerID(1),
		ShipSymbol:          "LIGHT-7",
		DestinationWaypoint: "X1-DP51-M1",
		CoordinatorID:       "worker_rebalancer_coordinator-1",
	}

	resp, err := handler.Handle(context.Background(), cmd)

	require.NoError(t, err)
	require.Equal(t, []string{"LIGHT-7->X1-DP51-M1"}, rep.calls, "the ferry delegates exactly one move to shared travel()")
	r, ok := resp.(*WorkerFerryResponse)
	require.True(t, ok, "returns a WorkerFerryResponse")
	require.Equal(t, "LIGHT-7", r.ShipSymbol)
	require.Equal(t, "X1-DP51-M1", r.DestinationWaypoint)
}

// A travel failure is surfaced as an error so the container FAILS honestly (the runner
// releases the claim and the coordinator re-evaluates the hull next tick) rather than
// reporting a false success on a stranded hull (sp-f5pr fail-closed).
func TestWorkerFerry_TravelError_FailsHonestly(t *testing.T) {
	rep := &fakeFerryRepositioner{err: fmt.Errorf("destination jump gate under construction")}
	handler := NewWorkerFerryHandler(rep)
	cmd := &WorkerFerryCommand{
		PlayerID:            shared.MustNewPlayerID(1),
		ShipSymbol:          "LIGHT-7",
		DestinationWaypoint: "X1-DP51-M1",
	}

	_, err := handler.Handle(context.Background(), cmd)

	require.Error(t, err, "a travel failure surfaces — never a false success on a stranded hull")
	require.Contains(t, err.Error(), "destination jump gate under construction", "the underlying cause is preserved verbatim")
}
