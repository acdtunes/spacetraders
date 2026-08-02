package grpc

import (
	"context"
	"fmt"

	playerQuery "github.com/andrescamacho/spacetraders-go/internal/application/player/queries"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

const containerTimestampFormat = "2006-01-02T15:04:05Z"

// daemonServiceImpl implements the DaemonServiceServer interface
// It bridges gRPC requests to the DaemonServer business logic
type daemonServiceImpl struct {
	pb.UnimplementedDaemonServiceServer
	daemon *DaemonServer
}

// resolvePlayerID resolves a player_id from either the provided player_id or agent_symbol
// Priority: player_id > agent_symbol
// Returns an error if both are missing or if agent_symbol lookup fails
func (s *daemonServiceImpl) resolvePlayerID(ctx context.Context, playerID int32, agentSymbol *string) (int, error) {
	// If player_id is provided and non-zero, use it directly
	if playerID != 0 {
		return int(playerID), nil
	}

	// If agent_symbol is provided, resolve it to player_id
	if agentSymbol != nil && *agentSymbol != "" {
		response, err := s.daemon.mediator.Send(ctx, &playerQuery.GetPlayerQuery{
			AgentSymbol: *agentSymbol,
		})
		if err != nil {
			return 0, fmt.Errorf("failed to resolve agent symbol %s to player_id: %w", *agentSymbol, err)
		}

		getPlayerResp, ok := response.(*playerQuery.GetPlayerResponse)
		if !ok {
			return 0, fmt.Errorf("unexpected response type from GetPlayerQuery")
		}

		return getPlayerResp.Player.ID.Value(), nil
	}

	// Neither player_id nor agent_symbol provided
	return 0, fmt.Errorf("either player_id or agent_symbol must be provided")
}

// newDaemonServiceImpl creates a new gRPC service implementation
func newDaemonServiceImpl(daemon *DaemonServer) *daemonServiceImpl {
	return &daemonServiceImpl{
		daemon: daemon,
	}
}

// NewDaemonServiceImpl creates a new gRPC service implementation (exported for testing)
func NewDaemonServiceImpl(daemon *DaemonServer) pb.DaemonServiceServer {
	return newDaemonServiceImpl(daemon)
}
