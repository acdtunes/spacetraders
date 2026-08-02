package cli

import (
	"context"
	"fmt"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

type NavigateResponse struct {
	ContainerID   string
	ShipSymbol    string
	Destination   string
	Status        string
	EstimatedTime int32
}

type RouteResponse struct {
	ContainerID string
	ShipSymbol  string
	Destination string
	Status      string
}

type WarpResponse struct {
	ContainerID string
	ShipSymbol  string
	Destination string
	Status      string
}

type DockResponse struct {
	ContainerID string
	ShipSymbol  string
	Status      string
}

type OrbitResponse struct {
	ContainerID string
	ShipSymbol  string
	Status      string
}

type RefuelResponse struct {
	ContainerID string
	ShipSymbol  string
	FuelAdded   int32
	CreditsCost int32
	Status      string
}

type JumpResponse struct {
	Success           bool
	NavigatedToGate   bool
	JumpGateSymbol    string
	DestinationSystem string
	CooldownSeconds   int32
	Message           string
	Error             string
}

// NavigateShip initiates ship navigation
func (c *DaemonClient) NavigateShip(
	ctx context.Context,
	shipSymbol, destination string,
	playerID int,
	agentSymbol string,
) (*NavigateResponse, error) {
	req := &pb.NavigateShipRequest{
		ShipSymbol:  shipSymbol,
		Destination: destination,
		PlayerId:    int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.NavigateShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &NavigateResponse{
		ContainerID:   resp.ContainerId,
		ShipSymbol:    resp.ShipSymbol,
		Destination:   resp.Destination,
		Status:        resp.Status,
		EstimatedTime: resp.EstimatedTimeSeconds,
	}, nil
}

// RouteShip initiates cross-system point-to-point travel
func (c *DaemonClient) RouteShip(
	ctx context.Context,
	shipSymbol, destination string,
	playerID int,
	agentSymbol string,
) (*RouteResponse, error) {
	req := &pb.RouteShipRequest{
		ShipSymbol:  shipSymbol,
		Destination: destination,
		PlayerId:    int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.RouteShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &RouteResponse{
		ContainerID: resp.ContainerId,
		ShipSymbol:  resp.ShipSymbol,
		Destination: resp.Destination,
		Status:      resp.Status,
	}, nil
}

// WarpShip initiates an off-gate warp. The daemon performs the warp behind its
// fail-closed guards (RULING #3: the CLI never touches the game API itself).
func (c *DaemonClient) WarpShip(
	ctx context.Context,
	shipSymbol, destination string,
	playerID int,
	agentSymbol string,
) (*WarpResponse, error) {
	req := &pb.WarpShipRequest{
		ShipSymbol:  shipSymbol,
		Destination: destination,
		PlayerId:    int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.WarpShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &WarpResponse{
		ContainerID: resp.ContainerId,
		ShipSymbol:  resp.ShipSymbol,
		Destination: resp.Destination,
		Status:      resp.Status,
	}, nil
}

// DockShip initiates ship docking
func (c *DaemonClient) DockShip(
	ctx context.Context,
	shipSymbol string,
	playerID int,
	agentSymbol string,
) (*DockResponse, error) {
	req := &pb.DockShipRequest{
		ShipSymbol: shipSymbol,
		PlayerId:   int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.DockShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &DockResponse{
		ContainerID: resp.ContainerId,
		ShipSymbol:  resp.ShipSymbol,
		Status:      resp.Status,
	}, nil
}

// OrbitShip initiates ship orbit
func (c *DaemonClient) OrbitShip(
	ctx context.Context,
	shipSymbol string,
	playerID int,
	agentSymbol string,
) (*OrbitResponse, error) {
	req := &pb.OrbitShipRequest{
		ShipSymbol: shipSymbol,
		PlayerId:   int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.OrbitShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &OrbitResponse{
		ContainerID: resp.ContainerId,
		ShipSymbol:  resp.ShipSymbol,
		Status:      resp.Status,
	}, nil
}

// RefuelShip initiates ship refuel
func (c *DaemonClient) RefuelShip(
	ctx context.Context,
	shipSymbol string,
	playerID int,
	agentSymbol string,
	units *int,
) (*RefuelResponse, error) {
	req := &pb.RefuelShipRequest{
		ShipSymbol: shipSymbol,
		PlayerId:   int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	if units != nil {
		u := int32(*units)
		req.Units = &u
	}

	resp, err := c.client.RefuelShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &RefuelResponse{
		ContainerID: resp.ContainerId,
		ShipSymbol:  resp.ShipSymbol,
		FuelAdded:   resp.FuelAdded,
		CreditsCost: resp.CreditsCost,
		Status:      resp.Status,
	}, nil
}

// JumpShip executes a jump to a different star system via jump gate
func (c *DaemonClient) JumpShip(
	ctx context.Context,
	shipSymbol string,
	destinationSystem string,
	playerID int,
	agentSymbol string,
) (*JumpResponse, error) {
	req := &pb.JumpShipRequest{
		ShipSymbol:        shipSymbol,
		DestinationSystem: destinationSystem,
		PlayerId:          int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.JumpShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &JumpResponse{
		Success:           resp.Success,
		NavigatedToGate:   resp.NavigatedToGate,
		JumpGateSymbol:    resp.JumpGateSymbol,
		DestinationSystem: resp.DestinationSystem,
		CooldownSeconds:   resp.CooldownSeconds,
		Message:           resp.Message,
		Error:             resp.Error,
	}, nil
}
