package grpc

import (
	"context"
	"fmt"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/application/scouting"
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
func (c *DaemonClientGRPC) ListContainers(ctx context.Context, playerID uint) ([]daemon.Container, error) {
	req := &pb.ListContainersRequest{
		PlayerId: intPtr(int32(playerID)),
	}

	resp, err := c.client.ListContainers(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	containers := make([]daemon.Container, 0, len(resp.Containers))
	for _, pbCont := range resp.Containers {
		containers = append(containers, daemon.Container{
			ID:       pbCont.ContainerId,
			PlayerID: uint(pbCont.PlayerId),
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
	cmd, ok := command.(*scouting.ScoutTourCommand)
	if !ok {
		return fmt.Errorf("invalid command type: expected *scouting.ScoutTourCommand, got %T", command)
	}

	req := &pb.ScoutTourRequest{
		ShipSymbol: cmd.ShipSymbol,
		Markets:    cmd.Markets,
		Iterations: int32(cmd.Iterations),
		PlayerId:   int32(playerID),
	}

	// Note: The daemon server handles container ID generation internally
	// We ignore the containerID parameter for now (server generates its own)
	_, err := c.client.ScoutTour(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to create scout tour container: %w", err)
	}

	return nil
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
