package grpc

import (
	"context"
	"fmt"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// ListShips lists all ships for a player
func (s *daemonServiceImpl) ListShips(ctx context.Context, req *pb.ListShipsRequest) (*pb.ListShipsResponse, error) {
	var playerID *int
	if req.PlayerId != nil {
		pid := FromProtobufPlayerID(*req.PlayerId)
		playerID = &pid
	}

	agentSymbol := stringValue(req.AgentSymbol)

	ships, err := s.daemon.ListShips(ctx, playerID, agentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to list ships: %w", err)
	}

	return &pb.ListShipsResponse{
		Ships: ships,
	}, nil
}

// GetShip retrieves detailed ship information
func (s *daemonServiceImpl) GetShip(ctx context.Context, req *pb.GetShipRequest) (*pb.GetShipResponse, error) {
	var playerID *int
	if req.PlayerId != nil {
		pid := FromProtobufPlayerID(*req.PlayerId)
		playerID = &pid
	}

	agentSymbol := stringValue(req.AgentSymbol)

	shipDetail, err := s.daemon.GetShip(ctx, req.ShipSymbol, playerID, agentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to get ship: %w", err)
	}

	return &pb.GetShipResponse{
		Ship: shipDetail,
	}, nil
}

func (s *daemonServiceImpl) RefreshShip(ctx context.Context, req *pb.RefreshShipRequest) (*pb.RefreshShipResponse, error) {
	var playerID *int
	if req.PlayerId != nil {
		pid := FromProtobufPlayerID(*req.PlayerId)
		playerID = &pid
	}

	agentSymbol := stringValue(req.AgentSymbol)

	shipDetail, err := s.daemon.RefreshShip(ctx, req.ShipSymbol, playerID, agentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh ship: %w", err)
	}

	return &pb.RefreshShipResponse{
		Ship: shipDetail,
	}, nil
}

func (s *daemonServiceImpl) ReserveShip(ctx context.Context, req *pb.ReserveShipRequest) (*pb.ReserveShipResponse, error) {
	var playerID *int
	if req.PlayerId != nil {
		pid := FromProtobufPlayerID(*req.PlayerId)
		playerID = &pid
	}

	agentSymbol := stringValue(req.AgentSymbol)
	reason := stringValue(req.Reason)

	shipSymbol, respReason, warning, preempted, preemptedFrom, err := s.daemon.ReserveShip(ctx, req.ShipSymbol, reason, playerID, agentSymbol, req.GetForce())
	if err != nil {
		return nil, fmt.Errorf("failed to reserve ship: %w", err)
	}

	return &pb.ReserveShipResponse{
		ShipSymbol:    shipSymbol,
		Reason:        respReason,
		Warning:       warning,
		Preempted:     preempted,
		PreemptedFrom: preemptedFrom,
	}, nil
}

func (s *daemonServiceImpl) ReleaseShip(ctx context.Context, req *pb.ReleaseShipRequest) (*pb.ReleaseShipResponse, error) {
	var playerID *int
	if req.PlayerId != nil {
		pid := FromProtobufPlayerID(*req.PlayerId)
		playerID = &pid
	}

	agentSymbol := stringValue(req.AgentSymbol)
	reason := stringValue(req.Reason)

	shipSymbol, err := s.daemon.ReleaseShip(ctx, req.ShipSymbol, reason, playerID, agentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to release ship: %w", err)
	}

	return &pb.ReleaseShipResponse{
		ShipSymbol: shipSymbol,
	}, nil
}

func (s *daemonServiceImpl) AssignShipFleet(ctx context.Context, req *pb.AssignShipFleetRequest) (*pb.AssignShipFleetResponse, error) {
	var playerID *int
	if req.PlayerId != nil {
		pid := FromProtobufPlayerID(*req.PlayerId)
		playerID = &pid
	}

	agentSymbol := stringValue(req.AgentSymbol)

	shipSymbol, fleet, err := s.daemon.AssignShipFleet(ctx, req.ShipSymbol, req.Fleet, playerID, agentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to assign ship fleet: %w", err)
	}

	return &pb.AssignShipFleetResponse{
		ShipSymbol: shipSymbol,
		Fleet:      fleet,
	}, nil
}

// FleetHub adds or removes a standby-station ("hub") on a running operation's
// coordinator, live. Resolves the player from player_id or agent_symbol
// (like the other coordinator RPCs), then delegates the persisted-set mutation to
// the daemon, which is the single writer (RULINGS #3).
func (s *daemonServiceImpl) FleetHub(ctx context.Context, req *pb.FleetHubRequest) (*pb.FleetHubResponse, error) {
	// player_id is optional (*int32) on this request; 0 lets resolvePlayerID fall
	// back to agent_symbol, matching the other fleet RPCs.
	var pid int32
	if req.PlayerId != nil {
		pid = *req.PlayerId
	}
	playerID, err := s.resolvePlayerID(ctx, pid, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	stations, changed, err := s.daemon.MutateStandbyStation(ctx, req.Operation, req.Waypoint, req.Add, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to mutate standby station: %w", err)
	}

	return &pb.FleetHubResponse{
		Operation:       req.Operation,
		StandbyStations: stations,
		Changed:         changed,
	}, nil
}

// ScanDedupAllowlist adds, removes, or lists ships on the sp-7q61f scan-dedup
// A/B test's live allowlist. Resolves the player from player_id or
// agent_symbol (like the other coordinator RPCs), then delegates the
// persisted-set mutation/read to the daemon, which is the single writer
// (RULINGS #3).
func (s *daemonServiceImpl) ScanDedupAllowlist(ctx context.Context, req *pb.ScanDedupAllowlistRequest) (*pb.ScanDedupAllowlistResponse, error) {
	var pid int32
	if req.PlayerId != nil {
		pid = *req.PlayerId
	}
	playerID, err := s.resolvePlayerID(ctx, pid, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	switch req.Action {
	case "add", "remove":
		if req.ShipSymbol == "" {
			return nil, fmt.Errorf("ship_symbol is required for action %q", req.Action)
		}
		ships, changed, err := s.daemon.MutateScanDedupAllowlist(ctx, req.ShipSymbol, req.Action == "add", playerID)
		if err != nil {
			return nil, fmt.Errorf("failed to mutate scan-dedup allowlist: %w", err)
		}
		return &pb.ScanDedupAllowlistResponse{ShipSymbols: ships, Changed: changed}, nil
	case "list":
		ships, err := s.daemon.ListScanDedupAllowlist(ctx, playerID)
		if err != nil {
			return nil, fmt.Errorf("failed to list scan-dedup allowlist: %w", err)
		}
		return &pb.ScanDedupAllowlistResponse{ShipSymbols: ships}, nil
	default:
		return nil, fmt.Errorf("unknown scan-dedup allowlist action %q (want add|remove|list)", req.Action)
	}
}

func (s *daemonServiceImpl) UnassignShipFleet(ctx context.Context, req *pb.UnassignShipFleetRequest) (*pb.UnassignShipFleetResponse, error) {
	var playerID *int
	if req.PlayerId != nil {
		pid := FromProtobufPlayerID(*req.PlayerId)
		playerID = &pid
	}

	agentSymbol := stringValue(req.AgentSymbol)

	// Unassign clears the dedication (assign-to-empty through the single write
	// path, sp-l7h2) AND breaks the live coordinator work-claim so the
	// coordinator stops routing the hull (sp-w3yd) — not just the tag.
	shipSymbol, err := s.daemon.UnassignShipFleet(ctx, req.ShipSymbol, playerID, agentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to unassign ship fleet: %w", err)
	}

	return &pb.UnassignShipFleetResponse{
		ShipSymbol: shipSymbol,
	}, nil
}

// RetireShip is deliberately NOT UnassignShipFleet: it leaves both the dedication and the
// live claim alone, so the tour in flight finishes and sells, and the hull is never handed
// to the general pool where another coordinator would re-employ it.
func (s *daemonServiceImpl) RetireShip(ctx context.Context, req *pb.RetireShipRequest) (*pb.RetireShipResponse, error) {
	var playerID *int
	if req.PlayerId != nil {
		pid := FromProtobufPlayerID(*req.PlayerId)
		playerID = &pid
	}

	shipSymbol, retiring, cargoUnits, drained, err := s.daemon.RetireShip(
		ctx, req.ShipSymbol, req.GetCancel(), playerID, stringValue(req.AgentSymbol))
	if err != nil {
		return nil, fmt.Errorf("failed to retire ship: %w", err)
	}

	return &pb.RetireShipResponse{
		ShipSymbol: shipSymbol,
		Retiring:   retiring,
		CargoUnits: int32(cargoUnits),
		Drained:    drained,
	}, nil
}

func (s *daemonServiceImpl) ListFleets(ctx context.Context, req *pb.ListFleetsRequest) (*pb.ListFleetsResponse, error) {
	var playerID *int
	if req.PlayerId != nil {
		pid := FromProtobufPlayerID(*req.PlayerId)
		playerID = &pid
	}

	agentSymbol := stringValue(req.AgentSymbol)

	fleets, err := s.daemon.ListFleets(ctx, playerID, agentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to list fleets: %w", err)
	}

	return &pb.ListFleetsResponse{
		Fleets: fleets,
	}, nil
}
