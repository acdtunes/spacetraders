package grpc

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// fakeJumpMediator returns a canned response/error for whatever request it
// receives, letting these tests exercise daemonServiceImpl.JumpShip without
// standing up a real handler, ship repository, or database - mirroring
// blockingMediator's role in recover_running_containers_pin_test.go.
type fakeJumpMediator struct {
	response common.Response
	err      error
}

func (m *fakeJumpMediator) Send(_ context.Context, _ common.Request) (common.Response, error) {
	return m.response, m.err
}

func (m *fakeJumpMediator) Register(_ reflect.Type, _ common.RequestHandler) error { return nil }

func (m *fakeJumpMediator) RegisterMiddleware(_ common.Middleware) {}

// A successful jump must emit a workflow.finished captain event carrying the
// ship and destination, so the watchkeeper observes the jump - mirroring how
// ContainerRunner.signalCompletionWithStatus reports finished workflows.
func TestJumpShip_SuccessfulJump_EmitsWorkflowFinishedEvent(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	med := &fakeJumpMediator{response: &shipNav.JumpShipResponse{
		Success:           true,
		NavigatedToGate:   true,
		JumpGateSymbol:    "X1-AB12-GATE",
		DestinationSystem: "X1-CD34",
		CooldownSeconds:   60,
		Message:           "jumped",
	}}
	impl := newDaemonServiceImpl(&DaemonServer{mediator: med})

	resp, err := impl.JumpShip(context.Background(), &pb.JumpShipRequest{
		ShipSymbol:        "PROBE-1",
		DestinationSystem: "X1-CD34",
		PlayerId:          1,
	})
	require.NoError(t, err)

	// The gRPC response must faithfully carry every field from the
	// handler's JumpShipResponse - not just Success - so a field-swap in
	// the pb.JumpShipResponse construction would be caught here.
	require.True(t, resp.Success)
	require.True(t, resp.NavigatedToGate)
	require.Equal(t, "X1-AB12-GATE", resp.JumpGateSymbol)
	require.Equal(t, "X1-CD34", resp.DestinationSystem)
	require.Equal(t, int32(60), resp.CooldownSeconds)
	require.Equal(t, "jumped", resp.Message)
	require.Empty(t, resp.Error)

	require.Len(t, rec.events, 1)
	require.Equal(t, captain.EventWorkflowFinished, rec.events[0].Type)
	require.Equal(t, "PROBE-1", rec.events[0].Ship)
	require.Equal(t, 1, rec.events[0].PlayerID)
	require.Contains(t, rec.events[0].Payload, "X1-CD34")
}

// A failed jump (e.g., the mediator/handler returns an error) must still
// emit a workflow.failed event - completion means the workflow reached an
// end state, success or not.
func TestJumpShip_FailedJump_EmitsWorkflowFailedEvent(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	med := &fakeJumpMediator{err: fmt.Errorf("cannot jump to X1-CD34: destination jump gate is still under construction")}
	impl := newDaemonServiceImpl(&DaemonServer{mediator: med})

	resp, err := impl.JumpShip(context.Background(), &pb.JumpShipRequest{
		ShipSymbol:        "PROBE-1",
		DestinationSystem: "X1-CD34",
		PlayerId:          1,
	})
	require.NoError(t, err) // gRPC-level error is nil; failure is carried in the response
	require.False(t, resp.Success)
	require.Contains(t, resp.Error, "under construction")

	require.Len(t, rec.events, 1)
	require.Equal(t, captain.EventWorkflowFailed, rec.events[0].Type)
	require.Equal(t, "PROBE-1", rec.events[0].Ship)
	require.Contains(t, rec.events[0].Payload, "under construction")
}
