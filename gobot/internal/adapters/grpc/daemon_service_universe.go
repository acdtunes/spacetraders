package grpc

import (
	"context"
	"fmt"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

func (s *daemonServiceImpl) ListWaypoints(ctx context.Context, req *pb.ListWaypointsRequest) (*pb.ListWaypointsResponse, error) {
	var playerID *int
	if req.PlayerId != nil {
		pid := FromProtobufPlayerID(*req.PlayerId)
		playerID = &pid
	}

	agentSymbol := stringValue(req.AgentSymbol)

	trait := stringValue(req.Trait)
	waypointType := stringValue(req.Type)

	waypoints, err := s.daemon.ListWaypoints(ctx, req.SystemSymbol, trait, waypointType, playerID, agentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to list waypoints: %w", err)
	}

	return &pb.ListWaypointsResponse{
		Waypoints: waypoints,
	}, nil
}

func (s *daemonServiceImpl) GetWaypoint(ctx context.Context, req *pb.GetWaypointRequest) (*pb.GetWaypointResponse, error) {
	var playerID *int
	if req.PlayerId != nil {
		pid := FromProtobufPlayerID(*req.PlayerId)
		playerID = &pid
	}

	agentSymbol := stringValue(req.AgentSymbol)

	waypoint, err := s.daemon.GetWaypoint(ctx, req.WaypointSymbol, playerID, agentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to get waypoint: %w", err)
	}

	return &pb.GetWaypointResponse{
		Waypoint: waypoint,
	}, nil
}

// GetShipyardListings retrieves available ships at a shipyard
func (s *daemonServiceImpl) GetShipyardListings(ctx context.Context, req *pb.GetShipyardListingsRequest) (*pb.GetShipyardListingsResponse, error) {
	var playerID *int
	if req.PlayerId != 0 {
		pid := FromProtobufPlayerID(req.PlayerId)
		playerID = &pid
	}

	agentSymbol := stringValue(req.AgentSymbol)

	listings, shipyardSymbol, modificationFee, err := s.daemon.GetShipyardListings(ctx, req.SystemSymbol, req.WaypointSymbol, playerID, agentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to get shipyard listings: %w", err)
	}

	return &pb.GetShipyardListingsResponse{
		Listings:        listings,
		ShipyardSymbol:  shipyardSymbol,
		ModificationFee: modificationFee,
	}, nil
}

// PurchaseShip purchases a single ship from a shipyard
func (s *daemonServiceImpl) PurchaseShip(ctx context.Context, req *pb.PurchaseShipRequest) (*pb.PurchaseShipResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	// Convert optional shipyard waypoint
	var shipyardWaypoint *string
	if req.ShipyardWaypoint != nil {
		shipyardWaypoint = req.ShipyardWaypoint
	}

	containerID, purchasedShipSymbol, purchasePrice, agentCredits, status, err := s.daemon.PurchaseShip(
		ctx,
		req.PurchasingShipSymbol,
		req.ShipType,
		playerID,
		shipyardWaypoint,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to purchase ship: %w", err)
	}

	return &pb.PurchaseShipResponse{
		ContainerId:         containerID,
		PurchasedShipSymbol: purchasedShipSymbol,
		PurchasePrice:       int32(purchasePrice),
		AgentCredits:        int32(agentCredits),
		Status:              status,
	}, nil
}

// BatchPurchaseShips purchases multiple ships from a shipyard as a background operation
func (s *daemonServiceImpl) BatchPurchaseShips(ctx context.Context, req *pb.BatchPurchaseShipsRequest) (*pb.BatchPurchaseShipsResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	// Convert optional parameters
	var shipyardWaypoint *string
	if req.ShipyardWaypoint != nil {
		shipyardWaypoint = req.ShipyardWaypoint
	}

	var iterations *int
	if req.Iterations != nil {
		iter := int(*req.Iterations)
		iterations = &iter
	}

	// The optional operator-named fleet to dedicate each purchased hull to
	// atomically at purchase. Absent -> "" -> byte-identical (hull lands undedicated).
	dedicateFleet := ""
	if req.DedicateFleet != nil {
		dedicateFleet = *req.DedicateFleet
	}

	containerID, shipsToPurchase, maxBudget, resolvedShipyard, status, err := s.daemon.BatchPurchaseShips(
		ctx,
		req.PurchasingShipSymbol,
		req.ShipType,
		int(req.Quantity),
		int(req.MaxBudget),
		playerID,
		shipyardWaypoint,
		iterations,
		dedicateFleet,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to batch purchase ships: %w", err)
	}

	return &pb.BatchPurchaseShipsResponse{
		ContainerId:      containerID,
		ShipsToPurchase:  shipsToPurchase,
		MaxBudget:        maxBudget,
		ShipyardWaypoint: resolvedShipyard,
		Status:           status,
	}, nil
}
