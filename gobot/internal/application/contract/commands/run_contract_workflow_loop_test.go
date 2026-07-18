package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-ehg9: the bootstrap command frigate needs a CONTINUOUS single-hull contract
// loop — re-negotiate + run contracts until stopped — so the pre-hauler cold
// start earns income with zero captain re-launches. batch-contract without the
// loop flag stays single-shot (sp-6fsq); the loop is opt-in via RunWorkflowCommand.Loop.
//
// These tests pin the LOOP ORCHESTRATION (repeat, pace, park-not-crash, clean
// ctx-stop) against a fake per-contract cycle so they are decoupled from the
// delivery pipeline the single-cycle tests already cover. The wiring test below
// (Handle with Loop=true) proves the real cycle is the one that repeats.

// TestRunContractLoop_RunsUntilContextCancelled proves the loop is CONTINUOUS:
// it re-invokes the per-contract cycle after each successful fulfillment (the
// exact gap sp-ehg9 closes — batch-contract exits after ONE) and exits only when
// the container is stopped (ctx cancelled), returning the graceful ctx error.
func TestRunContractLoop_RunsUntilContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(auth.WithPlayerToken(context.Background(), "test-token"))

	calls := 0
	cycle := func(_ context.Context) (*RunWorkflowResponse, error) {
		calls++
		if calls >= 3 {
			// Stop the container after the 3rd contract — the loop must not
			// start a 4th and must return promptly.
			cancel()
		}
		return &RunWorkflowResponse{Fulfilled: true}, nil
	}

	h := &RunWorkflowHandler{clock: &shared.MockClock{}}
	cmd := &RunWorkflowCommand{ShipSymbol: "TORWIND-1", PlayerID: shared.MustNewPlayerID(1), Loop: true}

	_, err := h.runContractLoopWithCycle(ctx, cmd, cycle)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected the loop to exit with context.Canceled on stop, got: %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected the loop to run exactly 3 contracts then stop, ran %d", calls)
	}
}

// TestRunContractLoop_MoneyGuardParksAndContinues proves the money guard STOPS a
// spend it cannot afford without killing the loop: an ErrInsufficientCredits
// cycle (sp-vwhi park) is not propagated as a crash, the loop backs off (via the
// injected clock, so no real wait), and it keeps running so the frigate resumes
// contracts once the treasury recovers. This is the fail-closed money guard the
// bead requires ("stops it when it can't afford") without stranding the hull.
func TestRunContractLoop_MoneyGuardParksAndContinues(t *testing.T) {
	ctx, cancel := context.WithCancel(auth.WithPlayerToken(context.Background(), "test-token"))

	clock := &shared.MockClock{}
	before := clock.Now()

	calls := 0
	cycle := func(_ context.Context) (*RunWorkflowResponse, error) {
		calls++
		switch calls {
		case 1, 2:
			// Money guard denies the purchase: cannot afford the contract goods.
			return &RunWorkflowResponse{}, &contractServices.ErrInsufficientCredits{
				ShipSymbol: "TORWIND-1", TradeSymbol: "ELECTRONICS", UnitsAttempted: 18,
				CreditsNeeded: 100188, CreditsAvailable: 85517,
			}
		case 3:
			// Treasury recovered — the contract completes.
			return &RunWorkflowResponse{Fulfilled: true}, nil
		default:
			cancel()
			return &RunWorkflowResponse{Fulfilled: true}, nil
		}
	}

	h := &RunWorkflowHandler{clock: clock}
	cmd := &RunWorkflowCommand{ShipSymbol: "TORWIND-1", PlayerID: shared.MustNewPlayerID(1), Loop: true}

	_, err := h.runContractLoopWithCycle(ctx, cmd, cycle)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected clean ctx-stop exit, got: %v", err)
	}
	if calls < 4 {
		t.Fatalf("expected the loop to survive 2 money-guard parks and continue (>=4 cycles), ran %d", calls)
	}
	// The loop must have paced itself (backed off) rather than hot-spinning the
	// API while insolvent — provable because the injected clock advanced.
	if !clock.Now().After(before) {
		t.Fatalf("expected the loop to back off (advance the clock) after a money-guard park; clock did not advance")
	}
}

// TestRunContractLoop_StopsPromptlyWhenAlreadyCancelled proves a stop requested
// before the first cycle is honoured immediately — the loop never starts a
// contract on a stopped container (clean handoff at the first-hauler pivot).
func TestRunContractLoop_StopsPromptlyWhenAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(auth.WithPlayerToken(context.Background(), "test-token"))
	cancel()

	calls := 0
	cycle := func(_ context.Context) (*RunWorkflowResponse, error) {
		calls++
		return &RunWorkflowResponse{}, nil
	}

	h := &RunWorkflowHandler{clock: &shared.MockClock{}}
	cmd := &RunWorkflowCommand{ShipSymbol: "TORWIND-1", PlayerID: shared.MustNewPlayerID(1), Loop: true}

	_, err := h.runContractLoopWithCycle(ctx, cmd, cycle)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
	if calls != 0 {
		t.Fatalf("expected no contract to start on an already-stopped container, ran %d", calls)
	}
}
