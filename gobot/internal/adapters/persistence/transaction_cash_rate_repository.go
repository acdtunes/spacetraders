package persistence

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// transaction_cash_rate_repository.go — the DEFINITIVE transactions-cash realized $/hr
// reader. It is the cash-true counterpart of the telemetry read
// the autosizer uses (fleet_autosizer_ports.FleetTourRate → trading.ComputeFleetTourRate):
// a thin windowed SQL sum over the transactions ledger + a deferral to the pure
// trading.ComputeCashRealizedRate for the arithmetic.
//
// It sums the signed amount over the three cash-flow transaction types a trade round-trip
// produces — SELL_CARGO (income, +), PURCHASE_CARGO (cost, −) and REFUEL (cost, −) — so the
// result reconciles to the treasury, unlike the telemetry-netting rate found ~2x
// inflated (it dropped ~1/3 of buy legs). The window is [since, now); the rate divides by
// the FULL wall-clock span so idle time between tours counts against the duty-cycle $/hr.
//
// It filters on created_at (the ledger ingestion time) — the same column the sibling
// transactions-cash reader GormChainPnLRepository.ReadRealizedPnL scopes on, so consumers can
// switch onto a consistent window basis.

// cashRateTransactionTypes are the ledger transaction types that constitute a trade
// round-trip's realized cash flow. amount is stored signed (income +, expense −), so a
// plain SUM(amount) over these types is the net cash the fleet earned in the window.
var cashRateTransactionTypes = []string{"SELL_CARGO", "PURCHASE_CARGO", "REFUEL"}

// RealizedCashRate returns the player's transactions-cash realized $/hr over [since, now).
// operationType scopes to a single operation (e.g. "tour" for the drop-in replacement of
// the tour telemetry-netting rate the autosizer/placement guards read); pass "" to include
// EVERY operation (the whole-fleet cash rate a dashboard wants). now is passed in (not read
// from a clock) so the window span is explicit and the read is deterministically testable.
// A read error is returned verbatim; an empty window yields a fail-closed (Readable=false)
// CashRealizedRate, never a fabricated 0/hr.
func (r *GormTransactionRepository) RealizedCashRate(ctx context.Context, playerID int, since, now time.Time, operationType string) (trading.CashRealizedRate, error) {
	query := r.db.WithContext(ctx).
		Model(&TransactionModel{}).
		Where("player_id = ?", playerID).
		Where("transaction_type IN ?", cashRateTransactionTypes).
		Where("created_at >= ?", since).
		Where("created_at < ?", now)
	if operationType != "" {
		query = query.Where("operation_type = ?", operationType)
	}

	var agg struct {
		Net int64
		Cnt int64
	}
	if err := query.Select("COALESCE(SUM(amount), 0) AS net, COUNT(*) AS cnt").Scan(&agg).Error; err != nil {
		return trading.CashRealizedRate{}, fmt.Errorf("read realized cash rate for player %d: %w", playerID, err)
	}

	return trading.ComputeCashRealizedRate(agg.Net, int(agg.Cnt), now.Sub(since)), nil
}
