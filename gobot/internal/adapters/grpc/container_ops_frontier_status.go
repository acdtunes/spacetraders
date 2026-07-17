package grpc

import (
	"context"
	"fmt"

	expansionCmd "github.com/andrescamacho/spacetraders-go/internal/application/expansion/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// This file wires the sp-pvw3 `frontier status` read-only query to the daemon. The heavy lifting (the
// assembled view) lives on the frontier coordinator handler, which already holds every port it needs;
// the daemon just resolves the running frontier container and delegates.

// frontierStatusProvider is the narrow read-only slice of the frontier coordinator the daemon calls
// for `frontier status`. The RunFrontierExpansionCoordinatorHandler satisfies it. By construction it
// exposes ONLY the query — no reconcile/actuation — so the status RPC can never move a probe.
type frontierStatusProvider interface {
	Status(ctx context.Context, cmd *expansionCmd.RunFrontierExpansionCoordinatorCommand) (*expansionCmd.FrontierStatusView, error)
}

// SetFrontierStatusProvider injects the frontier coordinator's read-only status query (sp-pvw3). Wired
// in main.go after the coordinator is built; leaving it unset makes GetFrontierStatus report the
// coordinator unavailable rather than panicking.
func (s *DaemonServer) SetFrontierStatusProvider(provider frontierStatusProvider) {
	s.frontierStatus = provider
}

// GetFrontierStatus resolves the player's RUNNING frontier coordinator and returns its assembled live
// view plus the container id. It reuses resolveTunableContainer (the `--operation frontier` lookup,
// freshest-heartbeat row wins), so it errors clearly when no frontier coordinator is running. The view
// itself is assembled by the coordinator handler off its live-config snapshot, so status and the
// coordinator's actual behavior never disagree.
func (s *DaemonServer) GetFrontierStatus(ctx context.Context, playerID int) (*expansionCmd.FrontierStatusView, string, error) {
	if s.frontierStatus == nil {
		return nil, "", fmt.Errorf("frontier status is unavailable — the frontier coordinator is not wired on this daemon")
	}
	model, err := s.resolveTunableContainer(ctx, "", "frontier", playerID)
	if err != nil {
		return nil, "", err
	}
	cmd := &expansionCmd.RunFrontierExpansionCoordinatorCommand{
		PlayerID:    shared.MustNewPlayerID(playerID),
		ContainerID: model.ID,
	}
	view, err := s.frontierStatus.Status(ctx, cmd)
	if err != nil {
		return nil, "", fmt.Errorf("failed to assemble frontier status: %w", err)
	}
	return view, model.ID, nil
}

// GetFrontierStatus is the gRPC entry point (sp-pvw3): resolve the player, delegate to the daemon, and
// map the view to the wire response.
func (s *daemonServiceImpl) GetFrontierStatus(ctx context.Context, req *pb.GetFrontierStatusRequest) (*pb.GetFrontierStatusResponse, error) {
	var pid int32
	if req.PlayerId != nil {
		pid = *req.PlayerId
	}
	playerID, err := s.resolvePlayerID(ctx, pid, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	view, containerID, err := s.daemon.GetFrontierStatus(ctx, playerID)
	if err != nil {
		return nil, err
	}

	blockers := view.Blockers
	if blockers == nil {
		blockers = []string{}
	}
	return &pb.GetFrontierStatusResponse{
		ContainerId:       containerID,
		DiscoveryShare:    int32(view.DiscoveryShare),
		ScanShare:         int32(view.ScanShare),
		SplitSummary:      view.SplitSummary,
		VirginQueueDepth:  int32(view.VirginQueueDepth),
		DarkSystems:       int32(view.DarkSystems),
		DarkMarketplaces:  int32(view.DarkMarketplaces),
		ProbeFleet:        int32(view.ProbeFleet),
		ProbeCap:          int32(view.ProbeCap),
		ProbesIdle:        int32(view.ProbesIdle),
		PostsInFlight:     int32(view.PostsInFlight),
		LastBuyPrice:      int32(view.LastBuyPrice),
		LastBuyAgeSeconds: int32(view.LastBuyAgeSeconds),
		Blockers:          blockers,
	}, nil
}
