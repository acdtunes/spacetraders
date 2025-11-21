package common

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// PlayerResolver provides unified logic for resolving player IDs.
//
// This utility eliminates duplication across multiple query handlers that need to
// resolve a player by either a numeric player ID or an agent symbol string.
//
// The resolver supports two resolution modes:
//  1. Direct ID resolution: When playerID is provided, validate and convert it
//  2. Symbol-based resolution: When agentSymbol is provided, lookup player in repository
//
// Business rules:
//   - At least one of playerID or agentSymbol must be provided
//   - If both are provided, playerID takes precedence
//   - Returns error if neither is provided or if lookup fails
type PlayerResolver struct {
	playerRepo player.PlayerRepository
}

// NewPlayerResolver creates a new player resolver with required dependencies.
func NewPlayerResolver(playerRepo player.PlayerRepository) *PlayerResolver {
	return &PlayerResolver{
		playerRepo: playerRepo,
	}
}

// ResolvePlayerID resolves a player ID from either a numeric ID or agent symbol.
//
// Resolution logic:
//  1. If playerID is provided, validate and return it
//  2. If agentSymbol is provided, lookup player by symbol
//  3. Return error if neither is provided
//
// This method eliminates the need for each handler to implement its own resolution logic.
//
// Example usage:
//
//	resolver := NewPlayerResolver(playerRepo)
//	playerID, err := resolver.ResolvePlayerID(ctx, cmd.PlayerID, cmd.AgentSymbol)
//	if err != nil {
//	    return nil, err
//	}
func (r *PlayerResolver) ResolvePlayerID(ctx context.Context, playerID *int, agentSymbol string) (shared.PlayerID, error) {
	// Validate that at least one identifier is provided
	if playerID == nil && agentSymbol == "" {
		return shared.PlayerID{}, fmt.Errorf("either player_id or agent_symbol must be provided")
	}

	// If numeric player ID is provided, validate and return it
	if playerID != nil {
		pid, err := shared.NewPlayerID(*playerID)
		if err != nil {
			return shared.PlayerID{}, fmt.Errorf("invalid player ID: %w", err)
		}
		return pid, nil
	}

	// Otherwise, resolve by agent symbol
	player, err := r.playerRepo.FindByAgentSymbol(ctx, agentSymbol)
	if err != nil {
		return shared.PlayerID{}, fmt.Errorf("failed to find player by agent symbol: %w", err)
	}

	return player.ID, nil
}
