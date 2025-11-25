package persistence

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"gorm.io/gorm"
)

// GormManufacturingFactoryStateRepository implements FactoryStateRepository using GORM
type GormManufacturingFactoryStateRepository struct {
	db *gorm.DB
}

// NewGormManufacturingFactoryStateRepository creates a new GORM manufacturing factory state repository
func NewGormManufacturingFactoryStateRepository(db *gorm.DB) *GormManufacturingFactoryStateRepository {
	return &GormManufacturingFactoryStateRepository{db: db}
}

// Create persists a new factory state
func (r *GormManufacturingFactoryStateRepository) Create(ctx context.Context, state *manufacturing.FactoryState) error {
	model, err := r.factoryStateToModel(state)
	if err != nil {
		return fmt.Errorf("failed to convert factory state to model: %w", err)
	}

	result := r.db.WithContext(ctx).Create(model)
	if result.Error != nil {
		return fmt.Errorf("failed to create factory state: %w", result.Error)
	}

	// Update the state's ID with the auto-generated one
	state.SetID(model.ID)

	return nil
}

// Update saves changes to an existing factory state
func (r *GormManufacturingFactoryStateRepository) Update(ctx context.Context, state *manufacturing.FactoryState) error {
	model, err := r.factoryStateToModel(state)
	if err != nil {
		return fmt.Errorf("failed to convert factory state to model: %w", err)
	}

	result := r.db.WithContext(ctx).Save(model)
	if result.Error != nil {
		return fmt.Errorf("failed to update factory state: %w", result.Error)
	}

	return nil
}

// FindByID retrieves a factory state by database ID
func (r *GormManufacturingFactoryStateRepository) FindByID(ctx context.Context, id int) (*manufacturing.FactoryState, error) {
	var model ManufacturingFactoryStateModel
	result := r.db.WithContext(ctx).Where("id = ?", id).First(&model)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to find factory state: %w", result.Error)
	}

	return r.modelToFactoryState(&model)
}

// FindByFactory retrieves factory state for a specific factory/output/pipeline
func (r *GormManufacturingFactoryStateRepository) FindByFactory(ctx context.Context, pipelineID string, factorySymbol string, outputGood string) (*manufacturing.FactoryState, error) {
	var model ManufacturingFactoryStateModel
	result := r.db.WithContext(ctx).
		Where("pipeline_id = ? AND factory_symbol = ? AND output_good = ?", pipelineID, factorySymbol, outputGood).
		First(&model)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to find factory state: %w", result.Error)
	}

	return r.modelToFactoryState(&model)
}

// FindByPipelineID retrieves all factory states for a pipeline
func (r *GormManufacturingFactoryStateRepository) FindByPipelineID(ctx context.Context, pipelineID string) ([]*manufacturing.FactoryState, error) {
	var models []ManufacturingFactoryStateModel
	result := r.db.WithContext(ctx).
		Where("pipeline_id = ?", pipelineID).
		Find(&models)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to find factory states: %w", result.Error)
	}

	states := make([]*manufacturing.FactoryState, len(models))
	for i, model := range models {
		s, err := r.modelToFactoryState(&model)
		if err != nil {
			return nil, fmt.Errorf("failed to convert factory state model: %w", err)
		}
		states[i] = s
	}

	return states, nil
}

// FindPending retrieves factory states awaiting production for a player
func (r *GormManufacturingFactoryStateRepository) FindPending(ctx context.Context, playerID int) ([]*manufacturing.FactoryState, error) {
	var models []ManufacturingFactoryStateModel
	result := r.db.WithContext(ctx).
		Where("player_id = ? AND ready_for_collection = ?", playerID, false).
		Find(&models)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to find pending factory states: %w", result.Error)
	}

	states := make([]*manufacturing.FactoryState, len(models))
	for i, model := range models {
		s, err := r.modelToFactoryState(&model)
		if err != nil {
			return nil, fmt.Errorf("failed to convert factory state model: %w", err)
		}
		states[i] = s
	}

	return states, nil
}

// FindReadyForCollection retrieves factory states ready for collection
func (r *GormManufacturingFactoryStateRepository) FindReadyForCollection(ctx context.Context, playerID int) ([]*manufacturing.FactoryState, error) {
	var models []ManufacturingFactoryStateModel
	result := r.db.WithContext(ctx).
		Where("player_id = ? AND ready_for_collection = ?", playerID, true).
		Find(&models)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to find ready factory states: %w", result.Error)
	}

	states := make([]*manufacturing.FactoryState, len(models))
	for i, model := range models {
		s, err := r.modelToFactoryState(&model)
		if err != nil {
			return nil, fmt.Errorf("failed to convert factory state model: %w", err)
		}
		states[i] = s
	}

	return states, nil
}

