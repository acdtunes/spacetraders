package manufacturing

import (
	"testing"

	domain "github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// TestAddActivePipeline_KeysByPipelineID documents that AddActivePipeline registers a
// pipeline keyed by its own ID (pipeline.ID()). The registry is the source of truth for
// the key, so no separate id argument is needed.
func TestAddActivePipeline_KeysByPipelineID(t *testing.T) {
	registry := NewActivePipelineRegistry()
	manager := NewPipelineLifecycleManager(
		nil, nil, nil, nil, nil, nil, nil, nil, nil,
		registry,
		nil, nil, nil, nil, nil,
	)

	pipeline := domain.NewPipeline("IRON_ORE", "X1-MARKET", 1000, 1)

	manager.AddActivePipeline(pipeline)

	active := manager.GetActivePipelines()
	got, ok := active[pipeline.ID()]
	if !ok {
		t.Fatalf("expected pipeline registered under key %q, keys present: %v", pipeline.ID(), keysOf(active))
	}
	if got != pipeline {
		t.Fatalf("expected registered pipeline to be the same instance keyed by ID")
	}
}

func keysOf(m map[string]*domain.ManufacturingPipeline) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
