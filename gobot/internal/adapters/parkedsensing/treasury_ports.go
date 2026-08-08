package parkedsensing

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/probebuy"
	"github.com/andrescamacho/spacetraders-go/internal/domain/fleetgrowth"
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

// CargoOutflowSince reports ONE trailing window of the trading fleet's cargo ledger: what it spent
// on cargo, what it recovered by selling cargo, the largest single purchase, and whether the reader
// saw the whole window.
//
// BOTH SIDES, BECAUSE SPEND ALONE MEASURES TURNOVER RATHER THAN CAPITAL: the same credits are spent
// again on every buy→sell→buy recycle, so a gross figure grows with how WELL the fleet trades. The
// difference between the two sides is the position it has not got back yet.
//
// RECOVERY IS READ FIRST, SPEND SECOND. Two reads cannot be atomic, so order decides which way the
// gap errs: with sales already read, a sale landing in it is missed and a purchase landing in it is
// counted, and both leave the position HIGHER than the truth. Reversed, an active fleet would read
// low exactly when it is busiest. The spend statistics come from ONE pass over one row set for the
// same reason — the two working-capital terms are a max() over each other.
//
// THE LARGEST SINGLE ROW IS THE PER-HULL SCALE: the ledger carries no ship symbol, so a per-hull
// mean is not derivable here, and an outlier can only raise the largest — the strict direction.
//
// THE TWO SIDES TAKE OPPOSITE RULES ON A MALFORMED ROW, both pointing at the guard. Expenses are
// stored NEGATIVE and each spend row's absolute value is taken individually, so a stray positive
// row ADDS to outflow rather than cancelling real spend. On the recovery side that same absolute
// value would subtract a PHANTOM recovery, so a row that is not genuine income is dropped.
//
// COMPLETE IS THE READER'S VERDICT ON ITS WINDOW: false as soon as either side reaches the scan
// bound, because a difference netted across a half-seen window is understated by what fell outside
// it. The port reports the truncation; fleetgrowth.WorkingCapital decides what to do about it.
func (p *CargoSpendPort) CargoOutflowSince(ctx context.Context, playerID int, since time.Time) (fleetgrowth.CargoOutflow, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return fleetgrowth.CargoOutflow{}, err
	}

	sells, err := p.scanCargoRows(ctx, pid, ledger.TransactionTypeSellCargo, since)
	if err != nil {
		return fleetgrowth.CargoOutflow{}, fmt.Errorf("failed to read recent cargo recovery: %w", err)
	}
	buys, err := p.scanCargoRows(ctx, pid, ledger.TransactionTypePurchaseCargo, since)
	if err != nil {
		return fleetgrowth.CargoOutflow{}, fmt.Errorf("failed to read recent cargo outflow: %w", err)
	}

	out := fleetgrowth.CargoOutflow{Complete: len(buys) < cargoSpendScan && len(sells) < cargoSpendScan}
	for _, row := range buys {
		amount := int64(row.Amount())
		if amount < 0 {
			amount = -amount
		}
		out.Spent += amount
		if amount > out.Largest {
			out.Largest = amount
		}
	}
	for _, row := range sells {
		if amount := int64(row.Amount()); amount > 0 {
			out.Recovered += amount
		}
	}
	return out, nil
}

// scanCargoRows reads one side of the cargo window. Both sides go through it so the window and the
// scan bound cannot drift apart — a difference measured over two windows is not a difference.
func (p *CargoSpendPort) scanCargoRows(
	ctx context.Context, pid shared.PlayerID, txnType ledger.TransactionType, since time.Time,
) ([]*ledger.Transaction, error) {
	return p.txns.FindByPlayer(ctx, pid, ledger.QueryOptions{
		TransactionType: &txnType,
		StartDate:       &since,
		Limit:           cargoSpendScan,
	})
}