// Delete removes a factory state
func (r *GormManufacturingFactoryStateRepository) Delete(ctx context.Context, id int) error {
	result := r.db.WithContext(ctx).Where("id = ?", id).Delete(&ManufacturingFactoryStateModel{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete factory state: %w", result.Error)
	}

	return nil
}

// DeleteByPipelineID removes all factory states for a pipeline
func (r *GormManufacturingFactoryStateRepository) DeleteByPipelineID(ctx context.Context, pipelineID string) error {
	result := r.db.WithContext(ctx).Where("pipeline_id = ?", pipelineID).Delete(&ManufacturingFactoryStateModel{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete factory states: %w", result.Error)
	}

	return nil
}

// factoryStateToModel converts domain entity to database model
func (r *GormManufacturingFactoryStateRepository) factoryStateToModel(s *manufacturing.FactoryState) (*ManufacturingFactoryStateModel, error) {
	// Convert required inputs to JSON
	requiredInputsJSON, err := json.Marshal(s.RequiredInputs())
	if err != nil {
		return nil, fmt.Errorf("failed to marshal required inputs: %w", err)
	}

	// Convert delivered inputs to JSON
	deliveredInputsMap := make(map[string]interface{})
	for good, inputState := range s.DeliveredInputs() {
		deliveredInputsMap[good] = map[string]interface{}{
			"delivered": inputState.Delivered,
			"quantity":  inputState.Quantity,
			"ship":      inputState.DeliveredBy,
		}
	}
	deliveredInputsJSON, err := json.Marshal(deliveredInputsMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal delivered inputs: %w", err)
	}

	var currentSupply, previousSupply *string
	if s.CurrentSupply() != "" {
		cs := s.CurrentSupply()
		currentSupply = &cs
	}
	if s.PreviousSupply() != "" {
		ps := s.PreviousSupply()
		previousSupply = &ps
	}

	return &ManufacturingFactoryStateModel{
		ID:                 s.ID(),
		FactorySymbol:      s.FactorySymbol(),
		OutputGood:         s.OutputGood(),
		PlayerID:           s.PlayerID(),
		PipelineID:         s.PipelineID(),
		RequiredInputs:     string(requiredInputsJSON),
		DeliveredInputs:    string(deliveredInputsJSON),
		AllInputsDelivered: s.AllInputsDelivered(),
		CurrentSupply:      currentSupply,
		PreviousSupply:     previousSupply,
		ReadyForCollection: s.ReadyForCollection(),
		CreatedAt:          s.CreatedAt(),
		InputsCompletedAt:  s.InputsCompletedAt(),
		ReadyAt:            s.ReadyAt(),
	}, nil
}

// modelToFactoryState converts database model to domain entity
func (r *GormManufacturingFactoryStateRepository) modelToFactoryState(m *ManufacturingFactoryStateModel) (*manufacturing.FactoryState, error) {
	// Parse required inputs from JSON
	var requiredInputs []string
	if err := json.Unmarshal([]byte(m.RequiredInputs), &requiredInputs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal required inputs: %w", err)
	}

	// Parse delivered inputs from JSON
	var deliveredInputsRaw map[string]map[string]interface{}
	if err := json.Unmarshal([]byte(m.DeliveredInputs), &deliveredInputsRaw); err != nil {
		return nil, fmt.Errorf("failed to unmarshal delivered inputs: %w", err)
	}

	deliveredInputs := make(map[string]*manufacturing.InputState)
	for good, rawState := range deliveredInputsRaw {
		delivered, _ := rawState["delivered"].(bool)
		quantity := 0
		if q, ok := rawState["quantity"].(float64); ok {
			quantity = int(q)
		}
		deliveredBy, _ := rawState["ship"].(string)
		deliveredInputs[good] = &manufacturing.InputState{
			Delivered:   delivered,
			Quantity:    quantity,
			DeliveredBy: deliveredBy,
		}
	}

	var currentSupply, previousSupply string
	if m.CurrentSupply != nil {
		currentSupply = *m.CurrentSupply
	}
	if m.PreviousSupply != nil {
		previousSupply = *m.PreviousSupply
	}

	return manufacturing.ReconstituteFactoryState(
		m.ID,
		m.FactorySymbol,
		m.OutputGood,
		m.PipelineID,
		m.PlayerID,
		requiredInputs,
		deliveredInputs,
		m.AllInputsDelivered,
		currentSupply,
		previousSupply,
		m.ReadyForCollection,
		m.CreatedAt,
		m.InputsCompletedAt,
		m.ReadyAt,
	), nil
}
