package manufacturing

import (
	"context"
	"testing"

	domain "github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// adoptionFakePipelineRepo returns a fixed pipeline set from FindByStatus and
// records pipelines persisted via Update, so adoption behavior can be asserted
// without a database.
type adoptionFakePipelineRepo struct {
	domain.PipelineRepository
	byStatus []*domain.ManufacturingPipeline
	updated  []*domain.ManufacturingPipeline
}

func (r *adoptionFakePipelineRepo) FindByStatus(_ context.Context, _ int, _ []domain.PipelineStatus) ([]*domain.ManufacturingPipeline, error) {
	return r.byStatus, nil
}

func (r *adoptionFakePipelineRepo) Update(_ context.Context, p *domain.ManufacturingPipeline) error {
	r.updated = append(r.updated, p)
	return nil
}

func newExecutingConstructionPipeline(t *testing.T, site string) *domain.ManufacturingPipeline {
	t.Helper()
	p := domain.NewConstructionPipeline(site, 1, 3, 5)
	if err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return p
}

func newAdoptionManager(repo domain.PipelineRepository, registry *ActivePipelineRegistry) *PipelineLifecycleManager {
	return NewPipelineLifecycleManager(
		nil, nil, nil, nil, nil,
		repo, // pipelineRepo (arg 6)
		nil, nil, nil,
		registry, // registry (arg 10)
		nil, nil, nil, nil, nil,
	)
}

// A construction pipeline created (e.g. by `construction start`) while the
// coordinator is already running lands in the database but not in the
// coordinator's in-memory registry. Before sp-hkfb the registry was only
// populated at startup recovery, so the pipeline stayed unadopted until a manual
// coordinator bounce (the retired L57 "start FIRST, then bounce" folklore). The
// main-loop adoption poll must pick it up within one cycle - no restart.
func TestAdoptUnregisteredConstructionPipelines_AdoptsRuntimeCreatedPipeline(t *testing.T) {
	pipeline := newExecutingConstructionPipeline(t, "X1-TEST-I67")
	repo := &adoptionFakePipelineRepo{byStatus: []*domain.ManufacturingPipeline{pipeline}}
	registry := NewActivePipelineRegistry()
	manager := newAdoptionManager(repo, registry)

	adopted := manager.AdoptUnregisteredConstructionPipelines(context.Background(), 1)

	if adopted != 1 {
		t.Fatalf("expected 1 pipeline adopted, got %d", adopted)
	}
	if registry.Get(pipeline.ID()) == nil {
		t.Fatalf("expected pipeline %s adopted into registry without a restart", pipeline.ID()[:8])
	}
}

// A PLANNING construction pipeline must be Started (PLANNING -> EXECUTING) when
// adopted so its dependency-free delivery tasks become dispatchable in the same
// cycle, and the transition must be persisted so a later completion check reads
// a consistent status from the database.
func TestAdoptUnregisteredConstructionPipelines_StartsPlanningPipeline(t *testing.T) {
	pipeline := domain.NewConstructionPipeline("X1-TEST-I67", 1, 3, 5) // PLANNING
	repo := &adoptionFakePipelineRepo{byStatus: []*domain.ManufacturingPipeline{pipeline}}
	registry := NewActivePipelineRegistry()
	manager := newAdoptionManager(repo, registry)

	adopted := manager.AdoptUnregisteredConstructionPipelines(context.Background(), 1)

	if adopted != 1 {
		t.Fatalf("expected 1 pipeline adopted, got %d", adopted)
	}
	if pipeline.Status() != domain.PipelineStatusExecuting {
		t.Fatalf("expected adopted PLANNING pipeline to be EXECUTING, got %s", pipeline.Status())
	}
	if len(repo.updated) != 1 {
		t.Fatalf("expected the PLANNING->EXECUTING transition persisted once, got %d Update calls", len(repo.updated))
	}
}

// Adoption must be idempotent: a pipeline already in the registry (adopted at
// startup or on a previous poll) must not be re-adopted or re-persisted, so the
// 10s poll does not churn the database or double-count active pipelines.
func TestAdoptUnregisteredConstructionPipelines_SkipsAlreadyRegistered(t *testing.T) {
	pipeline := newExecutingConstructionPipeline(t, "X1-TEST-I67")
	repo := &adoptionFakePipelineRepo{byStatus: []*domain.ManufacturingPipeline{pipeline}}
	registry := NewActivePipelineRegistry()
	registry.Register(pipeline)
	manager := newAdoptionManager(repo, registry)

	adopted := manager.AdoptUnregisteredConstructionPipelines(context.Background(), 1)

	if adopted != 0 {
		t.Fatalf("expected already-registered pipeline to be skipped, got %d adopted", adopted)
	}
	if len(repo.updated) != 0 {
		t.Fatalf("expected no persistence for already-registered pipeline, got %d Update calls", len(repo.updated))
	}
}

// Only externally-created CONSTRUCTION pipelines need adoption. FABRICATION and
// COLLECTION pipelines are always self-registered inline by their owning
// (singleton-per-system) coordinator, so an unregistered active one belongs to a
// different coordinator and must NOT be stolen by this player-scoped poll.
func TestAdoptUnregisteredConstructionPipelines_IgnoresFabricationPipelines(t *testing.T) {
	fab := domain.NewPipeline("IRON_ORE", "X1-MARKET", 1000, 1)
	if err := fab.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	repo := &adoptionFakePipelineRepo{byStatus: []*domain.ManufacturingPipeline{fab}}
	registry := NewActivePipelineRegistry()
	manager := newAdoptionManager(repo, registry)

	adopted := manager.AdoptUnregisteredConstructionPipelines(context.Background(), 1)

	if adopted != 0 {
		t.Fatalf("expected FABRICATION pipeline to be ignored, got %d adopted", adopted)
	}
	if registry.Get(fab.ID()) != nil {
		t.Fatalf("expected FABRICATION pipeline NOT adopted into registry")
	}
}
