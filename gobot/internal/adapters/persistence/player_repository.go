package persistence

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"gorm.io/gorm"
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
func (r *GormPlayerRepository) FindByID(ctx context.Context, playerID shared.PlayerID) (*player.Player, error) {
	var model PlayerModel
	result := r.db.WithContext(ctx).Where("id = ?", playerID.Value()).First(&model)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("player not found: %s", playerID.String())
		}
		return nil, fmt.Errorf("failed to find player: %w", result.Error)
	}

	return r.modelToPlayer(&model)
}

// FindByAgentSymbol retrieves a player by agent symbol
func (r *GormPlayerRepository) FindByAgentSymbol(ctx context.Context, agentSymbol string) (*player.Player, error) {
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

// ListAll retrieves all players from the database
func (r *GormPlayerRepository) ListAll(ctx context.Context) ([]*player.Player, error) {
	var models []PlayerModel
	result := r.db.WithContext(ctx).Find(&models)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to list players: %w", result.Error)
	}

	players := make([]*player.Player, 0, len(models))
	for _, model := range models {
		p, err := r.modelToPlayer(&model)
		if err != nil {
			continue // Skip invalid players
		}
		players = append(players, p)
	}

	return players, nil
}

// Add persists a player
func (r *GormPlayerRepository) Add(ctx context.Context, player *player.Player) error {
	model, err := r.playerToModel(player)
	if err != nil {
		return fmt.Errorf("failed to convert player to model: %w", err)
	}

	// Upsert: create or update
	result := r.db.WithContext(ctx).Save(model)
	if result.Error != nil {
		return fmt.Errorf("failed to add player: %w", result.Error)
	}

	return nil
}

// modelToPlayer converts database model to domain DTO
// NOTE: Credits are NOT mapped from database - they're always fetched fresh from API
func (r *GormPlayerRepository) modelToPlayer(model *PlayerModel) (*player.Player, error) {
	var metadata map[string]interface{}
	if model.Metadata != "" {
		if err := json.Unmarshal([]byte(model.Metadata), &metadata); err != nil {
			// If unmarshal fails, leave metadata as nil
			metadata = nil
		}
	}

	playerID, err := shared.NewPlayerID(model.ID)
	if err != nil {
		return nil, fmt.Errorf("invalid player ID in database: %w", err)
	}

	return &player.Player{
		ID:          playerID,
		AgentSymbol: model.AgentSymbol,
		Token:       model.Token,
		Credits:     0, // Credits set to 0 - will be populated by handler from API
		Metadata:    metadata,
	}, nil
}

// playerToModel converts domain DTO to database model
// NOTE: Credits are NOT persisted to database - they're always fetched fresh from API
func (r *GormPlayerRepository) playerToModel(player *player.Player) (*PlayerModel, error) {
	var metadataJSON string
	if player.Metadata != nil {
		bytes, err := json.Marshal(player.Metadata)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal metadata: %w", err)
		}
		metadataJSON = string(bytes)
	} else {
		// Use empty JSON object for nil metadata (JSONB column rejects empty strings)
		metadataJSON = "{}"
	}

	return &PlayerModel{
		ID:          player.ID.Value(),
		AgentSymbol: player.AgentSymbol,
		Token:       player.Token,
		// Credits field intentionally omitted - not persisted to database
		Metadata: metadataJSON,
	}, nil
}
