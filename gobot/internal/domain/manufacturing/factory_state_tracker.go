package manufacturing

import (
	"fmt"
	"sync"
)

// FactoryStateTracker manages factory states across the system
// Thread-safe for concurrent access
type FactoryStateTracker struct {
	mu     sync.RWMutex
	states map[string]*FactoryState // key: "pipelineID:factorySymbol:outputGood"
}

// NewFactoryStateTracker creates a new factory state tracker
func NewFactoryStateTracker() *FactoryStateTracker {
	return &FactoryStateTracker{
		states: make(map[string]*FactoryState),
	}
}

// makeKey generates a unique key for a factory state
func (t *FactoryStateTracker) makeKey(pipelineID, factorySymbol, outputGood string) string {
	return fmt.Sprintf("%s:%s:%s", pipelineID, factorySymbol, outputGood)
}

// LoadState loads an existing factory state into the tracker
func (t *FactoryStateTracker) LoadState(state *FactoryState) {
	t.mu.Lock()
	defer t.mu.Unlock()

	key := t.makeKey(state.PipelineID(), state.FactorySymbol(), state.OutputGood())
	t.states[key] = state
}

// GetAllFactories returns all tracked factory states
// Used by supply monitor to poll ALL factories (including ready ones)
// so we can detect supply drops and reset ready flags
func (t *FactoryStateTracker) GetAllFactories() []*FactoryState {
	t.mu.RLock()
	defer t.mu.RUnlock()

	all := make([]*FactoryState, 0, len(t.states))
	for _, state := range t.states {
		all = append(all, state)
	}
	return all
}

// RemovePipeline removes all factory states for a completed/failed pipeline
func (t *FactoryStateTracker) RemovePipeline(pipelineID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for key, state := range t.states {
		if state.PipelineID() == pipelineID {
			delete(t.states, key)
		}
	}
}
