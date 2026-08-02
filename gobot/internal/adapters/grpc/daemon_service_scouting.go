package grpc

import (
	"context"
	"fmt"
	"time"

	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// AddScoutPost adds or updates a desired-state scout post (sp-cxpq)
func (s *daemonServiceImpl) AddScoutPost(ctx context.Context, req *pb.AddScoutPostRequest) (*pb.ScoutPostResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	kind := domainScouting.PostKind(req.Kind)
	if req.Kind == "" {
		kind = domainScouting.PostKindStanding
	}
	freshness := time.Duration(req.FreshnessSeconds) * time.Second

	post, err := s.daemon.AddScoutPost(ctx, playerID, req.SystemSymbol, freshness, kind, int(req.Hulls))
	if err != nil {
		return nil, fmt.Errorf("failed to add scout post: %w", err)
	}

	return &pb.ScoutPostResponse{Post: scoutPostToProto(post)}, nil
}

// RemoveScoutPost removes a scout post and releases its hull (sp-cxpq)
func (s *daemonServiceImpl) RemoveScoutPost(ctx context.Context, req *pb.RemoveScoutPostRequest) (*pb.RemoveScoutPostResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	if err := s.daemon.RemoveScoutPost(ctx, playerID, req.SystemSymbol); err != nil {
		return nil, fmt.Errorf("failed to remove scout post: %w", err)
	}

	return &pb.RemoveScoutPostResponse{Status: "REMOVED"}, nil
}

// ListScoutPosts returns the active scout posts for a player (sp-cxpq)
func (s *daemonServiceImpl) ListScoutPosts(ctx context.Context, req *pb.ListScoutPostsRequest) (*pb.ListScoutPostsResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	posts, err := s.daemon.ListScoutPosts(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to list scout posts: %w", err)
	}

	protoPosts := make([]*pb.ScoutPost, len(posts))
	for i, p := range posts {
		protoPosts[i] = scoutPostToProto(p)
	}
	return &pb.ListScoutPostsResponse{Posts: protoPosts}, nil
}

// scoutPostToProto maps a domain scout post to its wire representation. hulls is the
// probe budget and manned_count how many of those slots currently have a hull.
func scoutPostToProto(p *domainScouting.ScoutPost) *pb.ScoutPost {
	return &pb.ScoutPost{
		SystemSymbol:     p.SystemSymbol,
		FreshnessSeconds: int32(p.FreshnessTarget.Seconds()),
		Kind:             string(p.Kind),
		AssignedHull:     p.AssignedHull,
		TourContainerId:  p.TourContainerID,
		Hulls:            int32(p.HullBudget()),
		MannedCount:      int32(p.MannedCount()),
	}
}

// ScoutMarkets orchestrates fleet deployment for market scouting (multi-ship with VRP)
func (s *daemonServiceImpl) ScoutMarkets(ctx context.Context, req *pb.ScoutMarketsRequest) (*pb.ScoutMarketsResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	containerIDs, assignments, reusedContainers, err := s.daemon.ScoutMarkets(
		ctx,
		req.ShipSymbols,
		req.SystemSymbol,
		req.Markets,
		int(req.Iterations),
		playerID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to start scout markets: %w", err)
	}

	pbAssignments := make(map[string]*pb.MarketAssignment)
	for ship, markets := range assignments {
		pbAssignments[ship] = &pb.MarketAssignment{
			Markets: markets,
		}
	}

	response := &pb.ScoutMarketsResponse{
		ContainerIds:     containerIDs,
		Assignments:      pbAssignments,
		ReusedContainers: reusedContainers,
	}

	return response, nil
}

// AssignScoutingFleet creates a fleet-assignment container for async VRP optimization
func (s *daemonServiceImpl) AssignScoutingFleet(ctx context.Context, req *pb.AssignScoutingFleetRequest) (*pb.AssignScoutingFleetResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	// Create fleet-assignment container (returns immediately)
	containerID, err := s.daemon.AssignScoutingFleet(
		ctx,
		req.SystemSymbol,
		playerID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create fleet assignment container: %w", err)
	}

	response := &pb.AssignScoutingFleetResponse{
		ContainerId: containerID,
	}

	return response, nil
}

// SensingRescreen re-opens every sensing system verdict for a player — the supported
// response to editing config.yaml's [sensing] goods_whitelist mid-era. It resolves the player and
// delegates the write to the daemon, the single writer (RULINGS #3). The write is confined to
// sensing_systems.verdict, so running it against a fleet of parked probes cannot disturb a hull the
// probe cap is counting (RULINGS #4).
func (s *daemonServiceImpl) SensingRescreen(ctx context.Context, req *pb.SensingRescreenRequest) (*pb.SensingRescreenResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	result, err := s.daemon.RescreenSensing(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to rescreen sensing verdicts: %w", err)
	}
	return &pb.SensingRescreenResponse{SystemsReopened: result.SystemsReopened}, nil
}
