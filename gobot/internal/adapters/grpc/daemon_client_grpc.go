package grpc

import (
	"context"
	"fmt"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// DaemonClientGRPC implements daemon.DaemonClient using gRPC
// This adapter connects application layer to the daemon gRPC service
type DaemonClientGRPC struct {
	conn   *grpc.ClientConn
	client pb.DaemonServiceClient
}

// NewDaemonClientGRPC creates a new gRPC daemon client
// socketPath should be a Unix domain socket path (e.g., "/tmp/spacetraders-daemon.sock")
func NewDaemonClientGRPC(socketPath string) (*DaemonClientGRPC, error) {
	// Connect to Unix socket via gRPC
	conn, err := grpc.NewClient(
		"unix:"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to daemon socket: %w", err)
	}

	client := pb.NewDaemonServiceClient(conn)

	return &DaemonClientGRPC{
		conn:   conn,
		client: client,
	}, nil
}

// Close closes the gRPC connection
func (c *DaemonClientGRPC) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// ListContainers retrieves all containers for a player
func (c *DaemonClientGRPC) ListContainers(ctx context.Context, playerID uint) ([]daemon.ContainerInfo, error) {
	req := &pb.ListContainersRequest{
		PlayerId: intPtr(ToProtobufPlayerID(int(playerID))),
	}

	resp, err := c.client.ListContainers(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	containers := make([]daemon.ContainerInfo, 0, len(resp.Containers))
	for _, pbCont := range resp.Containers {
		containers = append(containers, daemon.ContainerInfo{
			ID:       pbCont.ContainerId,
			PlayerID: FromProtobufPlayerID(pbCont.PlayerId),
			Status:   pbCont.Status,
			Type:     pbCont.ContainerType,
		})
	}

	return containers, nil
}

// CreateScoutTourContainer creates a background container for scout tour operations
func (c *DaemonClientGRPC) CreateScoutTourContainer(
	ctx context.Context,
	containerID string,
	playerID uint,
	command interface{},
) error {
	// Type assert to ScoutTourCommand
	cmd, ok := command.(*scoutingCmd.ScoutTourCommand)
	if !ok {
		return fmt.Errorf("invalid command type: expected *scoutingCmd.ScoutTourCommand, got %T", command)
	}

	req := &pb.ScoutTourRequest{
		ShipSymbol: cmd.ShipSymbol,
		Markets:    cmd.Markets,
		Iterations: int32(cmd.Iterations),
		PlayerId:   ToProtobufPlayerID(int(playerID)),
	}

	// Note: The daemon server handles container ID generation internally
	// We ignore the containerID parameter for now (server generates its own)
	_, err := c.client.ScoutTour(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to create scout tour container: %w", err)
	}

	return nil
}

// CreateContractWorkflowContainer creates AND STARTS a background container for contract workflow operations
func (c *DaemonClientGRPC) CreateContractWorkflowContainer(
	ctx context.Context,
	containerID string,
	playerID uint,
	command interface{},
	completionCallback chan<- string,
) error {
	// Type assert to ContractWorkflowCommand
	_, ok := command.(*contractCmd.RunWorkflowCommand)
	if !ok {
		return fmt.Errorf("invalid command type: expected *contractCmd.RunWorkflowCommand, got %T", command)
	}

	// This method is a placeholder - gRPC implementation would send the command to the daemon
	// For now, we don't support creating contract workflow containers via gRPC
	// (This would require adding protobuf message and RPC method)
	return fmt.Errorf("CreateContractWorkflowContainer not implemented for gRPC client")
}

// PersistContractWorkflowContainer creates (but does NOT start) a worker container in DB
func (c *DaemonClientGRPC) PersistContractWorkflowContainer(
	ctx context.Context,
	containerID string,
	playerID uint,
	command interface{},
) error {
	return fmt.Errorf("PersistContractWorkflowContainer not implemented for gRPC client")
}

// StartContractWorkflowContainer starts a previously persisted worker container
func (c *DaemonClientGRPC) StartContractWorkflowContainer(
	ctx context.Context,
	containerID string,
	completionCallback chan<- string,
) error {
	return fmt.Errorf("StartContractWorkflowContainer not implemented for gRPC client")
}

// StopContainer stops a running container
func (c *DaemonClientGRPC) StopContainer(ctx context.Context, containerID string) error {
	req := &pb.StopContainerRequest{
		ContainerId: containerID,
	}

	_, err := c.client.StopContainer(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to stop container: %w", err)
	}

	return nil
}

// Helper function to create int32 pointer
func intPtr(val int32) *int32 {
	return &val
}
