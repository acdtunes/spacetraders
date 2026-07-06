package cli

import (
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

func connectDaemon() (*DaemonClient, error) {
	client, err := NewDaemonClient(socketPath)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to daemon: %w", err)
	}
	return client, nil
}

// PlayerIdentifier holds player identification (either ID or agent symbol)
type PlayerIdentifier struct {
	PlayerID    int
	AgentSymbol string
}

// playerPointers converts a resolved identifier into the optional pointer
// arguments the daemon client expects.
func playerPointers(playerIdent *PlayerIdentifier) (*int32, *string) {
	var playerID *int32
	if playerIdent.PlayerID > 0 {
		pid := int32(playerIdent.PlayerID)
		playerID = &pid
	}

	var agentSymbol *string
	if playerIdent.AgentSymbol != "" {
		agentSymbol = &playerIdent.AgentSymbol
	}

	return playerID, agentSymbol
}

// resolvePlayerIdentifier resolves player identification from flags or defaults
// Priority: CLI flags (--player-id or --agent) > User config defaults
// Returns error only if no player can be identified from any source
func resolvePlayerIdentifier() (*PlayerIdentifier, error) {
	// If explicit flags provided, use them
	if playerID > 0 {
		return &PlayerIdentifier{PlayerID: playerID}, nil
	}
	if agentSymbol != "" {
		return &PlayerIdentifier{AgentSymbol: agentSymbol}, nil
	}

	// Try to load default player from user config
	userConfigHandler, err := config.NewUserConfigHandler()
	if err != nil {
		return nil, fmt.Errorf("no player specified and failed to load user config: %w", err)
	}

	userCfg, err := userConfigHandler.Load()
	if err != nil {
		return nil, fmt.Errorf("no player specified and failed to load user config: %w", err)
	}

	// Use default player from config
	if userCfg.DefaultPlayerID != nil {
		return &PlayerIdentifier{PlayerID: *userCfg.DefaultPlayerID}, nil
	}
	if userCfg.DefaultAgent != "" {
		return &PlayerIdentifier{AgentSymbol: userCfg.DefaultAgent}, nil
	}

	return nil, fmt.Errorf("no player specified: use --player-id or --agent, or set default with 'spacetraders config set-player'")
}
