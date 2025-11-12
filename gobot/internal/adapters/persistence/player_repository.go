package persistence

import (
	"context"
	"encoding/json"
	"fmt"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// GormPlayerRepository implements PlayerRepository using GORM
type GormPlayerRepository struct {
	db *gorm.DB
}

// NewGormPlayerRepository creates a new GORM player repository
func NewGormPlayerRepository(db *gorm.DB) *GormPlayerRepository {
	return &GormPlayerRepository{db: db}
}

// FindByID retrieves a player by ID
func (r *GormPlayerRepository) FindByID(ctx context.Context, playerID int) (*common.Player, error) {
	var model PlayerModel
	result := r.db.WithContext(ctx).Where("player_id = ?", playerID).First(&model)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("player not found: %d", playerID)
		}
		return nil, fmt.Errorf("failed to find player: %w", result.Error)
	}

	return r.modelToPlayer(&model)
}

// FindByAgentSymbol retrieves a player by agent symbol
func (r *GormPlayerRepository) FindByAgentSymbol(ctx context.Context, agentSymbol string) (*common.Player, error) {
	var model PlayerModel
	result := r.db.WithContext(ctx).Where("agent_symbol = ?", agentSymbol).First(&model)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("player not found: %s", agentSymbol)
		}
		return nil, fmt.Errorf("failed to find player: %w", result.Error)
	}

	return r.modelToPlayer(&model)
}

// Save persists a player
func (r *GormPlayerRepository) Save(ctx context.Context, player *common.Player) error {
	model, err := r.playerToModel(player)
	if err != nil {
		return fmt.Errorf("failed to convert player to model: %w", err)
	}

	// Upsert: create or update
	result := r.db.WithContext(ctx).Save(model)
	if result.Error != nil {
		return fmt.Errorf("failed to save player: %w", result.Error)
	}

	return nil
}

// modelToPlayer converts database model to domain DTO
// NOTE: Credits are NOT mapped from database - they're always fetched fresh from API
func (r *GormPlayerRepository) modelToPlayer(model *PlayerModel) (*common.Player, error) {
	var metadata map[string]interface{}
	if model.Metadata != "" {
		if err := json.Unmarshal([]byte(model.Metadata), &metadata); err != nil {
			// If unmarshal fails, leave metadata as nil
			metadata = nil
		}
	}

	return &common.Player{
		ID:          model.PlayerID,
		AgentSymbol: model.AgentSymbol,
		Token:       model.Token,
		Credits:     0, // Credits set to 0 - will be populated by handler from API
		Metadata:    metadata,
	}, nil
}

// playerToModel converts domain DTO to database model
// NOTE: Credits are NOT persisted to database - they're always fetched fresh from API
func (r *GormPlayerRepository) playerToModel(player *common.Player) (*PlayerModel, error) {
	var metadataJSON string
	if player.Metadata != nil {
		bytes, err := json.Marshal(player.Metadata)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal metadata: %w", err)
		}
		metadataJSON = string(bytes)
	}

	return &PlayerModel{
		PlayerID:    player.ID,
		AgentSymbol: player.AgentSymbol,
		Token:       player.Token,
		// Credits field intentionally omitted - not persisted to database
		Metadata:    metadataJSON,
	}, nil
}
