package player

import "context"

// PlayerRepository defines player persistence operations
type PlayerRepository interface {
	FindByID(ctx context.Context, playerID int) (*Player, error)
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
