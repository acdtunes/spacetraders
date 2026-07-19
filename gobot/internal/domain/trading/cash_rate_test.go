package trading

import (
	"math"
	"testing"
	"time"
)

// A representative reconciliation case: a window's true cash net of ~66.4M over 12h
// is ~5.53M/hr.
func TestComputeCashRealizedRate_WindowedRate(t *testing.T) {
	got := ComputeCashRealizedRate(66_400_000, 2061, 12*time.Hour)

	if !got.Readable {
		t.Fatalf("a window with transactions and a positive span must be readable")
	}
	if got.NetCredits != 66_400_000 || got.TxCount != 2061 {
		t.Fatalf("net/count passthrough = %d/%d, want 66400000/2061", got.NetCredits, got.TxCount)
	}
	if math.Abs(got.WindowHours-12) > 1e-9 {
		t.Fatalf("window hours = %v, want 12", got.WindowHours)
	}
	wantRate := 66_400_000.0 / 12.0 // ≈ 5,533,333.33/hr
	if math.Abs(got.CreditsPerHour-wantRate) > 1e-6 {
		t.Fatalf("credits/hour = %v, want %v", got.CreditsPerHour, wantRate)
	}
}

// A net-loss window is a genuine, steerable signal — a negative rate, still Readable.
func TestComputeCashRealizedRate_NegativeNetIsReadable(t *testing.T) {
	got := ComputeCashRealizedRate(-3_600_000, 40, 2*time.Hour)

	if !got.Readable {
		t.Fatalf("a net-loss window must be readable (a real signal, not an error)")
	}
	if got.CreditsPerHour != -1_800_000 {
		t.Fatalf("credits/hour = %v, want -1800000", got.CreditsPerHour)
	}
}

// Fail closed on an empty window: no transactions ⇒ Readable false, zero rate (never a
// fabricated 0/hr), mirroring ComputeFleetTourRate's contract.
func TestComputeCashRealizedRate_EmptyWindowFailsClosed(t *testing.T) {
	got := ComputeCashRealizedRate(0, 0, 6*time.Hour)

	if got.Readable {
		t.Fatalf("an empty window (0 transactions) must fail closed, not report a readable 0/hr")
	}
	if got.CreditsPerHour != 0 {
		t.Fatalf("credits/hour = %v, want 0 on a fail-closed empty window", got.CreditsPerHour)
	}
}

// Fail closed on a non-positive span: a zero (or inverted) window has no divisor, so no
// rate — even if transactions were counted.
func TestComputeCashRealizedRate_NonPositiveSpanFailsClosed(t *testing.T) {
	got := ComputeCashRealizedRate(5_000_000, 10, 0)

	if got.Readable {
		t.Fatalf("a non-positive window span must fail closed")
	}
	if got.CreditsPerHour != 0 {
		t.Fatalf("credits/hour = %v, want 0 on a zero-span window", got.CreditsPerHour)
	}
}
