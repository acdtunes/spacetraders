package trading

import "time"

// cash_rate.go — the DEFINITIVE transactions-cash realized $/hr (sp-rd21, epic sp-g9td).
//
// This is the cash-true twin of the telemetry-netting rate (ComputeFleetTourRate /
// MedianTourRate). The 12h reconciliation that opened sp-g9td proved the telemetry-netting
// rate reads ~2x inflated because ~1/3 of buy legs were dropped from tour_leg_telemetry
// (their destination sells were still logged), so telemetry-net = sells − (partial buys)
// over-counts profit. The transactions ledger, by contrast, records EVERY cargo trade and
// reconciles to the treasury — SELL_CARGO(+) + PURCHASE_CARGO(−) + REFUEL(−) summed over a
// window IS the cash the fleet actually earned. Dividing by the window's wall-clock hours
// (not the active-tour span) yields the duty-cycle $/hr — idle time between tours counts
// against the rate, exactly as it should for a steering KPI.
//
// sp-rd21 delivers this computation; the sibling sp-461l switches the consumers
// (reposition rate floor / placement β, autosizer realized-rate + era-payback guards,
// chain-P&L, dashboards) from the telemetry-netting rate onto it. The persistence reader
// GormTransactionRepository.RealizedCashRate performs the windowed SQL sum and defers here
// for the arithmetic, so the SQL read and the rate math are independently testable — the
// same split ComputeFleetTourRate uses against its repository port.

// CashRealizedRate is the transactions-cash realized-$/hr summary over a window.
type CashRealizedRate struct {
	// NetCredits is the signed sum of amount over SELL_CARGO(+), PURCHASE_CARGO(−) and
	// REFUEL(−) transactions in the window — cash in minus cash out, treasury-true.
	NetCredits int64
	// TxCount is the number of cargo/refuel transactions summed. Zero ⇒ Readable false
	// (an empty window carries no rate to steer on — fail closed, RULINGS #4).
	TxCount int
	// WindowHours is the window's wall-clock span (now − since) in hours; the divisor.
	WindowHours float64
	// CreditsPerHour is NetCredits / WindowHours (0 when not Readable). May be negative —
	// a net-loss window is a genuine, steerable signal, not an error.
	CreditsPerHour float64
	// Readable is true iff TxCount>0 AND WindowHours>0. false ⇒ callers fail closed,
	// mirroring FleetTourRateResult.Readable — a readable zero is never invented.
	Readable bool
}

// ComputeCashRealizedRate derives the cash $/hr from a windowed net-credits total, the
// count of transactions summed, and the window duration. Pure and fail-closed: an empty
// window (txCount<=0) or a non-positive span yields Readable=false with a zero rate, never
// a fabricated 0/hr — the same contract ComputeFleetTourRate keeps so a consumer that
// cannot see the economics does not steer on an invented number.
func ComputeCashRealizedRate(netCredits int64, txCount int, window time.Duration) CashRealizedRate {
	hours := window.Hours()
	result := CashRealizedRate{
		NetCredits:  netCredits,
		TxCount:     txCount,
		WindowHours: hours,
	}
	if txCount <= 0 || hours <= 0 {
		return result // fail closed: no data / no span ⇒ Readable false, rate 0
	}
	result.CreditsPerHour = float64(netCredits) / hours
	result.Readable = true
	return result
}
