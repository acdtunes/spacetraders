package manufacturing

import (
	"fmt"
	"time"
)

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

// Getters

func (f *FactoryState) ID() int                                 { return f.id }
func (f *FactoryState) FactorySymbol() string                   { return f.factorySymbol }
func (f *FactoryState) OutputGood() string                      { return f.outputGood }
func (f *FactoryState) PipelineID() string                      { return f.pipelineID }
func (f *FactoryState) PlayerID() int                           { return f.playerID }
func (f *FactoryState) RequiredInputs() []string                { return f.requiredInputs }
func (f *FactoryState) DeliveredInputs() map[string]*InputState { return f.deliveredInputs }
func (f *FactoryState) AllInputsDelivered() bool                { return f.allInputsDelivered }
func (f *FactoryState) InputsCompletedAt() *time.Time           { return f.inputsCompletedAt }
func (f *FactoryState) CurrentSupply() string                   { return f.currentSupply }
func (f *FactoryState) PreviousSupply() string                  { return f.previousSupply }
func (f *FactoryState) ReadyForCollection() bool                { return f.readyForCollection }
func (f *FactoryState) ReadyAt() *time.Time                     { return f.readyAt }
func (f *FactoryState) CreatedAt() time.Time                    { return f.createdAt }

// SetID sets the database ID (used after persistence)
func (f *FactoryState) SetID(id int) { f.id = id }

// RecordDelivery marks an input as delivered. If inputGood is not a known
// required input, it is still recorded rather than rejected - the API may have
// updated factory configuration since local state was built - and tracked
// dynamically without being added to requiredInputs, which would change the
// contract.
func (f *FactoryState) RecordDelivery(inputGood string, quantity int, shipSymbol string) error {
	state, exists := f.deliveredInputs[inputGood]
	if !exists {
		state = &InputState{
			Good:      inputGood,
			Delivered: false,
		}
		f.deliveredInputs[inputGood] = state
	}

	now := time.Now()
	state.Delivered = true
	state.DeliveredAt = &now
	state.DeliveredBy = shipSymbol
	state.Quantity = quantity

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
	f.inputsCompletedAt = nowPtr()
}

// UpdateSupply updates the current supply level
func (f *FactoryState) UpdateSupply(supplyLevel string) {
	if f.currentSupply != "" && f.currentSupply != supplyLevel {
		f.previousSupply = f.currentSupply
	}
	f.currentSupply = supplyLevel

	f.checkReadyForCollection()
}

// checkReadyForCollection updates ready state based on supply
// NOTE: This is not sticky - if supply drops below HIGH, ready state is reset
// NOTE: Collection is allowed if supply is HIGH/ABUNDANT, regardless of whether
// we delivered inputs. This allows opportunistic collection from factories
// that already have high supply from other activity.
func (f *FactoryState) checkReadyForCollection() {
	if SupplyLevel(f.currentSupply).IsFavorableForCollection() {
		// Supply is HIGH or ABUNDANT - ready for collection
		// Don't require our inputs to be delivered - factory may already have supply
		if !f.readyForCollection {
			f.readyForCollection = true
			f.readyAt = nowPtr()
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
