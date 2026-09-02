package mvt

import (
	"context"
	"time"
)

// State is a hull-loop state. Transitions between them are the loop's only telemetry.
type State string

const (
	StateTrade  State = "TRADE"
	StateClaim  State = "CLAIM"
	StateTravel State = "TRAVEL"
)

// Transition is one state change with the numbers that caused it — exactly the fields the
// spec names: hull, from_state, to_state, system, yield_here, best_alternative, travel_cost, reason.
type Transition struct {
	PlayerID        int
	Hull            string
	From, To        State
	System          string
	YieldHere       float64
	BestAlternative float64
	TravelCost      float64
	Reason          string
	At              time.Time
}

// TransitionRecorder persists transitions. Recording must never block a hull: callers log
// and continue on error.
type TransitionRecorder interface {
	Record(ctx context.Context, t Transition) error
}
