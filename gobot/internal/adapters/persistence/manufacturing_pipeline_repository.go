package persistence

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"gorm.io/gorm"
)

// GormManufacturingPipelineRepository implements PipelineRepository using GORM
type GormManufacturingPipelineRepository struct {
	db *gorm.DB
}

// NewGormManufacturingPipelineRepository creates a new GORM manufacturing pipeline repository
func NewGormManufacturingPipelineRepository(db *gorm.DB) *GormManufacturingPipelineRepository {
	return &GormManufacturingPipelineRepository{db: db}
}

// Create persists a new pipeline
func (r *GormManufacturingPipelineRepository) Create(ctx context.Context, pipeline *manufacturing.ManufacturingPipeline) error {
	// Compute next sequence number for this player
	var maxSeq int
	err := r.db.WithContext(ctx).
		Model(&ManufacturingPipelineModel{}).
		Where("player_id = ?", pipeline.PlayerID()).
		Select("COALESCE(MAX(sequence_number), 0)").
		Scan(&maxSeq).Error
	if err != nil {
		return fmt.Errorf("failed to get max sequence number: %w", err)
	}

	// Set the sequence number on the pipeline
	pipeline.SetSequenceNumber(maxSeq + 1)

	model := r.pipelineToModel(pipeline)

	result := r.db.WithContext(ctx).Create(model)
	if result.Error != nil {
		return fmt.Errorf("failed to create pipeline: %w", result.Error)
	}

	return nil
}

// Update saves changes to an existing pipeline
func (r *GormManufacturingPipelineRepository) Update(ctx context.Context, pipeline *manufacturing.ManufacturingPipeline) error {
	model := r.pipelineToModel(pipeline)

	result := r.db.WithContext(ctx).Save(model)
	if result.Error != nil {
		return fmt.Errorf("failed to update pipeline: %w", result.Error)
	}

	return nil
}

// FindByID retrieves a pipeline by its ID
func (r *GormManufacturingPipelineRepository) FindByID(ctx context.Context, id string) (*manufacturing.ManufacturingPipeline, error) {
	var model ManufacturingPipelineModel
	result := r.db.WithContext(ctx).Where("id = ?", id).First(&model)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to find pipeline: %w", result.Error)
	}

	return r.modelToPipeline(&model)
}

// FindByPlayerID retrieves all pipelines for a player
func (r *GormManufacturingPipelineRepository) FindByPlayerID(ctx context.Context, playerID int) ([]*manufacturing.ManufacturingPipeline, error) {
	var models []ManufacturingPipelineModel
	result := r.db.WithContext(ctx).
		Where("player_id = ?", playerID).
		Order("created_at DESC").
		Find(&models)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to find pipelines: %w", result.Error)
	}

	pipelines := make([]*manufacturing.ManufacturingPipeline, len(models))
	for i, model := range models {
		p, err := r.modelToPipeline(&model)
		if err != nil {
			return nil, fmt.Errorf("failed to convert pipeline model: %w", err)
		}
		pipelines[i] = p
	}

	return pipelines, nil
}

// FindByStatus retrieves pipelines by status for a player
func (r *GormManufacturingPipelineRepository) FindByStatus(ctx context.Context, playerID int, statuses []manufacturing.PipelineStatus) ([]*manufacturing.ManufacturingPipeline, error) {
	statusStrings := make([]string, len(statuses))
	for i, s := range statuses {
		statusStrings[i] = string(s)
	}

	var models []ManufacturingPipelineModel
	result := r.db.WithContext(ctx).
		Where("player_id = ? AND status IN ?", playerID, statusStrings).
		Order("created_at DESC").
		Find(&models)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to find pipelines by status: %w", result.Error)
	}

	pipelines := make([]*manufacturing.ManufacturingPipeline, len(models))
	for i, model := range models {
		p, err := r.modelToPipeline(&model)
		if err != nil {
			return nil, fmt.Errorf("failed to convert pipeline model: %w", err)
		}
		pipelines[i] = p
	}

	return pipelines, nil
}

