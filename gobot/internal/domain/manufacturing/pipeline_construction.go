package manufacturing

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

const defaultConstructionMaxWorkers = 5

// NewConstructionPipeline creates a new construction pipeline for delivering materials to a construction site.
// Construction pipelines track multiple materials with their individual delivery progress.
func NewConstructionPipeline(constructionSite string, playerID int, supplyChainDepth int, maxWorkers int) *ManufacturingPipeline {
	if maxWorkers <= 0 {
		maxWorkers = defaultConstructionMaxWorkers
	}
	return &ManufacturingPipeline{
		id:               uuid.New().String(),
		pipelineType:     PipelineTypeConstruction,
		productGood:      "", // Set by first material added
		sellMarket:       constructionSite,
		expectedPrice:    0, // Construction doesn't have sale prices
		playerID:         playerID,
		status:           PipelineStatusPlanning,
		tasks:            make([]*ManufacturingTask, 0),
		tasksByID:        make(map[string]*ManufacturingTask),
		createdAt:        time.Now(),
		constructionSite: constructionSite,
		materials:        make([]*ConstructionMaterialTarget, 0),
		supplyChainDepth: supplyChainDepth,
		maxWorkers:       maxWorkers,
	}
}

// ConstructionSite returns the waypoint symbol of the construction site (CONSTRUCTION pipelines only)
func (p *ManufacturingPipeline) ConstructionSite() string { return p.constructionSite }

// Materials returns the material targets for this construction pipeline
func (p *ManufacturingPipeline) Materials() []*ConstructionMaterialTarget {
	result := make([]*ConstructionMaterialTarget, len(p.materials))
	copy(result, p.materials)
	return result
}

// SupplyChainDepth returns how deep to go in the supply chain (CONSTRUCTION pipelines only)
// 0 = full chain (produce everything), 1 = raw materials only, 2 = intermediate goods
func (p *ManufacturingPipeline) SupplyChainDepth() int { return p.supplyChainDepth }

// MaxWorkers returns the maximum parallel workers for this pipeline (CONSTRUCTION pipelines only)
// 0 = unlimited, default is 5
func (p *ManufacturingPipeline) MaxWorkers() int { return p.maxWorkers }

// SetMaxWorkers amends the concurrent-worker cap on this construction pipeline. The drain re-reads it
// off the pipeline row every tick (resolveWorkerCap → errgroup.SetLimit), so a live update converges
// the fan-out on the next tick with no restart, and it is re-persisted so it survives a daemon bounce
// (RULINGS #2). A non-positive value falls back to the default (mirroring the constructor) so the cap
// can never drive SetLimit(0), which would deadlock the drain tick.
func (p *ManufacturingPipeline) SetMaxWorkers(maxWorkers int) {
	if maxWorkers <= 0 {
		maxWorkers = defaultConstructionMaxWorkers
	}
	p.maxWorkers = maxWorkers
}

// MinSupply returns the caller-set EXPORT sourcing floor for this construction
// pipeline (e.g. "SCARCE"), CONSTRUCTION pipelines only. Empty string means
// unset, which callers (MarketLocator.FindConstructionSource) treat as the
// default MODERATE floor.
func (p *ManufacturingPipeline) MinSupply() string { return p.minSupply }

// SetMinSupply sets the caller-set EXPORT sourcing floor for this construction
// pipeline. Used both when planning a new pipeline and when resuming an
// existing one with an updated --min-supply flag.
func (p *ManufacturingPipeline) SetMinSupply(minSupply string) { p.minSupply = minSupply }

// GoodOverrides returns the per-good buy-gating override map persisted on this construction
// pipeline. Empty when no per-good override was supplied at launch, in which case every
// good uses the pipeline's global min-supply floor.
func (p *ManufacturingPipeline) GoodOverrides() GoodGatingOverrides { return p.goodOverrides }

// SetGoodOverrides stores the per-good buy-gating override map on this construction pipeline. Set
// at planning time and re-persisted when a resumed launch supplies a changed map, so the
// deferred-material recovery loop reads the same overrides the initial plan used.
func (p *ManufacturingPipeline) SetGoodOverrides(overrides GoodGatingOverrides) {
	p.goodOverrides = overrides
}

// AddMaterial adds a material target to the construction pipeline
func (p *ManufacturingPipeline) AddMaterial(material *ConstructionMaterialTarget) error {
	if p.pipelineType != PipelineTypeConstruction {
		return fmt.Errorf("can only add materials to CONSTRUCTION pipelines")
	}
	if p.status != PipelineStatusPlanning {
		return &ErrInvalidPipelineTransition{
			PipelineID:  p.id,
			From:        p.status,
			To:          p.status,
			Description: "can only add materials during PLANNING",
		}
	}
	p.materials = append(p.materials, material)
	// Set productGood to first material for display purposes
	if p.productGood == "" {
		p.productGood = material.TradeSymbol()
	}
	return nil
}

// SetMaterials sets all materials for the pipeline (used during reconstruction)
func (p *ManufacturingPipeline) SetMaterials(materials []*ConstructionMaterialTarget) {
	p.materials = materials
}

// GetMaterial returns the material target for a specific trade symbol
func (p *ManufacturingPipeline) GetMaterial(tradeSymbol string) *ConstructionMaterialTarget {
	for _, m := range p.materials {
		if m.TradeSymbol() == tradeSymbol {
			return m
		}
	}
	return nil
}

// RecordMaterialDelivery updates the delivered quantity for a specific material
func (p *ManufacturingPipeline) RecordMaterialDelivery(tradeSymbol string, units int) error {
	material := p.GetMaterial(tradeSymbol)
	if material == nil {
		return fmt.Errorf("material %s not found in pipeline", tradeSymbol)
	}
	material.RecordDelivery(units)
	return nil
}

// ConstructionProgress returns overall completion percentage across all materials
func (p *ManufacturingPipeline) ConstructionProgress() float64 {
	if len(p.materials) == 0 {
		return 0
	}
	var totalTarget, totalDelivered int
	for _, m := range p.materials {
		totalTarget += m.TargetQuantity()
		totalDelivered += m.DeliveredQuantity()
	}
	if totalTarget == 0 {
		return 100.0
	}
	return float64(totalDelivered) / float64(totalTarget) * 100
}
