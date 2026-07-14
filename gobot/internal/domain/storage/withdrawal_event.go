package storage

import (
	"context"
	"time"
)

// WithdrawalEvent is a structured, queryable record of a single warehouse→hauler
// buffer draw (sp-kqxe). A warehouse withdrawal is a NON-monetary cargo transfer
// (the goods' cost basis is sunk at deposit; the draw itself costs zero), so it
// does not belong in the financial ledger — a zero-amount Transaction violates the
// ledger's balance invariant. Instead it is its own economic event, emitted once
// per successful draw so downstream analysis can measure warehouse ROI:
//   - buffer hit-rate           = withdrawal events / (events + fresh market sources)
//   - served-from-buffer units  = SUM(Units)
//   - contract-leg-avoided      = COUNT(events with a non-empty ContractID)
//
// The domain speaks this DTO; the persistence layer maps it to its own row model,
// keeping the application decoupled from GORM (the same dependency-inversion the
// tour-telemetry and storage-operation channels already use).
type WithdrawalEvent struct {
	Good        string    // trade symbol drawn from the warehouse buffer
	Units       int       // units withdrawn on this draw
	Waypoint    string    // warehouse location the goods were drawn from
	Ship        string    // withdrawing hauler symbol
	ContractID  string    // contract being served; "" when the draw serves no contract
	PlayerID    int       // owning player
	WithdrawnAt time.Time // when the draw completed
}

// WithdrawalRecorder persists warehouse→hauler withdrawal events and reads them
// back for warehouse-ROI analysis. Implemented by the persistence layer; consumed
// as a driven port by the contract delivery executor (the withdrawal call site).
type WithdrawalRecorder interface {
	// Record persists one warehouse→hauler withdrawal event. Called on the actual
	// successful draw, never on intent.
	Record(ctx context.Context, event WithdrawalEvent) error

	// ListByPlayer returns playerID's withdrawal events whose WithdrawnAt is at or
	// after since, in insertion order (a zero since returns the full history).
	ListByPlayer(ctx context.Context, playerID int, since time.Time) ([]WithdrawalEvent, error)
}