// FindActiveForProduct checks if there's an active pipeline for a product
func (r *GormManufacturingPipelineRepository) FindActiveForProduct(ctx context.Context, playerID int, productGood string) (*manufacturing.ManufacturingPipeline, error) {
	activeStatuses := []string{
		string(manufacturing.PipelineStatusPlanning),
		string(manufacturing.PipelineStatusExecuting),
	}

	var model ManufacturingPipelineModel
	result := r.db.WithContext(ctx).
		Where("player_id = ? AND product_good = ? AND status IN ?", playerID, productGood, activeStatuses).
		First(&model)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to find active pipeline: %w", result.Error)
	}

	return r.modelToPipeline(&model)
}

// FindActiveCollectionForProduct checks if there's an active COLLECTION pipeline for a product.
// This is used to prevent duplicate collection pipelines for the same good.
func (r *GormManufacturingPipelineRepository) FindActiveCollectionForProduct(ctx context.Context, playerID int, productGood string) (*manufacturing.ManufacturingPipeline, error) {
	activeStatuses := []string{
		string(manufacturing.PipelineStatusPlanning),
		string(manufacturing.PipelineStatusExecuting),
	}

	var model ManufacturingPipelineModel
	result := r.db.WithContext(ctx).
		Where("player_id = ? AND product_good = ? AND pipeline_type = ? AND status IN ?",
			playerID, productGood, string(manufacturing.PipelineTypeCollection), activeStatuses).
		First(&model)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to find active collection pipeline: %w", result.Error)
	}

	return r.modelToPipeline(&model)
}

// CountActiveFabricationPipelines counts only FABRICATION pipelines that are active.
// This is used for max_pipelines limiting.
func (r *GormManufacturingPipelineRepository) CountActiveFabricationPipelines(ctx context.Context, playerID int) (int, error) {
	activeStatuses := []string{
		string(manufacturing.PipelineStatusPlanning),
		string(manufacturing.PipelineStatusExecuting),
	}

	var count int64
	result := r.db.WithContext(ctx).
		Model(&ManufacturingPipelineModel{}).
		Where("player_id = ? AND pipeline_type = ? AND status IN ?",
			playerID, string(manufacturing.PipelineTypeFabrication), activeStatuses).
		Count(&count)

	if result.Error != nil {
		return 0, fmt.Errorf("failed to count active fabrication pipelines: %w", result.Error)
	}

	return int(count), nil
}

// CountActiveCollectionPipelines counts only COLLECTION pipelines that are active.
// This is used for max_collection_pipelines limiting (0 = unlimited).
func (r *GormManufacturingPipelineRepository) CountActiveCollectionPipelines(ctx context.Context, playerID int) (int, error) {
	activeStatuses := []string{
		string(manufacturing.PipelineStatusPlanning),
		string(manufacturing.PipelineStatusExecuting),
	}

	var count int64
	result := r.db.WithContext(ctx).
		Model(&ManufacturingPipelineModel{}).
		Where("player_id = ? AND pipeline_type = ? AND status IN ?",
			playerID, string(manufacturing.PipelineTypeCollection), activeStatuses).
		Count(&count)

	if result.Error != nil {
		return 0, fmt.Errorf("failed to count active collection pipelines: %w", result.Error)
	}

	return int(count), nil
}

// FindByConstructionSite retrieves the pipeline for a specific construction site (for idempotency).
// Returns nil, nil if no active pipeline exists for this site.
// Only returns non-terminal pipelines (excludes COMPLETED, FAILED, CANCELLED).
func (r *GormManufacturingPipelineRepository) FindByConstructionSite(ctx context.Context, constructionSiteSymbol string, playerID int) (*manufacturing.ManufacturingPipeline, error) {
	activeStatuses := []string{
		string(manufacturing.PipelineStatusPlanning),
		string(manufacturing.PipelineStatusExecuting),
	}

	var model ManufacturingPipelineModel
	result := r.db.WithContext(ctx).
		Where("player_id = ? AND construction_site = ? AND pipeline_type = ? AND status IN ?",
			playerID, constructionSiteSymbol, string(manufacturing.PipelineTypeConstruction), activeStatuses).
		First(&model)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to find pipeline by construction site: %w", result.Error)
	}

	return r.modelToPipeline(&model)
}

