package player

import "github.com/andrescamacho/spacetraders-go/internal/domain/shared"

// Player represents a SpaceTraders agent/player
type Player struct {
	ID              shared.PlayerID
	AgentSymbol     string
	Token           string
	Credits         int
	StartingFaction string
	Metadata        map[string]interface{}
}

// NewPlayer creates a new player
func NewPlayer(id shared.PlayerID, agentSymbol, token string) *Player {
	return &Player{
		ID:          id,
		AgentSymbol: agentSymbol,
		Token:       token,
		Metadata:    make(map[string]interface{}),
	}
}
