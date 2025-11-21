package player

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// PlayerRepository defines player persistence operations
type PlayerRepository interface {
	FindByID(ctx context.Context, playerID shared.PlayerID) (*Player, error)
	FindByAgentSymbol(ctx context.Context, agentSymbol string) (*Player, error)
	Add(ctx context.Context, player *Player) error
}

// DTOs for player operations

type AgentData struct {
	AccountID       string
	Symbol          string
	Headquarters    string
	Credits         int
	StartingFaction string
}
