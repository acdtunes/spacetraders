package manufacturing

import (
	"fmt"
	"sync"
	"time"
)

// SupplyLevel ordering for comparison
var SupplyLevelOrder = map[string]int{
	"SCARCE":   1,
	"LIMITED":  2,
	"MODERATE": 3,
	"HIGH":     4,
	"ABUNDANT": 5,
}

// RequiredSupplyLevel is the minimum supply level for collection
const RequiredSupplyLevel = "HIGH"

// InputState tracks delivery status of a single input
type InputState struct {
	Good        string
	Delivered   bool
	DeliveredAt *time.Time
	DeliveredBy string // Ship that delivered
	Quantity    int
}

// FactoryState tracks the state of a factory for a specific good within a pipeline.
// Each pipeline has its own factory states to avoid conflicts between concurrent pipelines.
type FactoryState struct {
	id            int // Database ID (0 if not persisted)
	factorySymbol string
	outputGood    string
	pipelineID    string // Pipeline this state belongs to
	playerID      int

	// Input tracking
	requiredInputs     []string
	deliveredInputs    map[string]*InputState
	allInputsDelivered bool

	// Production state
	inputsCompletedAt *time.Time
	currentSupply     string
	previousSupply    string

	// Ready state
	readyForCollection bool
	readyAt            *time.Time

	// Timing
	createdAt time.Time
}

// NewFactoryState creates a new factory state tracker for a pipeline
func NewFactoryState(
	factorySymbol string,
	outputGood string,
	pipelineID string,
	playerID int,
	requiredInputs []string,
) *FactoryState {
	inputStates := make(map[string]*InputState)
	for _, input := range requiredInputs {
		inputStates[input] = &InputState{
			Good:      input,
			Delivered: false,
		}
	}

	return &FactoryState{
		factorySymbol:      factorySymbol,
		outputGood:         outputGood,
		pipelineID:         pipelineID,
		playerID:           playerID,
		requiredInputs:     requiredInputs,
		deliveredInputs:    inputStates,
		allInputsDelivered: false,
		readyForCollection: false,
		createdAt:          time.Now(),
	}
}

// ReconstructFactoryState rebuilds a factory state from persistence
func ReconstructFactoryState(
	id int,
	factorySymbol string,
	outputGood string,
	pipelineID string,
	playerID int,
	requiredInputs []string,
	deliveredInputs map[string]*InputState,
	allInputsDelivered bool,
	inputsCompletedAt *time.Time,
	currentSupply string,
	previousSupply string,
	readyForCollection bool,
	readyAt *time.Time,
	createdAt time.Time,
) *FactoryState {
	return &FactoryState{
		id:                 id,
		factorySymbol:      factorySymbol,
		outputGood:         outputGood,
		pipelineID:         pipelineID,
		playerID:           playerID,
		requiredInputs:     requiredInputs,
		deliveredInputs:    deliveredInputs,
		allInputsDelivered: allInputsDelivered,
		inputsCompletedAt:  inputsCompletedAt,
		currentSupply:      currentSupply,
		previousSupply:     previousSupply,
		readyForCollection: readyForCollection,
		readyAt:            readyAt,
		createdAt:          createdAt,
	}
}

// Getters

func (f *FactoryState) ID() int                             { return f.id }
func (f *FactoryState) FactorySymbol() string               { return f.factorySymbol }
func (f *FactoryState) OutputGood() string                  { return f.outputGood }
func (f *FactoryState) PipelineID() string                  { return f.pipelineID }
func (f *FactoryState) PlayerID() int                       { return f.playerID }
func (f *FactoryState) RequiredInputs() []string            { return f.requiredInputs }
func (f *FactoryState) DeliveredInputs() map[string]*InputState { return f.deliveredInputs }
func (f *FactoryState) AllInputsDelivered() bool            { return f.allInputsDelivered }
func (f *FactoryState) InputsCompletedAt() *time.Time       { return f.inputsCompletedAt }
func (f *FactoryState) CurrentSupply() string               { return f.currentSupply }
func (f *FactoryState) PreviousSupply() string              { return f.previousSupply }
func (f *FactoryState) ReadyForCollection() bool            { return f.readyForCollection }
func (f *FactoryState) ReadyAt() *time.Time                 { return f.readyAt }
func (f *FactoryState) CreatedAt() time.Time                { return f.createdAt }

// SetID sets the database ID (used after persistence)
func (f *FactoryState) SetID(id int) { f.id = id }

