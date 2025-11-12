package player

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// PlayerSelectionOptions holds inputs for player selection
type PlayerSelectionOptions struct {
	PlayerIDFlag    *int   // --player-id flag (highest priority)
	AgentSymbolFlag string // --agent flag
	UserConfig      *config.UserConfig
}

// PlayerResolver resolves which player to use based on priority logic
type PlayerResolver struct {
	playerRepo common.PlayerRepository
}

// NewPlayerResolver creates a new PlayerResolver
func NewPlayerResolver(playerRepo common.PlayerRepository) *PlayerResolver {
	return &PlayerResolver{
		playerRepo: playerRepo,
	}
}

// ResolvePlayer resolves which player to use based on priority:
// 1. --player-id flag (highest priority)
// 2. --agent flag
// 3. config default player
// 4. auto-select if only one player exists
// 5. error if ambiguous
func (r *PlayerResolver) ResolvePlayer(ctx context.Context, opts *PlayerSelectionOptions) (*common.Player, error) {
	// Priority 1: --player-id flag
	if opts.PlayerIDFlag != nil {
		player, err := r.playerRepo.FindByID(ctx, *opts.PlayerIDFlag)
		if err != nil {
			return nil, fmt.Errorf("player with ID %d not found: %w", *opts.PlayerIDFlag, err)
		}
		return player, nil
	}

	// Priority 2: --agent flag
	if opts.AgentSymbolFlag != "" {
		player, err := r.playerRepo.FindByAgentSymbol(ctx, opts.AgentSymbolFlag)
		if err != nil {
			return nil, fmt.Errorf("player with agent symbol '%s' not found: %w", opts.AgentSymbolFlag, err)
		}
		return player, nil
	}

	// Priority 3: config default player
	if opts.UserConfig != nil {
		if opts.UserConfig.DefaultPlayerID != nil {
			player, err := r.playerRepo.FindByID(ctx, *opts.UserConfig.DefaultPlayerID)
			if err != nil {
				return nil, fmt.Errorf("default player (ID %d) not found: %w", *opts.UserConfig.DefaultPlayerID, err)
			}
			return player, nil
		}

		if opts.UserConfig.DefaultAgent != "" {
			player, err := r.playerRepo.FindByAgentSymbol(ctx, opts.UserConfig.DefaultAgent)
			if err != nil {
				return nil, fmt.Errorf("default player (agent '%s') not found: %w", opts.UserConfig.DefaultAgent, err)
			}
			return player, nil
		}
	}

	// Priority 4: Auto-select if only one player
	// TODO: Add ListAll method to PlayerRepository and implement auto-select
	// For now, return error
	return nil, fmt.Errorf("no player specified: use --player-id or --agent flag, or set default player with 'config set-player'")
}

// GetPlayerToken is a convenience method to resolve player and return token
func (r *PlayerResolver) GetPlayerToken(ctx context.Context, opts *PlayerSelectionOptions) (string, int, error) {
	player, err := r.ResolvePlayer(ctx, opts)
	if err != nil {
		return "", 0, err
	}
	return player.Token, player.ID, nil
}
