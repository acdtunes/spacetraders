package player

// Player represents a SpaceTraders agent/player
type Player struct {
	ID              int
	AgentSymbol     string
	Token           string
	Credits         int
	StartingFaction string
	Metadata        map[string]interface{}
}

// NewPlayer creates a new player
func NewPlayer(id int, agentSymbol, token string) *Player {
	return &Player{
		ID:          id,
		AgentSymbol: agentSymbol,
		Token:       token,
		Metadata:    make(map[string]interface{}),
	}
}