// RecordDelivery marks an input as delivered
func (f *FactoryState) RecordDelivery(inputGood string, quantity int, shipSymbol string) error {
	state, exists := f.deliveredInputs[inputGood]
	if !exists {
		return fmt.Errorf("unknown input good %s for factory %s", inputGood, f.factorySymbol)
	}

	now := time.Now()
	state.Delivered = true
	state.DeliveredAt = &now
	state.DeliveredBy = shipSymbol
	state.Quantity = quantity

	// Check if all inputs are now delivered
	f.checkAllInputsDelivered()

	return nil
}

// checkAllInputsDelivered updates the allInputsDelivered flag
func (f *FactoryState) checkAllInputsDelivered() {
	for _, state := range f.deliveredInputs {
		if !state.Delivered {
			return
		}
	}

	f.allInputsDelivered = true
	now := time.Now()
	f.inputsCompletedAt = &now
}

// UpdateSupply updates the current supply level
func (f *FactoryState) UpdateSupply(supplyLevel string) {
	if f.currentSupply != "" && f.currentSupply != supplyLevel {
		f.previousSupply = f.currentSupply
	}
	f.currentSupply = supplyLevel

	// Check if now ready for collection
	f.checkReadyForCollection()
}

// checkReadyForCollection updates ready state based on supply
// NOTE: This is not sticky - if supply drops below HIGH, ready state is reset
// NOTE: Collection is allowed if supply is HIGH/ABUNDANT, regardless of whether
// we delivered inputs. This allows opportunistic collection from factories
// that already have high supply from other activity.
func (f *FactoryState) checkReadyForCollection() {
	currentLevel := SupplyLevelOrder[f.currentSupply]
	requiredLevel := SupplyLevelOrder[RequiredSupplyLevel]

	if currentLevel >= requiredLevel {
		// Supply is HIGH or ABUNDANT - ready for collection
		// Don't require our inputs to be delivered - factory may already have supply
		if !f.readyForCollection {
			f.readyForCollection = true
			now := time.Now()
			f.readyAt = &now
		}
	} else {
		// Supply dropped below HIGH - reset ready state
		// This prevents dispatching workers to factories with insufficient supply
		f.readyForCollection = false
		f.readyAt = nil
	}
}

// IsReadyForCollection returns true if factory output is ready to collect
func (f *FactoryState) IsReadyForCollection() bool {
	return f.readyForCollection
}

// GetMissingInputs returns the list of inputs not yet delivered
func (f *FactoryState) GetMissingInputs() []string {
	missing := make([]string, 0)
	for _, state := range f.deliveredInputs {
		if !state.Delivered {
			missing = append(missing, state.Good)
		}
	}
	return missing
}

// GetDeliveryProgress returns (delivered, total) input counts
func (f *FactoryState) GetDeliveryProgress() (int, int) {
	delivered := 0
	for _, state := range f.deliveredInputs {
		if state.Delivered {
			delivered++
		}
	}
	return delivered, len(f.requiredInputs)
}

// HasReceivedAnyDelivery returns true if at least one ingredient has been delivered.
// Used by streaming execution model to prevent premature collection before
// any production has occurred.
func (f *FactoryState) HasReceivedAnyDelivery() bool {
	for _, state := range f.deliveredInputs {
		if state.Delivered {
			return true
		}
	}
	return false
}

// String provides human-readable representation
func (f *FactoryState) String() string {
	delivered, total := f.GetDeliveryProgress()
	return fmt.Sprintf("Factory[%s, output=%s, inputs=%d/%d, supply=%s, ready=%t]",
		f.factorySymbol, f.outputGood, delivered, total, f.currentSupply, f.readyForCollection)
}

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

// InitFactory registers a new factory for production tracking
func (t *FactoryStateTracker) InitFactory(
	factorySymbol string,
	outputGood string,
	pipelineID string,
	playerID int,
	requiredInputs []string,
) *FactoryState {
	t.mu.Lock()
	defer t.mu.Unlock()

	key := t.makeKey(pipelineID, factorySymbol, outputGood)
	state := NewFactoryState(factorySymbol, outputGood, pipelineID, playerID, requiredInputs)
	t.states[key] = state
	return state
}

// GetState returns the factory state for a specific factory/good/pipeline
func (t *FactoryStateTracker) GetState(pipelineID, factorySymbol, outputGood string) *FactoryState {
	t.mu.RLock()
	defer t.mu.RUnlock()

	key := t.makeKey(pipelineID, factorySymbol, outputGood)
	return t.states[key]
}

