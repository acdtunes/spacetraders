package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// assignCall records one AssignShipFleet invocation so a test can assert the
// exact (ship, fleet) tuple the add path wrote.
type assignCall struct {
	ship  string
	fleet string
}

// fakeFleetMutator is an in-memory fleetMutator that records every call. It has
// NO container-restart method by construction — that is the point: the `fleet
// add`/`fleet remove` verbs can only ever reach the tag-write and membership-read
// RPCs, so "the coordinator container is never restarted" is guaranteed by the
// surface these tests exercise, not merely asserted.
type fakeFleetMutator struct {
	assignCalls   []assignCall
	unassignCalls []string
	listResp      *pb.ListFleetsResponse
	assignErr     error
	unassignErr   error
	listErr       error
}

func (f *fakeFleetMutator) AssignShipFleet(_ context.Context, shipSymbol, fleet string, _ *int32, _ *string) (*pb.AssignShipFleetResponse, error) {
	f.assignCalls = append(f.assignCalls, assignCall{ship: shipSymbol, fleet: fleet})
	if f.assignErr != nil {
		return nil, f.assignErr
	}
	return &pb.AssignShipFleetResponse{ShipSymbol: shipSymbol, Fleet: fleet}, nil
}

func (f *fakeFleetMutator) UnassignShipFleet(_ context.Context, shipSymbol string, _ *int32, _ *string) (*pb.UnassignShipFleetResponse, error) {
	f.unassignCalls = append(f.unassignCalls, shipSymbol)
	if f.unassignErr != nil {
		return nil, f.unassignErr
	}
	return &pb.UnassignShipFleetResponse{ShipSymbol: shipSymbol}, nil
}

func (f *fakeFleetMutator) ListFleets(_ context.Context, _ *int32, _ *string) (*pb.ListFleetsResponse, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if f.listResp != nil {
		return f.listResp, nil
	}
	return &pb.ListFleetsResponse{}, nil
}

// fleetsFixture builds a ListFleetsResponse from a fleet-name -> member-symbols
// map, so a test can state the live fleet membership the remove-guard reads.
func fleetsFixture(membership map[string][]string) *pb.ListFleetsResponse {
	resp := &pb.ListFleetsResponse{}
	for name, ships := range membership {
		fleet := &pb.Fleet{Name: name}
		for _, s := range ships {
			fleet.Ships = append(fleet.Ships, &pb.FleetShip{ShipSymbol: s, Idle: true})
		}
		resp.Fleets = append(resp.Fleets, fleet)
	}
	return resp
}

// TestFleetAdd_DispatchedNextTick_NoRestart (sp-4s9m acceptance): adding a hull
// to a running operation writes exactly ONE dedication (AssignShipFleet with the
// operation as the fleet tag) and nothing else — no unassign, and (by the shape
// of fleetMutator) no container restart. The coordinator then discovers the hull
// live via ship_pool_manager.go FindIdleShipsByFleet (proven by
// TestFindIdleShipsByFleet_ReturnsOnlyIdleMembersOfNamedFleet), so it is
// dispatched from the next tick.
func TestFleetAdd_DispatchedNextTick_NoRestart(t *testing.T) {
	client := &fakeFleetMutator{}

	msg, err := runFleetAdd(context.Background(), client, "contract", "TORWIND-5", nil, nil)
	require.NoError(t, err)

	require.Equal(t, []assignCall{{ship: "TORWIND-5", fleet: "contract"}}, client.assignCalls,
		"add must write exactly one dedication, tagging the ship into the operation's fleet")
	require.Empty(t, client.unassignCalls, "add must never clear a dedication")
	require.Contains(t, msg, "TORWIND-5")
	require.Contains(t, msg, "contract")
	require.Contains(t, msg, "next tick")
}

// The daemon owns the tag write; a failure there must surface to the operator,
// naming the ship and operation, not be swallowed.
func TestFleetAdd_PropagatesDaemonError(t *testing.T) {
	client := &fakeFleetMutator{assignErr: errors.New("daemon unavailable")}

	_, err := runFleetAdd(context.Background(), client, "contract", "TORWIND-5", nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "TORWIND-5")
	require.Contains(t, err.Error(), "contract")
	require.Contains(t, err.Error(), "daemon unavailable")
}

