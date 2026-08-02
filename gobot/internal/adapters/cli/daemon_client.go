package cli

import (
	"context"
	"fmt"
	"time"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const grpcCallFailed = "gRPC call failed: %w"

// DaemonClient provides a client interface to communicate with the daemon via gRPC
type DaemonClient struct {
	conn       *grpc.ClientConn
	client     pb.DaemonServiceClient
	socketPath string
}

// NewDaemonClient creates a new daemon client
func NewDaemonClient(socketPath string) (*DaemonClient, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Unix domain sockets need the "unix:" scheme.
	conn, err := grpc.DialContext(
		ctx,
		"unix:"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to daemon socket: %w", err)
	}

	client := pb.NewDaemonServiceClient(conn)

	return &DaemonClient{
		conn:       conn,
		client:     client,
		socketPath: socketPath,
	}, nil
}

// Close closes the client connection
func (c *DaemonClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

type HealthResponse struct {
	Status           string
	Version          string
	ActiveContainers int32
}

// HealthCheck verifies daemon health
func (c *DaemonClient) HealthCheck(ctx context.Context) (*HealthResponse, error) {
	req := &pb.HealthCheckRequest{}

	resp, err := c.client.HealthCheck(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &HealthResponse{
		Status:           resp.Status,
		Version:          resp.Version,
		ActiveContainers: resp.ActiveContainers,
	}, nil
}

// GetAPIBudget retrieves API request-budget observability:
// per-hull req/s, global utilization vs the rate ceiling, and the
// duty-cycle KPI (ship-hours earning/day per hull).
func (c *DaemonClient) GetAPIBudget(ctx context.Context) (*pb.GetAPIBudgetResponse, error) {
	req := &pb.GetAPIBudgetRequest{}

	resp, err := c.client.GetAPIBudget(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}
