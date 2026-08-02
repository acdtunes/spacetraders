package cli

import (
	"context"
	"fmt"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// ContainerInfo mirrors the protobuf ContainerInfo message for CLI display.
// This struct includes all fields needed for user-facing container information.
// Note: PlayerID is int32 per protobuf requirements (converted from domain int).
type ContainerInfo struct {
	ContainerID      string
	ContainerType    string
	Status           string
	PlayerID         int32 // Protobuf int32 (convert from domain int)
	CreatedAt        string
	UpdatedAt        string
	CurrentIteration int32
	MaxIterations    int32
	RestartCount     int32
	Metadata         string
}

type StopContainerResponse struct {
	ContainerID string
	Status      string
	Message     string
}

type LogEntry struct {
	Timestamp string
	Level     string
	Message   string
	Metadata  string
}

// ListContainers lists all containers
func (c *DaemonClient) ListContainers(
	ctx context.Context,
	playerID *int,
	status *string,
) ([]*ContainerInfo, error) {
	req := &pb.ListContainersRequest{}
	if playerID != nil {
		p := int32(*playerID)
		req.PlayerId = &p
	}
	if status != nil {
		req.Status = status
	}

	resp, err := c.client.ListContainers(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	containers := make([]*ContainerInfo, 0, len(resp.Containers))
	for _, pbCont := range resp.Containers {
		containers = append(containers, &ContainerInfo{
			ContainerID:      pbCont.ContainerId,
			ContainerType:    pbCont.ContainerType,
			Status:           pbCont.Status,
			PlayerID:         pbCont.PlayerId,
			CreatedAt:        pbCont.CreatedAt,
			UpdatedAt:        pbCont.UpdatedAt,
			CurrentIteration: pbCont.CurrentIteration,
			MaxIterations:    pbCont.MaxIterations,
			RestartCount:     pbCont.RestartCount,
		})
	}

	return containers, nil
}

// GetContainer retrieves container details
func (c *DaemonClient) GetContainer(
	ctx context.Context,
	containerID string,
) (*ContainerInfo, error) {
	req := &pb.GetContainerRequest{
		ContainerId: containerID,
	}

	resp, err := c.client.GetContainer(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	pbCont := resp.Container
	return &ContainerInfo{
		ContainerID:      pbCont.ContainerId,
		ContainerType:    pbCont.ContainerType,
		Status:           pbCont.Status,
		PlayerID:         pbCont.PlayerId,
		CreatedAt:        pbCont.CreatedAt,
		UpdatedAt:        pbCont.UpdatedAt,
		CurrentIteration: pbCont.CurrentIteration,
		MaxIterations:    pbCont.MaxIterations,
		RestartCount:     pbCont.RestartCount,
		Metadata:         resp.Metadata,
	}, nil
}

// StopContainer stops a container
func (c *DaemonClient) StopContainer(
	ctx context.Context,
	containerID string,
) (*StopContainerResponse, error) {
	req := &pb.StopContainerRequest{
		ContainerId: containerID,
	}

	resp, err := c.client.StopContainer(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &StopContainerResponse{
		ContainerID: resp.ContainerId,
		Status:      resp.Status,
		Message:     resp.Message,
	}, nil
}

// GetContainerLogs retrieves container logs
func (c *DaemonClient) GetContainerLogs(
	ctx context.Context,
	containerID string,
	limit *int,
	level *string,
) ([]*LogEntry, error) {
	req := &pb.GetContainerLogsRequest{
		ContainerId: containerID,
	}
	if limit != nil {
		l := int32(*limit)
		req.Limit = &l
	}
	if level != nil {
		req.Level = level
	}

	resp, err := c.client.GetContainerLogs(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	logs := make([]*LogEntry, 0, len(resp.Logs))
	for _, pbLog := range resp.Logs {
		logs = append(logs, &LogEntry{
			Timestamp: pbLog.Timestamp,
			Level:     pbLog.Level,
			Message:   pbLog.Message,
			Metadata:  pbLog.Metadata,
		})
	}

	return logs, nil
}

// TuneContainerConfig sets (or, with value 0, reverts) one live knob on a running
// container's persisted config, with no container restart (sp-vwek).
func (c *DaemonClient) TuneContainerConfig(ctx context.Context, containerID, operation, key string, value int64, playerIdent *PlayerIdentifier) (*pb.TuneContainerConfigResponse, error) {
	playerID, agentSymbol := playerPointers(playerIdent)
	req := &pb.TuneContainerConfigRequest{
		ContainerId: containerID,
		Operation:   operation,
		Key:         key,
		Value:       value,
		PlayerId:    playerID,
		AgentSymbol: agentSymbol,
	}

	resp, err := c.client.TuneContainerConfig(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// ShowTunableConfig lists a running container's live-tunable knobs with their
// effective values, sources, and bounds (sp-vwek `tune --show`).
func (c *DaemonClient) ShowTunableConfig(ctx context.Context, containerID, operation string, playerIdent *PlayerIdentifier) (*pb.ShowTunableConfigResponse, error) {
	playerID, agentSymbol := playerPointers(playerIdent)
	req := &pb.ShowTunableConfigRequest{
		ContainerId: containerID,
		Operation:   operation,
		PlayerId:    playerID,
		AgentSymbol: agentSymbol,
	}

	resp, err := c.client.ShowTunableConfig(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}
