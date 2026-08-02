package parkedsensing

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/probebuy"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ---- TreasuryPort -----------------------------------------------------------

// TreasuryPort adapts the shared live-treasury reader — the same one every
// other money guard reads, whose cache is invalidated on each credit-decreasing
// call — to the engine's plain-int player identity.
type TreasuryPort struct{ reader probebuy.TreasuryReader }

// NewTreasuryPort wires the live-treasury read.
func NewTreasuryPort(reader probebuy.TreasuryReader) *TreasuryPort {
	return &TreasuryPort{reader: reader}
}

// LiveCredits returns the player's current spendable balance.
func (p *TreasuryPort) LiveCredits(ctx context.Context, playerID int) (int64, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, err
	}
	credits, err := p.reader.LiveCredits(sensingCtx(ctx), pid)
	if err != nil {
		return 0, err
	}
	return int64(credits), nil
}

// ---- CargoSpendPort ---------------------------------------------------------

// CargoSpendPort measures the trading fleet's recent cargo outflow from the
// persisted ledger, which is what makes the probe-buy floor rise when the fleet
// is busy. Derived fresh from the ledger on every call, so it survives a restart
// with no in-memory counter to lose.
type CargoSpendPort struct{ txns ledger.TransactionRepository }

// NewCargoSpendPort wires the ledger-derived cargo-spend read.
func NewCargoSpendPort(txns ledger.TransactionRepository) *CargoSpendPort {
	return &CargoSpendPort{txns: txns}
}

// AbsCargoBuySpendSince sums cargo-purchase spend booked since `since`.
//
// Ledger expenses are stored NEGATIVE. The absolute value of each row is taken
// individually rather than negating the sum, so a stray positive row (a refund,
// a correction) still ADDS to measured outflow instead of cancelling real spend
// out of it — the floor is a guard, and a malformed row must only ever make it
// stricter.
func (p *CargoSpendPort) AbsCargoBuySpendSince(ctx context.Context, playerID int, since time.Time) (int64, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, err
	}
	cargo := ledger.TransactionTypePurchaseCargo
	rows, err := p.txns.FindByPlayer(ctx, pid, ledger.QueryOptions{
		TransactionType: &cargo,
		StartDate:       &since,
		Limit:           cargoSpendScan,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to read recent cargo spend: %w", err)
	}
	var sum int64
	for _, row := range rows {
		amount := int64(row.Amount())
		if amount < 0 {
			amount = -amount
		}
		sum += amount
	}
	return sum, nil
}