// Delete removes a pipeline (cascades to tasks)
func (r *GormManufacturingPipelineRepository) Delete(ctx context.Context, id string) error {
	result := r.db.WithContext(ctx).Where("id = ?", id).Delete(&ManufacturingPipelineModel{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete pipeline: %w", result.Error)
	}

	return nil
}

// pipelineToModel converts domain entity to database model
func (r *GormManufacturingPipelineRepository) pipelineToModel(p *manufacturing.ManufacturingPipeline) *ManufacturingPipelineModel {
	var errorMsg *string
	if p.ErrorMessage() != "" {
		msg := p.ErrorMessage()
		errorMsg = &msg
	}

	// Handle construction site
	var constructionSite *string
	if p.ConstructionSite() != "" {
		site := p.ConstructionSite()
		constructionSite = &site
	}

	// Serialize materials to JSON
	materialsJSON := "[]"
	if len(p.Materials()) > 0 {
		materials := make([]map[string]interface{}, len(p.Materials()))
		for i, m := range p.Materials() {
			materials[i] = map[string]interface{}{
				"tradeSymbol":       m.TradeSymbol(),
				"targetQuantity":    m.TargetQuantity(),
				"deliveredQuantity": m.DeliveredQuantity(),
			}
		}
		if data, err := json.Marshal(materials); err == nil {
			materialsJSON = string(data)
		}
	}

	return &ManufacturingPipelineModel{
		ID:               p.ID(),
		SequenceNumber:   p.SequenceNumber(),
		PipelineType:     string(p.PipelineType()),
		PlayerID:         p.PlayerID(),
		ProductGood:      p.ProductGood(),
		SellMarket:       p.SellMarket(),
		ExpectedPrice:    p.ExpectedPrice(),
		Status:           string(p.Status()),
		TotalCost:        p.TotalCost(),
		TotalRevenue:     p.TotalRevenue(),
		NetProfit:        p.NetProfit(),
		ErrorMessage:     errorMsg,
		CreatedAt:        p.CreatedAt(),
		StartedAt:        p.StartedAt(),
		CompletedAt:      p.CompletedAt(),
		ConstructionSite: constructionSite,
		Materials:        materialsJSON,
		SupplyChainDepth: p.SupplyChainDepth(),
		MaxWorkers:       p.MaxWorkers(),
	}
}

// modelToPipeline converts database model to domain entity
func (r *GormManufacturingPipelineRepository) modelToPipeline(m *ManufacturingPipelineModel) (*manufacturing.ManufacturingPipeline, error) {
	var errorMsg string
	if m.ErrorMessage != nil {
		errorMsg = *m.ErrorMessage
	}

	// Handle construction site
	var constructionSite string
	if m.ConstructionSite != nil {
		constructionSite = *m.ConstructionSite
	}

	pipeline := manufacturing.ReconstitutePipeline(
		m.ID,
		m.SequenceNumber,
		manufacturing.PipelineType(m.PipelineType),
		m.ProductGood,
		m.SellMarket,
		m.ExpectedPrice,
		m.PlayerID,
		manufacturing.PipelineStatus(m.Status),
		m.TotalCost,
		m.TotalRevenue,
		m.NetProfit,
		errorMsg,
		m.CreatedAt,
		m.StartedAt,
		m.CompletedAt,
		constructionSite,
		m.SupplyChainDepth,
		m.MaxWorkers,
	)

	// Parse and set materials for construction pipelines
	if m.Materials != "" && m.Materials != "[]" {
		var materialsData []struct {
			TradeSymbol       string `json:"tradeSymbol"`
			TargetQuantity    int    `json:"targetQuantity"`
			DeliveredQuantity int    `json:"deliveredQuantity"`
		}
		if err := json.Unmarshal([]byte(m.Materials), &materialsData); err == nil {
			materials := make([]*manufacturing.ConstructionMaterialTarget, len(materialsData))
			for i, md := range materialsData {
				materials[i] = manufacturing.ReconstructConstructionMaterialTarget(
					md.TradeSymbol,
					md.TargetQuantity,
					md.DeliveredQuantity,
				)
			}
			pipeline.SetMaterials(materials)
		}
	}

	return pipeline, nil
}