// LoadState loads an existing factory state into the tracker
func (t *FactoryStateTracker) LoadState(state *FactoryState) {
	t.mu.Lock()
	defer t.mu.Unlock()

	key := t.makeKey(state.PipelineID(), state.FactorySymbol(), state.OutputGood())
	t.states[key] = state
}

// RecordDelivery records an input delivery to a factory
func (t *FactoryStateTracker) RecordDelivery(
	pipelineID string,
	factorySymbol string,
	outputGood string,
	inputGood string,
	quantity int,
	shipSymbol string,
) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	key := t.makeKey(pipelineID, factorySymbol, outputGood)
	state, exists := t.states[key]
	if !exists {
		return fmt.Errorf("no factory state for %s producing %s in pipeline %s",
			factorySymbol, outputGood, pipelineID)
	}

	return state.RecordDelivery(inputGood, quantity, shipSymbol)
}

// UpdateSupply updates the supply level for a factory
func (t *FactoryStateTracker) UpdateSupply(
	pipelineID string,
	factorySymbol string,
	outputGood string,
	supplyLevel string,
) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	key := t.makeKey(pipelineID, factorySymbol, outputGood)
	state, exists := t.states[key]
	if !exists {
		return fmt.Errorf("no factory state for %s producing %s in pipeline %s",
			factorySymbol, outputGood, pipelineID)
	}

	state.UpdateSupply(supplyLevel)
	return nil
}

// IsReadyForCollection checks if a factory is ready for collection
func (t *FactoryStateTracker) IsReadyForCollection(pipelineID, factorySymbol, outputGood string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	key := t.makeKey(pipelineID, factorySymbol, outputGood)
	state, exists := t.states[key]
	if !exists {
		return false
	}

	return state.IsReadyForCollection()
}

// GetReadyFactories returns all factories ready for collection
func (t *FactoryStateTracker) GetReadyFactories() []*FactoryState {
	t.mu.RLock()
	defer t.mu.RUnlock()

	ready := make([]*FactoryState, 0)
	for _, state := range t.states {
		if state.IsReadyForCollection() {
			ready = append(ready, state)
		}
	}
	return ready
}

// GetFactoriesAwaitingProduction returns factories with all inputs delivered but not ready
func (t *FactoryStateTracker) GetFactoriesAwaitingProduction() []*FactoryState {
	t.mu.RLock()
	defer t.mu.RUnlock()

	awaiting := make([]*FactoryState, 0)
	for _, state := range t.states {
		if state.AllInputsDelivered() && !state.IsReadyForCollection() {
			awaiting = append(awaiting, state)
		}
	}
	return awaiting
}

// GetFactoriesNotReady returns all factories not yet ready for collection
// This includes factories regardless of whether inputs have been delivered
func (t *FactoryStateTracker) GetFactoriesNotReady() []*FactoryState {
	t.mu.RLock()
	defer t.mu.RUnlock()

	notReady := make([]*FactoryState, 0)
	for _, state := range t.states {
		if !state.IsReadyForCollection() {
			notReady = append(notReady, state)
		}
	}
	return notReady
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

// GetFactoriesByPipeline returns all factory states for a pipeline
func (t *FactoryStateTracker) GetFactoriesByPipeline(pipelineID string) []*FactoryState {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]*FactoryState, 0)
	for _, state := range t.states {
		if state.PipelineID() == pipelineID {
			result = append(result, state)
		}
	}
	return result
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

// Count returns the total number of tracked factory states
func (t *FactoryStateTracker) Count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.states)
}

// ReconstituteFactoryState creates a factory state from persisted data (for repository use only)
func ReconstituteFactoryState(
	id int,
	factorySymbol string,
	outputGood string,
	pipelineID string,
	playerID int,
	requiredInputs []string,
	deliveredInputs map[string]*InputState,
	allInputsDelivered bool,
	currentSupply string,
	previousSupply string,
	readyForCollection bool,
	createdAt time.Time,
	inputsCompletedAt *time.Time,
	readyAt *time.Time,
) *FactoryState {
	return &FactoryState{
		id:                 id,
		factorySymbol:      factorySymbol,
		outputGood:         outputGood,
		pipelineID:         pipelineID,
		playerID:           playerID,
		requiredInputs:     requiredInputs,
		deliveredInputs:    deliveredInputs,
		allInputsDelivered: allInputsDelivered,
		currentSupply:      currentSupply,
		previousSupply:     previousSupply,
		readyForCollection: readyForCollection,
		createdAt:          createdAt,
		inputsCompletedAt:  inputsCompletedAt,
		readyAt:            readyAt,
	}
}
