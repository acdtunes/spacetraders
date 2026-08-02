package cli

import (
	"context"
	"fmt"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

type ScoutTourResponse struct {
	ContainerID string
	ShipSymbol  string
	Markets     []string
	Iterations  int
	Status      string
}

type ScoutMarketsResponse struct {
	ContainerIDs     []string
	Assignments      map[string]*MarketAssignment
	ReusedContainers []string
}

type MarketAssignment struct {
	Markets []string
}

// ScoutPost mirrors the protobuf ScoutPost message for CLI display (sp-cxpq). Hulls
// is the probe budget N and MannedCount how many of those slots have a hull.
type ScoutPost struct {
	SystemSymbol     string
	FreshnessSeconds int
	Kind             string
	AssignedHull     string
	TourContainerID  string
	Hulls            int
	MannedCount      int
}

// AssignScoutingFleetResponse contains the fleet-assignment container ID
type AssignScoutingFleetResponse struct {
	ContainerID string
}

// ScoutTour initiates market scouting tour (single ship)
func (c *DaemonClient) ScoutTour(
	ctx context.Context,
	shipSymbol string,
	markets []string,
	iterations int,
	playerID int,
	agentSymbol string,
) (*ScoutTourResponse, error) {
	req := &pb.ScoutTourRequest{
		ShipSymbol: shipSymbol,
		Markets:    markets,
		Iterations: int32(iterations),
		PlayerId:   int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.ScoutTour(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &ScoutTourResponse{
		ContainerID: resp.ContainerId,
		ShipSymbol:  resp.ShipSymbol,
		Markets:     resp.Markets,
		Iterations:  int(resp.Iterations),
		Status:      resp.Status,
	}, nil
}

// ScoutMarkets initiates fleet market scouting with VRP optimization (multi-ship)
func (c *DaemonClient) ScoutMarkets(
	ctx context.Context,
	shipSymbols []string,
	systemSymbol string,
	markets []string,
	iterations int,
	playerID int,
	agentSymbol string,
) (*ScoutMarketsResponse, error) {
	req := &pb.ScoutMarketsRequest{
		ShipSymbols:  shipSymbols,
		SystemSymbol: systemSymbol,
		Markets:      markets,
		Iterations:   int32(iterations),
		PlayerId:     int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.ScoutMarkets(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	assignments := make(map[string]*MarketAssignment)
	for ship, pbAssignment := range resp.Assignments {
		assignments[ship] = &MarketAssignment{
			Markets: pbAssignment.Markets,
		}
	}

	return &ScoutMarketsResponse{
		ContainerIDs:     resp.ContainerIds,
		Assignments:      assignments,
		ReusedContainers: resp.ReusedContainers,
	}, nil
}

// AssignScoutingFleet creates a fleet-assignment container for async VRP optimization
func (c *DaemonClient) AssignScoutingFleet(
	ctx context.Context,
	systemSymbol string,
	playerID int,
	agentSymbol string,
) (*AssignScoutingFleetResponse, error) {
	req := &pb.AssignScoutingFleetRequest{
		SystemSymbol: systemSymbol,
		PlayerId:     int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.AssignScoutingFleet(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &AssignScoutingFleetResponse{
		ContainerID: resp.ContainerId,
	}, nil
}

// SensingRescreen re-opens every sensing system verdict for a player,
// so the steady-state sweep re-judges them under the CURRENT goods whitelist.
func (c *DaemonClient) SensingRescreen(ctx context.Context, playerIdent *PlayerIdentifier) (*pb.SensingRescreenResponse, error) {
	playerID, agentSymbol := playerPointers(playerIdent)
	var pid int32
	if playerID != nil {
		pid = *playerID
	}
	resp, err := c.client.SensingRescreen(ctx, &pb.SensingRescreenRequest{
		PlayerId:    pid,
		AgentSymbol: agentSymbol,
	})
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}
	return resp, nil
}

// AddScoutPost adds or updates a desired-state scout post (sp-cxpq). hulls is the
// probe budget N; 0 defaults to single-hull.
func (c *DaemonClient) AddScoutPost(ctx context.Context, playerID int, agentSymbol, systemSymbol string, freshnessSeconds int, kind string, hulls int) (*ScoutPost, error) {
	req := &pb.AddScoutPostRequest{
		PlayerId:         int32(playerID),
		SystemSymbol:     systemSymbol,
		FreshnessSeconds: int32(freshnessSeconds),
		Kind:             kind,
		Hulls:            int32(hulls),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	resp, err := c.client.AddScoutPost(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}
	return protoToScoutPost(resp.Post), nil
}

// RemoveScoutPost removes a scout post (sp-cxpq).
func (c *DaemonClient) RemoveScoutPost(ctx context.Context, playerID int, agentSymbol, systemSymbol string) error {
	req := &pb.RemoveScoutPostRequest{
		PlayerId:     int32(playerID),
		SystemSymbol: systemSymbol,
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	if _, err := c.client.RemoveScoutPost(ctx, req); err != nil {
		return fmt.Errorf(grpcCallFailed, err)
	}
	return nil
}

// ListScoutPosts lists the active scout posts (sp-cxpq).
func (c *DaemonClient) ListScoutPosts(ctx context.Context, playerID int, agentSymbol string) ([]*ScoutPost, error) {
	req := &pb.ListScoutPostsRequest{PlayerId: int32(playerID)}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	resp, err := c.client.ListScoutPosts(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}
	posts := make([]*ScoutPost, len(resp.Posts))
	for i, p := range resp.Posts {
		posts[i] = protoToScoutPost(p)
	}
	return posts, nil
}

func protoToScoutPost(p *pb.ScoutPost) *ScoutPost {
	if p == nil {
		return nil
	}
	return &ScoutPost{
		SystemSymbol:     p.SystemSymbol,
		FreshnessSeconds: int(p.FreshnessSeconds),
		Kind:             p.Kind,
		AssignedHull:     p.AssignedHull,
		TourContainerID:  p.TourContainerId,
		Hulls:            int(p.Hulls),
		MannedCount:      int(p.MannedCount),
	}
}