// TestFleetRemove_MidContract_FinishesOrHandsOff_NoStranded (sp-4s9m
// acceptance): removing a member hull clears its dedication with exactly ONE
// UnassignShipFleet write and nothing else — no release, no restart (the
// fleetMutator surface has neither). Clearing the tag does not evict the hull's
// current container claim, so a hull removed mid-contract finishes its in-flight
// leg and then returns to the general pool; the coordinator simply stops
// re-selecting it next pass. No contract is stranded.
func TestFleetRemove_MidContract_FinishesOrHandsOff_NoStranded(t *testing.T) {
	client := &fakeFleetMutator{
		listResp: fleetsFixture(map[string][]string{"contract": {"TORWIND-1", "TORWIND-5"}}),
	}

	msg, err := runFleetRemove(context.Background(), client, "contract", "TORWIND-1", nil, nil)
	require.NoError(t, err)

	require.Equal(t, []string{"TORWIND-1"}, client.unassignCalls,
		"remove must clear exactly the named hull's dedication")
	require.Empty(t, client.assignCalls, "remove must never write a new dedication")
	require.Contains(t, msg, "TORWIND-1")
	require.Contains(t, msg, "in-progress contract leg", "the message must promise a clean hand-off, not an abort")
}

// TestFleetRemove_ClearsDedication_CoordinatorStopsUsing (sp-4s9m acceptance):
// the happy-path removal routes the clear through the daemon's UnassignShipFleet
// (the single tag-write path). Once cleared, FindIdleShipsByFleet no longer
// returns the hull (proven for the empty tag by
// TestFindIdleShipsByFleet_ReturnsOnlyIdleMembersOfNamedFleet's membership
// filter), so the coordinator stops dispatching it.
func TestFleetRemove_ClearsDedication_CoordinatorStopsUsing(t *testing.T) {
	client := &fakeFleetMutator{
		listResp: fleetsFixture(map[string][]string{"contract": {"TORWIND-1"}}),
	}

	_, err := runFleetRemove(context.Background(), client, "contract", "TORWIND-1", nil, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"TORWIND-1"}, client.unassignCalls)
}

// TestFleetRemove_RespectsExclusivity_RefusesForeignOperation (sp-4s9m
// acceptance, no-poach at the operator surface): `fleet remove --operation
// contract` on a hull that is actually dedicated to `stocker` must REFUSE, naming
// the real fleet, and must NOT clear the tag — a mistyped operation can never
// pull a hull out of a different coordinator's fleet. (The coordinator-side
// no-poach — a foreign coordinator can never CLAIM a dedicated hull — is proven
// at the repository by TestClaimShip_RejectsForeignOperationOnDedicatedHull.)
func TestFleetRemove_RespectsExclusivity_RefusesForeignOperation(t *testing.T) {
	client := &fakeFleetMutator{
		listResp: fleetsFixture(map[string][]string{
			"contract": {"TORWIND-5"},
			"stocker":  {"TORWIND-1"},
		}),
	}

	_, err := runFleetRemove(context.Background(), client, "contract", "TORWIND-1", nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "stocker", "the error must name the hull's real fleet")
	require.Contains(t, err.Error(), "TORWIND-1")
	require.Empty(t, client.unassignCalls, "a foreign-operation removal must not clear the tag")
}

// A hull that carries no dedication at all cannot be removed from an operation —
// the guard reports it plainly and writes nothing.
func TestFleetRemove_ShipInNoFleet_Errors(t *testing.T) {
	client := &fakeFleetMutator{
		listResp: fleetsFixture(map[string][]string{"contract": {"TORWIND-5"}}),
	}

	_, err := runFleetRemove(context.Background(), client, "contract", "TORWIND-9", nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "TORWIND-9")
	require.Empty(t, client.unassignCalls)
}

// The membership read is a hard pre-check: if ListFleets fails, removal aborts
// WITHOUT clearing the tag, rather than guessing membership and possibly yanking
// a hull out of the wrong fleet.
func TestFleetRemove_ListFleetsError_AbortsWithoutClearing(t *testing.T) {
	client := &fakeFleetMutator{listErr: errors.New("daemon unavailable")}

	_, err := runFleetRemove(context.Background(), client, "contract", "TORWIND-1", nil, nil)
	require.Error(t, err)
	require.Empty(t, client.unassignCalls, "an unverified removal must never clear a dedication")
}
