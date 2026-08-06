package navigation

// jump_ship_fee_operation_type_test.go is the LEAF half of sp-wxgd2's contract, driven
// end-to-end through the real RecordTransactionHandler and a real (constraint-enforcing)
// test DB: whatever operation context a coordinator stamps on ctx must land in the
// persisted row's operation_type, and only a genuinely unstamped caller may fall through
// to 'manual'.
//
// This is the assertion the sensing/scouting fix depends on and cannot make for itself:
// those coordinators live in another package and can only prove that the context reaches
// the port that issues the jump (scouting/commands/scouting_operation_attribution_test.go).
// What happens to it AFTER that is decided here, in the else branch that produced 113
// unattributed jump fees worth 579,597 credits.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// jumpUnder drives the harness's jump on a caller-supplied context. It deliberately does
// NOT reuse jumpLedgerHarness.jump, whose hardcoded context.Background() is precisely the
// unstamped case this file has to distinguish from a stamped one.
func jumpUnder(t *testing.T, h *jumpLedgerHarness, ctx context.Context) {
	t.Helper()
	pid := h.playerID.Value()
	_, err := h.handler.Handle(ctx, &JumpShipCommand{
		ShipSymbol:        "PROBE-1",
		DestinationSystem: "X1-CD34",
		PlayerID:          &pid,
	})
	require.NoError(t, err)
}

// A jump made by an attributed coordinator books its gate fee to that coordinator.
//
// The two operation names below are the live ones the sensing and scouting coordinators
// stamp (parkedsensing.SensingCoverageOperationType and the scouting package's own
// constants). They are written as literals rather than imported so this file states the
// exact strings the ledger and its historical backfill must agree on — an import would
// make the test pass through any rename, which is the one failure mode that silently
// splits a subsystem's costs across two operation_types.
func TestJumpFee_StampedOperationContextIsRecorded(t *testing.T) {
	const startingCredits, fee = 100000, 5343

	for name, opType := range map[string]string{
		"the parked-sensing engine": "sensing coverage",
		"a scout-post relay":        "scout reposition",
	} {
		t.Run(name, func(t *testing.T) {
			const containerID = "probe_sensing_coordinator-player-7"
			h := newJumpLedgerHarness(t, startingCredits, fee)

			jumpUnder(t, h, shared.WithOperationContext(context.Background(),
				shared.NewOperationContext(containerID, opType)))

			require.Equal(t, 1, h.api.jumps, "precondition: the fake server actually charged one gate fee")
			row := h.latest(t)
			require.Equal(t, string(ledger.TransactionTypeJump), string(row.TransactionType()),
				"precondition: the row under assertion is the JUMP fee")
			require.Equal(t, opType, row.OperationType(),
				"the gate fee must be booked to the operation that spent it, not to 'manual'")
			require.NotEqual(t, "manual", row.OperationType())
			require.Equal(t, containerID, row.RelatedEntityID(),
				"and must name the container, so per-operation P&L can join back to it")
			require.Equal(t, "container", row.RelatedEntityType())
			require.Equal(t, -fee, row.Amount(), "attribution only: the recorded spend is unchanged")
		})
	}
}

// AND THE ELSE BRANCH STILL MEANS WHAT IT SAYS. An operator jumping a hull by hand runs
// on an unstamped context and must still book 'manual' — the fix closes a propagation
// gap, it does not relabel human actions as automated. Without this control the test
// above would also pass against an implementation that hardcoded the operation type.
func TestJumpFee_UnstampedJumpStaysManual(t *testing.T) {
	const startingCredits, fee = 100000, 5343
	h := newJumpLedgerHarness(t, startingCredits, fee)

	jumpUnder(t, h, context.Background())

	row := h.latest(t)
	require.Equal(t, string(ledger.TransactionTypeJump), string(row.TransactionType()))
	require.Equal(t, "manual", row.OperationType(),
		"a genuinely unattributed jump must stay 'manual' — that bucket is what an operator's own actions land in")
	require.Empty(t, row.RelatedEntityID(), "and it names no container, because none owned it")
}
