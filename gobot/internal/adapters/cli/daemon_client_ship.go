package cli

import (
	"context"
	"fmt"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// TransferCargoResponse is the CLI-side result of a ship-to-ship cargo move.
// Error carries the daemon's refusal verbatim when the move was turned away.
type TransferCargoResponse struct {
	Success          bool
	FromShipSymbol   string
	ToShipSymbol     string
	GoodSymbol       string
	UnitsTransferred int32
	RemainingUnits   int32
	Error            string
}

type JettisonResponse struct {
	ContainerID     string
	ShipSymbol      string
	GoodSymbol      string
	UnitsJettisoned int32
	Status          string
	Message         string
}

// ListShips lists all ships for a player
func (c *DaemonClient) ListShips(ctx context.Context, playerIdent *PlayerIdentifier) (*pb.ListShipsResponse, error) {
	playerID, agentSymbol := playerPointers(playerIdent)
	req := &pb.ListShipsRequest{
		PlayerId:    playerID,
		AgentSymbol: agentSymbol,
	}

	resp, err := c.client.ListShips(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// GetShip gets detailed ship information
func (c *DaemonClient) GetShip(ctx context.Context, shipSymbol string, playerIdent *PlayerIdentifier) (*pb.GetShipResponse, error) {
	playerID, agentSymbol := playerPointers(playerIdent)
	req := &pb.GetShipRequest{
		ShipSymbol:  shipSymbol,
		PlayerId:    playerID,
		AgentSymbol: agentSymbol,
	}

	resp, err := c.client.GetShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// RefreshShip forces a resync of a ship from the API, overwriting the daemon cache
func (c *DaemonClient) RefreshShip(ctx context.Context, shipSymbol string, playerIdent *PlayerIdentifier) (*pb.RefreshShipResponse, error) {
	playerID, agentSymbol := playerPointers(playerIdent)
	req := &pb.RefreshShipRequest{
		ShipSymbol:  shipSymbol,
		PlayerId:    playerID,
		AgentSymbol: agentSymbol,
	}

	resp, err := c.client.RefreshShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// ReserveShip reserves a ship for the captain's direct manual use, hiding it
// from every coordinator's assignment discovery. When force is true,
// a coordinator's live claim is PREEMPTED — atomically revoked and transferred
// to the captain (sp-w3yd) — instead of rejected.
func (c *DaemonClient) ReserveShip(ctx context.Context, shipSymbol string, reason *string, playerIdent *PlayerIdentifier, force bool) (*pb.ReserveShipResponse, error) {
	playerID, agentSymbol := playerPointers(playerIdent)
	req := &pb.ReserveShipRequest{
		ShipSymbol:  shipSymbol,
		Reason:      reason,
		PlayerId:    playerID,
		AgentSymbol: agentSymbol,
	}
	if force {
		req.Force = &force
	}

	resp, err := c.client.ReserveShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// ReleaseShip clears a captain reservation, returning the ship to idle so
// normal coordinator discovery can claim it again
func (c *DaemonClient) ReleaseShip(ctx context.Context, shipSymbol string, reason *string, playerIdent *PlayerIdentifier) (*pb.ReleaseShipResponse, error) {
	playerID, agentSymbol := playerPointers(playerIdent)
	req := &pb.ReleaseShipRequest{
		ShipSymbol:  shipSymbol,
		Reason:      reason,
		PlayerId:    playerID,
		AgentSymbol: agentSymbol,
	}

	resp, err := c.client.ReleaseShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// RetireShip withdraws a hull from service: its current tour finishes and sells, and once
// its hold is empty no coordinator plans it another. cancel clears the mark.
func (c *DaemonClient) RetireShip(ctx context.Context, shipSymbol string, cancel bool, playerIdent *PlayerIdentifier) (*pb.RetireShipResponse, error) {
	playerID, agentSymbol := playerPointers(playerIdent)
	req := &pb.RetireShipRequest{
		ShipSymbol:  shipSymbol,
		Cancel:      cancel,
		PlayerId:    playerID,
		AgentSymbol: agentSymbol,
	}

	resp, err := c.client.RetireShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// AssignShipFleet dedicates a ship to a named fleet, making it exclusive to
// that coordinator's discovery
func (c *DaemonClient) AssignShipFleet(ctx context.Context, shipSymbol, fleet string, playerIdent *PlayerIdentifier) (*pb.AssignShipFleetResponse, error) {
	playerID, agentSymbol := playerPointers(playerIdent)
	req := &pb.AssignShipFleetRequest{
		ShipSymbol:  shipSymbol,
		Fleet:       fleet,
		PlayerId:    playerID,
		AgentSymbol: agentSymbol,
	}

	resp, err := c.client.AssignShipFleet(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// UnassignShipFleet clears a ship's fleet dedication, returning it to the
// general pool
func (c *DaemonClient) UnassignShipFleet(ctx context.Context, shipSymbol string, playerIdent *PlayerIdentifier) (*pb.UnassignShipFleetResponse, error) {
	playerID, agentSymbol := playerPointers(playerIdent)
	req := &pb.UnassignShipFleetRequest{
		ShipSymbol:  shipSymbol,
		PlayerId:    playerID,
		AgentSymbol: agentSymbol,
	}

	resp, err := c.client.UnassignShipFleet(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// FleetHub adds or removes a standby-station ("hub") waypoint on a running
// operation's coordinator, live, with no container restart.
func (c *DaemonClient) FleetHub(ctx context.Context, operation, waypoint string, add bool, playerIdent *PlayerIdentifier) (*pb.FleetHubResponse, error) {
	playerID, agentSymbol := playerPointers(playerIdent)
	req := &pb.FleetHubRequest{
		Operation:   operation,
		Waypoint:    waypoint,
		Add:         add,
		PlayerId:    playerID,
		AgentSymbol: agentSymbol,
	}

	resp, err := c.client.FleetHub(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// ListFleets lists every dedicated fleet and its member ships
func (c *DaemonClient) ListFleets(ctx context.Context, playerIdent *PlayerIdentifier) (*pb.ListFleetsResponse, error) {
	playerID, agentSymbol := playerPointers(playerIdent)
	req := &pb.ListFleetsRequest{
		PlayerId:    playerID,
		AgentSymbol: agentSymbol,
	}

	resp, err := c.client.ListFleets(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// TransferCargo moves cargo between two co-located hulls. The daemon performs the
// move behind its own guards (RULING #3: the CLI never touches the game API itself).
func (c *DaemonClient) TransferCargo(
	ctx context.Context,
	fromShipSymbol, toShipSymbol, goodSymbol string,
	units int,
	playerID int,
	agentSymbol string,
) (*TransferCargoResponse, error) {
	req := &pb.TransferCargoRequest{
		FromShipSymbol: fromShipSymbol,
		ToShipSymbol:   toShipSymbol,
		GoodSymbol:     goodSymbol,
		Units:          int32(units),
		PlayerId:       int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.TransferCargo(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &TransferCargoResponse{
		Success:          resp.Success,
		FromShipSymbol:   resp.FromShipSymbol,
		ToShipSymbol:     resp.ToShipSymbol,
		GoodSymbol:       resp.GoodSymbol,
		UnitsTransferred: resp.UnitsTransferred,
		RemainingUnits:   resp.RemainingUnits,
		Error:            resp.Error,
	}, nil
}

// JettisonCargo jettisons cargo from a ship
func (c *DaemonClient) JettisonCargo(
	ctx context.Context,
	shipSymbol string,
	goodSymbol string,
	units int,
	playerID int,
	agentSymbol string,
) (*JettisonResponse, error) {
	req := &pb.JettisonCargoRequest{
		ShipSymbol: shipSymbol,
		GoodSymbol: goodSymbol,
		Units:      int32(units),
		PlayerId:   int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.JettisonCargo(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &JettisonResponse{
		ContainerID:     resp.ContainerId,
		ShipSymbol:      resp.ShipSymbol,
		GoodSymbol:      resp.GoodSymbol,
		UnitsJettisoned: resp.UnitsJettisoned,
		Status:          resp.Status,
		Message:         resp.Message,
	}, nil
}
