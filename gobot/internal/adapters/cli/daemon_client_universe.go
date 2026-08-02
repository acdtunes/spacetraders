package cli

import (
	"context"
	"fmt"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// ListWaypoints lists the waypoints of a system from the daemon's waypoint cache
func (c *DaemonClient) ListWaypoints(ctx context.Context, systemSymbol string, trait, waypointType *string, playerIdent *PlayerIdentifier) (*pb.ListWaypointsResponse, error) {
	playerID, agentSymbol := playerPointers(playerIdent)
	req := &pb.ListWaypointsRequest{
		SystemSymbol: systemSymbol,
		Trait:        trait,
		Type:         waypointType,
		PlayerId:     playerID,
		AgentSymbol:  agentSymbol,
	}

	resp, err := c.client.ListWaypoints(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// GetWaypoint gets the detail of a single waypoint
func (c *DaemonClient) GetWaypoint(ctx context.Context, waypointSymbol string, playerIdent *PlayerIdentifier) (*pb.GetWaypointResponse, error) {
	playerID, agentSymbol := playerPointers(playerIdent)
	req := &pb.GetWaypointRequest{
		WaypointSymbol: waypointSymbol,
		PlayerId:       playerID,
		AgentSymbol:    agentSymbol,
	}

	resp, err := c.client.GetWaypoint(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// GetShipyardListings gets shipyard listings at a waypoint
func (c *DaemonClient) GetShipyardListings(ctx context.Context, systemSymbol, waypointSymbol string, playerID int) (*pb.GetShipyardListingsResponse, error) {
	req := &pb.GetShipyardListingsRequest{
		SystemSymbol:   systemSymbol,
		WaypointSymbol: waypointSymbol,
		PlayerId:       int32(playerID),
	}

	resp, err := c.client.GetShipyardListings(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// PurchaseShip purchases a ship from a shipyard
func (c *DaemonClient) PurchaseShip(ctx context.Context, purchasingShipSymbol, shipType string, playerID int, agentSymbol, shipyardWaypoint string) (*pb.PurchaseShipResponse, error) {
	req := &pb.PurchaseShipRequest{
		PurchasingShipSymbol: purchasingShipSymbol,
		ShipType:             shipType,
		PlayerId:             int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	if shipyardWaypoint != "" {
		req.ShipyardWaypoint = &shipyardWaypoint
	}

	resp, err := c.client.PurchaseShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// BatchPurchaseShips purchases multiple ships in batch
func (c *DaemonClient) BatchPurchaseShips(ctx context.Context, purchasingShipSymbol, shipType string, quantity, maxBudget, playerID int, agentSymbol, shipyardWaypoint, dedicateFleet string) (*pb.BatchPurchaseShipsResponse, error) {
	req := &pb.BatchPurchaseShipsRequest{
		PurchasingShipSymbol: purchasingShipSymbol,
		ShipType:             shipType,
		Quantity:             int32(quantity),
		MaxBudget:            int32(maxBudget),
		PlayerId:             int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	if shipyardWaypoint != "" {
		req.ShipyardWaypoint = &shipyardWaypoint
	}
	// Forward the optional --fleet role only when set, so an omitted flag
	// leaves the field nil (byte-identical: the daemon lands the hull undedicated).
	if dedicateFleet != "" {
		req.DedicateFleet = &dedicateFleet
	}

	resp, err := c.client.BatchPurchaseShips(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}
