package helpers

import (
	"context"
	"fmt"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// MockPlayerRepository is a test double for PlayerRepository interface
type MockPlayerRepository struct {
	mu      sync.RWMutex
	players map[int]*player.Player    // playerID -> player
	byAgent map[string]*player.Player // agentSymbol -> player
}

// NewMockPlayerRepository creates a new mock player repository
func NewMockPlayerRepository() *MockPlayerRepository {
	return &MockPlayerRepository{
		players: make(map[int]*player.Player),
		byAgent: make(map[string]*player.Player),
	}
}

// AddPlayer adds a player to the mock repository
func (m *MockPlayerRepository) AddPlayer(p *player.Player) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.players[p.ID.Value()] = p
	m.byAgent[p.AgentSymbol] = p
}

// FindByID retrieves a player by ID
func (m *MockPlayerRepository) FindByID(ctx context.Context, playerID shared.PlayerID) (*player.Player, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	p, ok := m.players[playerID.Value()]
	if !ok {
		return nil, fmt.Errorf("player not found: %d", playerID.Value())
	}

	return p, nil
}

// FindByAgentSymbol retrieves a player by agent symbol
func (m *MockPlayerRepository) FindByAgentSymbol(ctx context.Context, agentSymbol string) (*player.Player, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	p, ok := m.byAgent[agentSymbol]
	if !ok {
		return nil, fmt.Errorf("player not found: %s", agentSymbol)
	}

	return p, nil
}

// Save persists player state
func (m *MockPlayerRepository) Save(ctx context.Context, p *player.Player) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.players[p.ID.Value()] = p
	m.byAgent[p.AgentSymbol] = p
	return nil
}
