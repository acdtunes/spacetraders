package storage

import (
	"context"
	"time"
)

// StockingEvent is a structured, queryable record of a single stocker→warehouse buffer
// DEPOSIT — the stock-IN mirror of the WithdrawalEvent draw. A deposit is a
// NON-monetary cargo transfer (the goods' cost basis is booked at the buy, in the financial
// ledger's PURCHASE_CARGO row; the deposit itself moves credits nowhere), so — exactly like
// the withdrawal — it does not belong in the financial ledger and is instead its own
// economic event, emitted once per CONFIRMED deposit so downstream analysis can measure the
// depot's stock-IN throughput and coverage:
//   - units-stocked            = SUM(Units)
//   - goods-covered            = COUNT(DISTINCT Good) per warehouse
//   - source-provenance        = which foreign/home market each buffered unit came from
//
// Paired with the WithdrawalEvent stream, the two flows difference to an event-sourced view
// of current warehouse fill (Σ deposits − Σ draws), observable WITHOUT depending on the
// (stale, for stationary depot hulls) ship cargo sync.
//
// The domain speaks this DTO; the persistence layer maps it to its own row model, keeping the
// application decoupled from GORM (the same dependency-inversion the withdrawal-event and
// storage-operation channels already use).
type StockingEvent struct {
	Good              string    // trade symbol deposited into the warehouse buffer
	Units             int       // units deposited on this transfer
	WarehouseWaypoint string    // warehouse location the goods were deposited into
	SourceWaypoint    string    // market the goods were bought from; "" when unknown (a resume deposit of prior-run cargo)
	Ship              string    // stocking hauler symbol
	PlayerID          int       // owning player
	DepositedAt       time.Time // when the deposit completed
}

// StockingRecorder persists stocker→warehouse deposit events and reads them back for
// stock-IN throughput/coverage analysis. Implemented by the persistence layer; consumed as a
// driven port by the stocker coordinator (the deposit call site).
type StockingRecorder interface {
	// Record persists one stocker→warehouse deposit event. Called on the actual CONFIRMED
	// deposit, never on intent.
	Record(ctx context.Context, event StockingEvent) error

	// ListByPlayer returns playerID's deposit events whose DepositedAt is at or after since,
	// in insertion order (a zero since returns the full history).
	ListByPlayer(ctx context.Context, playerID int, since time.Time) ([]StockingEvent, error)
}
