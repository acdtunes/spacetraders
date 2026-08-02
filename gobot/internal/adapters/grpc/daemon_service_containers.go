package grpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// ListContainers returns all containers
func (s *daemonServiceImpl) ListContainers(ctx context.Context, req *pb.ListContainersRequest) (*pb.ListContainersResponse, error) {
	// Handle optional filters
	var playerID *int
	if req.PlayerId != nil {
		p := FromProtobufPlayerID(*req.PlayerId)
		playerID = &p
	}

	// Apply status filter with smart defaults
	var status *string
	if req.Status != nil && *req.Status != "" {
		// User explicitly requested a status - use as-is
		status = req.Status
	} else {
		// DEFAULT: Only show active containers (RUNNING, INTERRUPTED)
		// Rationale: Operators care about what's currently active, not history
		// Use comma-separated list for multiple statuses
		defaultStatuses := "RUNNING,INTERRUPTED"
		status = &defaultStatuses
	}

	containers := s.daemon.ListContainers(playerID, status)

	pbContainers := make([]*pb.ContainerInfo, 0, len(containers))
	for _, cont := range containers {
		var parentID *string
		if cont.ParentContainerID() != nil {
			parentID = cont.ParentContainerID()
		}

		pbContainers = append(pbContainers, &pb.ContainerInfo{
			ContainerId:       cont.ID(),
			ContainerType:     string(cont.Type()),
			Status:            string(cont.Status()),
			PlayerId:          ToProtobufPlayerID(cont.PlayerID()),
			ParentContainerId: parentID,
			CreatedAt:         cont.CreatedAt().Format(containerTimestampFormat),
			UpdatedAt:         cont.UpdatedAt().Format(containerTimestampFormat),
			CurrentIteration:  int32(cont.CurrentIteration()),
			MaxIterations:     int32(cont.MaxIterations()),
			RestartCount:      int32(cont.RestartCount()),
		})
	}

	return &pb.ListContainersResponse{
		Containers: pbContainers,
	}, nil
}

// GetContainer retrieves container details
func (s *daemonServiceImpl) GetContainer(ctx context.Context, req *pb.GetContainerRequest) (*pb.GetContainerResponse, error) {
	container, err := s.daemon.GetContainer(req.ContainerId)
	if err != nil {
		return nil, fmt.Errorf("failed to get container: %w", err)
	}

	// Source the displayed config from Store A — the persisted ContainerModel.Config —
	// NOT the in-memory entity's Metadata(), which NewContainer freezes at launch. A live
	// config mutation (UpdateContainerConfig via `fleet hub`) rewrites only the persisted
	// config, so serializing Metadata() here made live changes invisible until a daemon
	// restart (sp-aoy2). The runtime/lifecycle fields below still come from the live
	// in-memory entity; only the config JSON is re-sourced from the DB.
	metadataJSON, found, err := s.daemon.PersistedContainerConfig(ctx, container.ID(), container.PlayerID())
	if err != nil {
		return nil, fmt.Errorf("failed to read container config: %w", err)
	}
	if !found {
		// No persisted row (e.g. an ephemeral container never written to the DB) — fall
		// back to the in-memory launch metadata so `container get` still returns its config
		// rather than an empty string.
		fallback, merr := json.Marshal(container.Metadata())
		if merr != nil {
			return nil, fmt.Errorf("failed to serialize metadata: %w", merr)
		}
		metadataJSON = string(fallback)
	}

	pbContainer := &pb.ContainerInfo{
		ContainerId:      container.ID(),
		ContainerType:    string(container.Type()),
		Status:           string(container.Status()),
		PlayerId:         ToProtobufPlayerID(container.PlayerID()),
		CreatedAt:        container.CreatedAt().Format(containerTimestampFormat),
		UpdatedAt:        container.UpdatedAt().Format(containerTimestampFormat),
		CurrentIteration: int32(container.CurrentIteration()),
		MaxIterations:    int32(container.MaxIterations()),
		RestartCount:     int32(container.RestartCount()),
	}

	return &pb.GetContainerResponse{
		Container: pbContainer,
		Metadata:  metadataJSON,
	}, nil
}

// StopContainer stops a container
func (s *daemonServiceImpl) StopContainer(ctx context.Context, req *pb.StopContainerRequest) (*pb.StopContainerResponse, error) {
	err := s.daemon.StopContainer(req.ContainerId)
	if err != nil {
		return nil, fmt.Errorf("failed to stop container: %w", err)
	}

	return &pb.StopContainerResponse{
		ContainerId: req.ContainerId,
		Status:      "STOPPED",
		Message:     "Container stopped successfully",
	}, nil
}

