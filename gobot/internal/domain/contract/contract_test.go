package contract

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// mustNewPayoutTestContract builds a minimal valid contract with the given
// payment terms - only Payment varies since TotalPayout only reads that field.
func mustNewPayoutTestContract(t *testing.T, onAccepted, onFulfilled int) *Contract {
	t.Helper()
	terms := Terms{
		Payment: Payment{OnAccepted: onAccepted, OnFulfilled: onFulfilled},
		Deliveries: []Delivery{
			{TradeSymbol: "ALUMINUM", DestinationSymbol: "X1-TEST-A1", UnitsRequired: 10, UnitsFulfilled: 0},
		},
		DeadlineToAccept: "2026-01-01T00:00:00Z",
		Deadline:         "2026-01-01T00:00:00Z",
	}
	c, err := NewContract("contract-payout-test", shared.MustNewPlayerID(1), "COSMIC", "PROCUREMENT", terms, nil)
	if err != nil {
		t.Fatalf("NewContract: %v", err)
	}
	return c
}

// TotalPayout is the gross credit value the value-floor gate (sp-snmb, see
// RunWorkflowHandler) compares against a configured minimum before letting
// the workflow accept a freshly negotiated contract: the upfront
// on-accepted payment plus the on-fulfilled completion payment, regardless
// of the contract's current accepted/fulfilled state.
func TestContract_TotalPayout(t *testing.T) {
	c := mustNewPayoutTestContract(t, 1000, 1500)

	if got := c.TotalPayout(); got != 2500 {
		t.Fatalf("TotalPayout() = %d, want 2500 (1000 OnAccepted + 1500 OnFulfilled)", got)
	}
}

func TestContract_TotalPayout_ZeroPayment(t *testing.T) {
	c := mustNewPayoutTestContract(t, 0, 0)

	if got := c.TotalPayout(); got != 0 {
		t.Fatalf("TotalPayout() = %d, want 0", got)
	}
}
