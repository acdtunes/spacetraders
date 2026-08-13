package grpc

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipScrap "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/scrap"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

const scrapTestPlayerID = 1

// scrapRecordingMediator captures the dispatched request and replays a canned
// outcome, so the RPC's own translation is what a test observes.
type scrapRecordingMediator struct {
	sent     common.Request
	response common.Response
	err      error
}

func (m *scrapRecordingMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	m.sent = request
	return m.response, m.err
}

func (m *scrapRecordingMediator) Register(reflect.Type, common.RequestHandler) error { return nil }
func (m *scrapRecordingMediator) RegisterMiddleware(common.Middleware)               {}

// The verb is a thin operator shell: the hull and the resolved player must land on
// the scrap command, and the price must come back.
func TestScrapShipRPCDelegatesToTheScrapCommand(t *testing.T) {
	med := &scrapRecordingMediator{response: &shipScrap.ScrapShipResponse{
		Success: true, ShipSymbol: "TORWIND-446", WaypointSymbol: "X1-JP61-A1",
		TotalPrice: 733517, Message: "Scrapped TORWIND-446 at X1-JP61-A1 for 733517 credits",
	}}
	service := newDaemonServiceImpl(&DaemonServer{mediator: med})

	resp, err := service.ScrapShip(context.Background(), &pb.ScrapShipRequest{
		ShipSymbol: "TORWIND-446",
		PlayerId:   scrapTestPlayerID,
	})

	require.NoError(t, err)
	cmd, ok := med.sent.(*shipScrap.ScrapShipCommand)
	require.True(t, ok, "the RPC dispatches the scrap command, not a new one")
	require.Equal(t, "TORWIND-446", cmd.ShipSymbol)
	require.NotNil(t, cmd.PlayerID)
	require.Equal(t, scrapTestPlayerID, *cmd.PlayerID)

	require.True(t, resp.Success)
	require.Equal(t, int32(733517), resp.TotalPrice)
	require.Equal(t, "X1-JP61-A1", resp.WaypointSymbol)
	require.Empty(t, resp.Error)
}

// The cargo refusal is the whole point of the verb, so it must reach the operator
// VERBATIM as a response — a transport error would print as a daemon fault instead.
func TestScrapShipRPCSurfacesTheCargoRefusalToTheOperator(t *testing.T) {
	const refusal = "refusing to scrap TORWIND-446: it still holds 225 unit(s) of cargo, " +
		"which a scrap destroys without paying for — drain the hold first"
	med := &scrapRecordingMediator{err: errors.New(refusal)}
	service := newDaemonServiceImpl(&DaemonServer{mediator: med})

	resp, err := service.ScrapShip(context.Background(), &pb.ScrapShipRequest{
		ShipSymbol: "TORWIND-446",
		PlayerId:   scrapTestPlayerID,
	})

	require.NoError(t, err, "a refusal is reported in the response, not as a transport failure")
	require.False(t, resp.Success)
	require.Equal(t, refusal, resp.Error, "the refusal reaches the operator whole, not flattened")
	require.Zero(t, resp.TotalPrice)
}
