package manufacturing

import (
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// ActivePipelineRegistry tracks active manufacturing pipelines in memory.
// Provides fast lookups and prevents duplicate pipelines for the same good.
type ActivePipelineRegistry struct {
	mu        sync.RWMutex
	pipelines map[string]*manufacturing.ManufacturingPipeline
}

// NewActivePipelineRegistry creates a new registry.
func NewActivePipelineRegistry() *ActivePipelineRegistry {
	return &ActivePipelineRegistry{
		pipelines: make(map[string]*manufacturing.ManufacturingPipeline),
	}
}

// Register adds a pipeline to the registry.
func (r *ActivePipelineRegistry) Register(pipeline *manufacturing.ManufacturingPipeline) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pipelines[pipeline.ID()] = pipeline
}

// Unregister removes a pipeline from the registry.
func (r *ActivePipelineRegistry) Unregister(pipelineID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.pipelines, pipelineID)
}

// Get returns a pipeline by ID, or nil if not found.
func (r *ActivePipelineRegistry) Get(pipelineID string) *manufacturing.ManufacturingPipeline {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.pipelines[pipelineID]
}

// GetAll returns a copy of all active pipelines.
func (r *ActivePipelineRegistry) GetAll() map[string]*manufacturing.ManufacturingPipeline {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[string]*manufacturing.ManufacturingPipeline, len(r.pipelines))
	for k, v := range r.pipelines {
		result[k] = v
	}
	return result
}

// HasPipelineForGood checks if an active pipeline exists for the given good.
func (r *ActivePipelineRegistry) HasPipelineForGood(good string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, pipeline := range r.pipelines {
		if pipeline.ProductGood() == good {
			return true
		}
	}
	return false
}

// Count returns the number of active pipelines.
func (r *ActivePipelineRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.pipelines)
}

// CountByType returns the count of pipelines by type.
func (r *ActivePipelineRegistry) CountByType(pipelineType manufacturing.PipelineType) int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, pipeline := range r.pipelines {
		if pipeline.PipelineType() == pipelineType {
			count++
		}
	}
	return count
}

// GetPipelineIDs returns all active pipeline IDs.
func (r *ActivePipelineRegistry) GetPipelineIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]string, 0, len(r.pipelines))
	for id := range r.pipelines {
		result = append(result, id)
	}
	return result
}

// GetPipelinesByType returns pipelines of a specific type.
func (r *ActivePipelineRegistry) GetPipelinesByType(pipelineType manufacturing.PipelineType) []*manufacturing.ManufacturingPipeline {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*manufacturing.ManufacturingPipeline, 0)
	for _, pipeline := range r.pipelines {
		if pipeline.PipelineType() == pipelineType {
			result = append(result, pipeline)
		}
	}
	return result
}

// Clear removes all pipelines from the registry.
func (r *ActivePipelineRegistry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pipelines = make(map[string]*manufacturing.ManufacturingPipeline)
}

// SetAll replaces all pipelines in the registry (for state recovery).
func (r *ActivePipelineRegistry) SetAll(pipelines map[string]*manufacturing.ManufacturingPipeline) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pipelines = pipelines
}

// Exists checks if a pipeline is registered.
func (r *ActivePipelineRegistry) Exists(pipelineID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, exists := r.pipelines[pipelineID]
	return exists
}

// GetGetter returns a read-only getter function for the pipelines.
// This is useful for injecting pipeline access without exposing the full registry.
func (r *ActivePipelineRegistry) GetGetter() func() map[string]*manufacturing.ManufacturingPipeline {
	return r.GetAll
}
