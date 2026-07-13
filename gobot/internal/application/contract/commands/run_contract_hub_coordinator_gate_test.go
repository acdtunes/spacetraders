package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- Acceptance #5 (Phase-2 regression seam): the re-home cost-gate is a PURE function,
// testable in isolation. It rejects a re-home unless the durable savings clear the trip cost
// plus a hysteresis margin — so a one-tick price flip (tiny savings) never triggers a move. ---

// A one-tick price flip yields a small per-contract saving that, even over the expected
// remaining contracts, does not clear the repositioning cost + hysteresis → NO re-home.
func TestShouldRehome_OneTickFlipDoesNotRehome(t *testing.T) {
	got := ShouldRehome(RehomeDecision{
		SavingsPerContract:         5,   // marginal buy-leg saving of H_alt over H_cur (a tick's worth)
		ExpectedRemainingContracts: 10,  // 5 × 10 = 50 total saving
		RepositionCost:             100, // moving costs more than the saving
		HysteresisMargin:           50,
	})
	assert.False(t, got, "a one-tick flip must never trigger a re-home")
}

// A durable structural shift (large, persistent per-contract saving over many remaining
// contracts) clears the trip cost + hysteresis → re-home permitted.
func TestShouldRehome_DurableShiftRehomes(t *testing.T) {
	got := ShouldRehome(RehomeDecision{
		SavingsPerContract:         80, // 80 × 20 = 1600 total saving
		ExpectedRemainingContracts: 20,
		RepositionCost:             100,
		HysteresisMargin:           50,
	})
	assert.True(t, got, "a durable, large saving over many contracts justifies the move")
}

// The hysteresis margin is the tie-breaker band: a saving that exactly covers the trip cost but
// not the margin is rejected (prevents oscillation around break-even).
func TestShouldRehome_HysteresisMarginRejectsBreakEven(t *testing.T) {
	got := ShouldRehome(RehomeDecision{
		SavingsPerContract:         10,
		ExpectedRemainingContracts: 10, // 100 total saving == trip cost, but not > cost + margin
		RepositionCost:             100,
		HysteresisMargin:           25,
	})
	assert.False(t, got, "break-even (no margin cleared) must not re-home")
}

// Zero expected remaining contracts → no future benefit → never re-home.
func TestShouldRehome_NoRemainingContractsNeverRehomes(t *testing.T) {
	got := ShouldRehome(RehomeDecision{
		SavingsPerContract:         1000,
		ExpectedRemainingContracts: 0,
		RepositionCost:             1,
		HysteresisMargin:           0,
	})
	assert.False(t, got, "with no contracts left this era there is nothing to amortize the move against")
}