// GetContainerLogs retrieves container logs
func (s *daemonServiceImpl) GetContainerLogs(ctx context.Context, req *pb.GetContainerLogsRequest) (*pb.GetContainerLogsResponse, error) {
	// TODO: Implement log retrieval when logging infrastructure is wired
	// For now, return empty logs
	return &pb.GetContainerLogsResponse{
		Logs: []*pb.LogEntry{},
	}, nil
}

// HealthCheck verifies daemon health
func (s *daemonServiceImpl) HealthCheck(ctx context.Context, req *pb.HealthCheckRequest) (*pb.HealthCheckResponse, error) {
	containers := s.daemon.ListContainers(nil, nil)
	activeCount := 0
	for _, cont := range containers {
		if cont.Status() == "RUNNING" {
			activeCount++
		}
	}

	return &pb.HealthCheckResponse{
		Status:           "ok",
		Version:          "0.1.0",
		ActiveContainers: int32(activeCount),
	}, nil
}

// GetAPIBudget returns API request-budget observability: per-hull
// req/s, global utilization vs the rate ceiling (429 rate, poll-cadence
// share of the budget, headroom), and the duty-cycle KPI (ship-hours
// earning/day per hull). Reads the daemon-wide singletons set at startup
// (metrics.GetGlobalAPIBudgetTracker, metrics.GetGlobalDutyCycleSampler);
// both trackers are nil-safe on Report(), so a metrics-disabled or
// not-yet-warmed-up daemon returns a zero-value report instead of erroring.
func (s *daemonServiceImpl) GetAPIBudget(ctx context.Context, req *pb.GetAPIBudgetRequest) (*pb.GetAPIBudgetResponse, error) {
	dualReport := metrics.GetGlobalAPIBudgetTracker().Report()
	dutyCycleReport := metrics.GetGlobalDutyCycleSampler().Report()

	return &pb.GetAPIBudgetResponse{
		Current:    apiBudgetReportToProto(dualReport.Current),
		Rolling_5M: apiBudgetReportToProto(dualReport.Rolling5m),
		DutyCycle:  dutyCycleReportToProto(dutyCycleReport),
	}, nil
}

// TuneContainerConfig sets (or reverts, value 0) one live knob on a running
// container (sp-vwek). Resolves the player from player_id or agent_symbol like the
// other coordinator RPCs, then delegates the registry-validated, persisted-config
// mutation to the daemon — the single writer (RULINGS #3). The running coordinator
// re-reads its config at each tick start, so the tune lands on the next tick with
// no restart.
func (s *daemonServiceImpl) TuneContainerConfig(ctx context.Context, req *pb.TuneContainerConfigRequest) (*pb.TuneContainerConfigResponse, error) {
	var pid int32
	if req.PlayerId != nil {
		pid = *req.PlayerId
	}
	playerID, err := s.resolvePlayerID(ctx, pid, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	out, err := s.daemon.MutateContainerConfigKey(ctx, req.ContainerId, req.Operation, req.Key, int(req.Value), playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to tune container config: %w", err)
	}

	return &pb.TuneContainerConfigResponse{
		ContainerId:   out.ContainerID,
		ContainerType: out.ContainerType,
		Key:           out.Key,
		OldEffective:  int64(out.OldEffective),
		OldSource:     out.OldSource,
		NewEffective:  int64(out.NewEffective),
		NewSource:     out.NewSource,
		Unit:          out.Unit,
		DefaultValue:  int64(out.DefaultValue),
		Changed:       out.Changed,
	}, nil
}

// ShowTunableConfig lists a container's live-tunable knobs with effective values,
// sources, and bounds (sp-vwek `tune --show`).
func (s *daemonServiceImpl) ShowTunableConfig(ctx context.Context, req *pb.ShowTunableConfigRequest) (*pb.ShowTunableConfigResponse, error) {
	var pid int32
	if req.PlayerId != nil {
		pid = *req.PlayerId
	}
	playerID, err := s.resolvePlayerID(ctx, pid, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	out, err := s.daemon.ShowTunableConfig(ctx, req.ContainerId, req.Operation, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to list tunable knobs: %w", err)
	}

	resp := &pb.ShowTunableConfigResponse{
		ContainerId:   out.ContainerID,
		ContainerType: out.ContainerType,
	}
	for _, k := range out.Knobs {
		resp.Knobs = append(resp.Knobs, &pb.TunableKnobStatus{
			Key:          k.Key,
			Effective:    int64(k.Effective),
			Source:       k.Source,
			Min:          int64(k.Bound.Min),
			Max:          int64(k.Bound.Max),
			Unit:         k.Bound.Unit,
			Description:  k.Bound.Description,
			DefaultValue: int64(k.Bound.Default),
		})
	}
	return resp, nil
}
