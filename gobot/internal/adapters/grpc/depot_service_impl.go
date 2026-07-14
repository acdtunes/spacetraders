package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// This file is the gRPC service surface for contract-depot management (bead sp-u9xa):
// it resolves the player, translates the proto request into the grpc-package spec DTOs,
// and delegates to the DaemonServer handlers (container_ops_depot.go), which drive the
// application Store. Pure translation — the durable behaviour lives one layer down.

// ApplyDepotTopology (mode A, declarative bulk) makes the persisted set exactly the
// requested depots.
func (s *daemonServiceImpl) ApplyDepotTopology(ctx context.Context, req *pb.ApplyDepotTopologyRequest) (*pb.ApplyDepotTopologyResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}
	spec := DepotTopologySpec{Depots: protoDepotsToSpecs(req.Depots)}
	if err := s.daemon.ApplyDepotTopology(ctx, playerID, spec); err != nil {
		return nil, fmt.Errorf("failed to apply depot topology: %w", err)
	}
	return &pb.ApplyDepotTopologyResponse{Status: "APPLIED", DepotCount: int32(len(spec.Depots))}, nil
}

// AddDepot (mode B, granular) adds one depot.
func (s *daemonServiceImpl) AddDepot(ctx context.Context, req *pb.AddDepotRequest) (*pb.AddDepotResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}
	if req.Depot == nil {
		return nil, fmt.Errorf("add depot: missing depot spec")
	}
	if err := s.daemon.AddDepot(ctx, playerID, protoDepotToSpec(req.Depot)); err != nil {
		return nil, fmt.Errorf("failed to add depot: %w", err)
	}
	return &pb.AddDepotResponse{Status: "ADDED"}, nil
}

// RemoveDepot (mode B, granular) removes one depot by id.
func (s *daemonServiceImpl) RemoveDepot(ctx context.Context, req *pb.RemoveDepotRequest) (*pb.RemoveDepotResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}
	if err := s.daemon.RemoveDepot(ctx, playerID, req.DepotId); err != nil {
		return nil, fmt.Errorf("failed to remove depot: %w", err)
	}
	return &pb.RemoveDepotResponse{Status: "REMOVED"}, nil
}

// AddDepotElement (mode B, granular) adds one element to a depot role.
func (s *daemonServiceImpl) AddDepotElement(ctx context.Context, req *pb.AddDepotElementRequest) (*pb.DepotElementResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}
	if err := s.daemon.AddDepotElement(ctx, playerID, req.DepotId, req.Role, req.Waypoint, req.ShipSymbol); err != nil {
		return nil, fmt.Errorf("failed to add depot element: %w", err)
	}
	return &pb.DepotElementResponse{Status: "ADDED"}, nil
}

// RemoveDepotElement (mode B, granular) removes the element crewed by a ship.
func (s *daemonServiceImpl) RemoveDepotElement(ctx context.Context, req *pb.RemoveDepotElementRequest) (*pb.DepotElementResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}
	if err := s.daemon.RemoveDepotElement(ctx, playerID, req.DepotId, req.Role, req.ShipSymbol); err != nil {
		return nil, fmt.Errorf("failed to remove depot element: %w", err)
	}
	return &pb.DepotElementResponse{Status: "REMOVED"}, nil
}

// PlaceDepotElement (mode B, granular) repositions an element to a waypoint.
func (s *daemonServiceImpl) PlaceDepotElement(ctx context.Context, req *pb.PlaceDepotElementRequest) (*pb.DepotElementResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}
	if err := s.daemon.PlaceDepotElement(ctx, playerID, req.DepotId, req.Role, req.ShipSymbol, req.Waypoint); err != nil {
		return nil, fmt.Errorf("failed to place depot element: %w", err)
	}
	return &pb.DepotElementResponse{Status: "PLACED"}, nil
}

// ListDepots returns the player's persisted depots as proto specs.
func (s *daemonServiceImpl) ListDepots(ctx context.Context, req *pb.ListDepotsRequest) (*pb.ListDepotsResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}
	depots, err := s.daemon.ListDepots(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to list depots: %w", err)
	}
	return &pb.ListDepotsResponse{Depots: domainDepotsToProto(depots)}, nil
}

// StartDepot (sp-38xc lifecycle) persists ONE depot's topology and launches its
// coordinators in one shot — live activation with no daemon restart.
func (s *daemonServiceImpl) StartDepot(ctx context.Context, req *pb.StartDepotRequest) (*pb.StartDepotResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}
	if req.Depot == nil {
		return nil, fmt.Errorf("start depot: missing depot spec")
	}
	launched, err := s.daemon.StartDepot(ctx, playerID, protoDepotToSpec(req.Depot))
	if err != nil {
		return nil, fmt.Errorf("failed to start depot: %w", err)
	}
	return &pb.StartDepotResponse{Status: "STARTED", Launched: int32(launched)}, nil
}

// StopDepot (sp-38xc lifecycle) tears down the named depot's running coordinators.
func (s *daemonServiceImpl) StopDepot(ctx context.Context, req *pb.StopDepotRequest) (*pb.StopDepotResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}
	stopped, err := s.daemon.StopDepot(ctx, playerID, req.DepotId)
	if err != nil {
		return nil, fmt.Errorf("failed to stop depot: %w", err)
	}
	return &pb.StopDepotResponse{Status: "STOPPED", Stopped: int32(stopped)}, nil
}

// --- proto <-> spec/domain translation ---

func protoDepotsToSpecs(pcs []*pb.DepotSpec) []DepotSpec {
	out := make([]DepotSpec, 0, len(pcs))
	for _, pc := range pcs {
		if pc == nil {
			continue
		}
		out = append(out, protoDepotToSpec(pc))
	}
	return out
}

func protoDepotToSpec(pc *pb.DepotSpec) DepotSpec {
	return DepotSpec{
		ID:            pc.Id,
		Warehouses:    protoElementsToSpecs(pc.Warehouses),
		Stockers:      protoElementsToSpecs(pc.Stockers),
		DeliveryHulls: protoElementsToSpecs(pc.DeliveryHulls),
		SourceHubs:    protoElementsToSpecs(pc.SourceHubs),
	}
}

func protoElementsToSpecs(pes []*pb.DepotElement) []ElementSpec {
	if len(pes) == 0 {
		return nil
	}
	out := make([]ElementSpec, 0, len(pes))
	for _, pe := range pes {
		if pe == nil {
			continue
		}
		out = append(out, ElementSpec{Waypoint: pe.Waypoint, ShipSymbol: pe.ShipSymbol})
	}
	return out
}

func domainDepotsToProto(depots []*depot.ContractDepot) []*pb.DepotSpec {
	out := make([]*pb.DepotSpec, 0, len(depots))
	for _, c := range depots {
		out = append(out, &pb.DepotSpec{
			Id:            c.ID(),
			Warehouses:    domainElementsToProto(c.Warehouses()),
			Stockers:      domainElementsToProto(c.Stockers()),
			DeliveryHulls: domainElementsToProto(c.DeliveryHulls()),
			SourceHubs:    domainElementsToProto(c.SourceHubs()),
		})
	}
	return out
}

func domainElementsToProto(es []depot.Element) []*pb.DepotElement {
	if len(es) == 0 {
		return nil
	}
	out := make([]*pb.DepotElement, 0, len(es))
	for _, e := range es {
		out = append(out, &pb.DepotElement{Waypoint: e.Waypoint, ShipSymbol: e.ShipSymbol})
	}
	return out
}
