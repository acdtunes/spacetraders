package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// These tests cover the `fleet hub add|remove` CLI surface (sp-jcke): the
// operation-oriented, live mutation of a running coordinator's standby-station
// ("hub") set. By construction the fake below has ONLY the FleetHub RPC — no
// container-restart method — so "no restart" is guaranteed by the surface these
// verbs can reach, exactly as the `fleet add`/`remove` tests guarantee it for the
// ship tag. The daemon is the sole writer of the persisted set (RULINGS #3).

// hubCall records one FleetHub invocation so a test can assert the exact
// (operation, waypoint, add) tuple the verb wrote.
type hubCall struct {
	operation string
	waypoint  string
	add       bool
}

// fakeHubMutator is an in-memory hubMutator recording every call and serving a
// canned response. It has NO restart method by construction.
type fakeHubMutator struct {
	calls   []hubCall
	resp    *pb.FleetHubResponse
	respErr error
}

func (f *fakeHubMutator) FleetHub(_ context.Context, operation, waypoint string, add bool, _ *int32, _ *string) (*pb.FleetHubResponse, error) {
	f.calls = append(f.calls, hubCall{operation: operation, waypoint: waypoint, add: add})
	if f.respErr != nil {
		return nil, f.respErr
	}
	return f.resp, nil
}

func TestRunFleetHubAdd_MutatesRunningCoordinatorLive(t *testing.T) {
	client := &fakeHubMutator{resp: &pb.FleetHubResponse{
		Operation:       "contract",
		StandbyStations: []string{"X1-TW-A1", "X1-TW-C3"},
		Changed:         true,
	}}

	msg, err := runFleetHubAdd(context.Background(), client, "contract", "X1-TW-C3", nil, nil)
	require.NoError(t, err)

	require.Len(t, client.calls, 1)
	require.Equal(t, hubCall{operation: "contract", waypoint: "X1-TW-C3", add: true}, client.calls[0])
	// The message must confirm the live, no-restart nature and name the new hub.
	require.Contains(t, msg, "X1-TW-C3")
	require.Contains(t, msg, "contract")
	require.Contains(t, strings.ToLower(msg), "no container restart")
}

func TestRunFleetHubAdd_AlreadyAHub_ReportsNoOp(t *testing.T) {
	client := &fakeHubMutator{resp: &pb.FleetHubResponse{
		Operation:       "contract",
		StandbyStations: []string{"X1-TW-A1"},
		Changed:         false, // already a hub → daemon reports no change
	}}

	msg, err := runFleetHubAdd(context.Background(), client, "contract", "X1-TW-A1", nil, nil)
	require.NoError(t, err)
	require.Contains(t, strings.ToLower(msg), "already")
}

func TestRunFleetHubRemove_MutatesRunningCoordinatorLive(t *testing.T) {
	client := &fakeHubMutator{resp: &pb.FleetHubResponse{
		Operation:       "contract",
		StandbyStations: []string{"X1-TW-A1"},
		Changed:         true,
	}}

	msg, err := runFleetHubRemove(context.Background(), client, "contract", "X1-TW-C3", nil, nil)
	require.NoError(t, err)

	require.Len(t, client.calls, 1)
	require.Equal(t, hubCall{operation: "contract", waypoint: "X1-TW-C3", add: false}, client.calls[0])
	require.Contains(t, msg, "X1-TW-C3")
	require.Contains(t, strings.ToLower(msg), "no container restart")
}

func TestRunFleetHubRemove_NotAHub_ReportsNoOp(t *testing.T) {
	client := &fakeHubMutator{resp: &pb.FleetHubResponse{
		Operation:       "contract",
		StandbyStations: []string{"X1-TW-A1"},
		Changed:         false, // absent → nothing removed
	}}

	msg, err := runFleetHubRemove(context.Background(), client, "contract", "X1-TW-Z9", nil, nil)
	require.NoError(t, err)
	require.Contains(t, strings.ToLower(msg), "not a")
}

func TestRunFleetHubAdd_DaemonError_Propagates(t *testing.T) {
	client := &fakeHubMutator{respErr: errors.New("no running contract coordinator")}

	_, err := runFleetHubAdd(context.Background(), client, "contract", "X1-TW-C3", nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "X1-TW-C3")
}
