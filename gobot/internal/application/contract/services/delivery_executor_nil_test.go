package services

import (
	"strings"
	"testing"

	contractQueries "github.com/andrescamacho/spacetraders-go/internal/application/contract/queries"
)

// A nil (or mistyped) profitability response used to be blindly type-asserted,
// panicking the whole daemon mid-contract when no market data existed yet.
func TestProfitabilityResultOrErrNil(t *testing.T) {
	if _, err := profitabilityResultOrErr(nil, "IRON_ORE"); err == nil {
		t.Fatal("expected error for nil profitability response")
	} else if !strings.Contains(err.Error(), "IRON_ORE") {
		t.Fatalf("error should name the good: %v", err)
	}
}

func TestProfitabilityResultOrErrTypedNil(t *testing.T) {
	var typedNil *contractQueries.ProfitabilityResult
	if _, err := profitabilityResultOrErr(typedNil, "IRON_ORE"); err == nil {
		t.Fatal("expected error for typed-nil profitability response")
	}
}

func TestProfitabilityResultOrErrValid(t *testing.T) {
	res := &contractQueries.ProfitabilityResult{}
	got, err := profitabilityResultOrErr(res, "IRON_ORE")
	if err != nil || got != res {
		t.Fatalf("expected passthrough, got %v err %v", got, err)
	}
}
