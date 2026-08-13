package grpc

import (
	"context"
	"fmt"

	shipScrap "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/scrap"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// ScrapShip sells a hull for credits and retires it from the fleet, through the
// daemon-side op (RULING #3). A refusal — most often a hull that still holds
// cargo — is returned in the response, not as a transport error, so the operator
// reads the daemon's own words instead of a gRPC fault.
func (s *daemonServiceImpl) ScrapShip(ctx context.Context, req *pb.ScrapShipRequest) (*pb.ScrapShipResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return &pb.ScrapShipResponse{ShipSymbol: req.ShipSymbol, Error: fmt.Sprintf("failed to resolve player: %v", err)}, nil
	}

	cmd := &shipScrap.ScrapShipCommand{ShipSymbol: req.ShipSymbol, PlayerID: &playerID}

	result, err := s.daemon.mediator.Send(ctx, cmd)
	if err != nil {
		return &pb.ScrapShipResponse{ShipSymbol: req.ShipSymbol, Error: err.Error()}, nil
	}
	resp, ok := result.(*shipScrap.ScrapShipResponse)
	if !ok {
		return &pb.ScrapShipResponse{ShipSymbol: req.ShipSymbol, Error: "unexpected response type from ScrapShipCommand"}, nil
	}

	return &pb.ScrapShipResponse{
		Success:        resp.Success,
		ShipSymbol:     resp.ShipSymbol,
		WaypointSymbol: resp.WaypointSymbol,
		TotalPrice:     int32(resp.TotalPrice),
		Message:        resp.Message,
	}, nil
}
