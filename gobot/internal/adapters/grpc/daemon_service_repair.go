package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/hullrepair"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// RepairUnreadableShip runs the confirmed repair against one named hull, through the
// daemon-side op (RULING #3). It is the manual door onto the same sequence the standing
// sweep runs: the confirmation and the money guard are not skipped, and a pass that writes
// still spends the hull's attempt budget, so driving this by hand cannot get around the
// bound. Only the backoff and a prior escalation are overridden — an operator asking
// directly is what those exist for.
func (s *daemonServiceImpl) RepairUnreadableShip(ctx context.Context, req *pb.RepairUnreadableShipRequest) (*pb.RepairUnreadableShipResponse, error) {
	if req.ShipSymbol == "" {
		return &pb.RepairUnreadableShipResponse{Error: "ship_symbol is required"}, nil
	}
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return &pb.RepairUnreadableShipResponse{ShipSymbol: req.ShipSymbol, Error: fmt.Sprintf("failed to resolve player: %v", err)}, nil
	}

	sweeper, err := s.daemon.hullRepairSweeper(playerID)
	if err != nil {
		return &pb.RepairUnreadableShipResponse{ShipSymbol: req.ShipSymbol, Error: err.Error()}, nil
	}

	result, err := sweeper.RepairNow(ctx, playerID, req.ShipSymbol)
	if err != nil {
		return &pb.RepairUnreadableShipResponse{ShipSymbol: req.ShipSymbol, Error: err.Error()}, nil
	}

	resp := &pb.RepairUnreadableShipResponse{
		Repaired:   result.Outcome == hullrepair.OutcomeRepaired,
		ShipSymbol: req.ShipSymbol,
		Outcome:    string(result.Outcome),
		Reason:     result.Reason,
	}
	if rec, found, ferr := s.daemon.hullRepairLedger().Find(ctx, playerID, req.ShipSymbol); ferr == nil && found {
		resp.Attempts = int32(rec.Attempts)
		resp.Escalated = rec.EscalatedAt != nil
	}
	return resp, nil
}
