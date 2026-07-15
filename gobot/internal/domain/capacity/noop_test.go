package capacity

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// NoOpProposalChannel must FAIL LOUD if invoked. st-0h8 replaces it in the
// daemon wiring with the real ProposalChannel, but it stays the safety default
// for any partially-assembled engine: an accidental Submit on an un-wired
// channel must surface loudly, never silently "succeed" and drop a treasury
// proposal on the floor. This pins that guarantee against a future refactor that
// might make the NoOp quietly return nil.
func TestNoOpProposalChannel_FailsLoudOnSubmit(t *testing.T) {
	err := NoOpProposalChannel{}.Submit(context.Background(), Proposal{
		ID:       "prop-1",
		PlayerID: 7,
		Action:   Action{Tier: TierCapital, Verb: VerbBuyHull, EstimatedCostCredits: 120000},
	})

	require.Error(t, err, "an un-wired proposal channel must fail loud, never pretend success")
	require.Contains(t, err.Error(), "not wired")
}
