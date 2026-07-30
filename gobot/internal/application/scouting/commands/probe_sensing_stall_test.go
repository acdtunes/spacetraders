package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sensingStallSpy is a fake health.StallObserver recording what each tick REPORTED, per key.
type sensingStallSpy struct {
	keys     []health.StallKey
	outcomes []health.TickOutcome
}

func (s *sensingStallSpy) Observe(_ context.Context, key health.StallKey, outcome health.TickOutcome) {
	s.keys = append(s.keys, key)
	s.outcomes = append(s.outcomes, outcome)
}

func (s *sensingStallSpy) forCoordinator(name string) []health.TickOutcome {
	var out []health.TickOutcome
	for i, k := range s.keys {
		if k.Coordinator == name {
			out = append(out, s.outcomes[i])
		}
	}
	return out
}

// errorLedger is a sensing ledger whose very first read fails — the shape of the production
// roster crawl that died at page 318 and had NEVER once succeeded. Everything else is delegated
// to the real fake, so the tick is stopped by the read it actually makes rather than by a
// half-built double.
type errorLedger struct {
	*psLedger
	err error
}

func (l *errorLedger) Systems(context.Context, int) ([]parkedsensing.ExpandSystem, error) {
	return nil, l.err
}

// A pre-EXPANSION tick is CORRECTLY gated — bootstrap owns probes until the home gate is built —
// so it must report IDLE for as many ticks as it takes and never escalate. This is the half of
// the design that makes the alarm worth having.
func TestSensingReportsIdleWhileTheExpansionGateHolds(t *testing.T) {
	h := NewRunProbeSensingCoordinatorHandler(nil, nil, nil, nil, &fakePhase{inExpansion: false}, &shared.MockClock{CurrentTime: time.Now()})
	spy := &sensingStallSpy{}
	h.SetStallObserver(spy)

	for i := 0; i < health.StallEscalationTicks*5; i++ {
		if err := h.ReconcileOnce(context.Background(), sensingTestCmd()); err != nil {
			t.Fatalf("ReconcileOnce error: %v", err)
		}
	}

	got := spy.forCoordinator(sensingStallCoordinator)
	if len(got) != health.StallEscalationTicks*5 {
		t.Fatalf("every gated tick must still report a verdict, got %d", len(got))
	}
	for i, o := range got {
		if o.Outcome != health.StallIdle {
			t.Fatalf("tick %d: outcome = %s (reason %q), want IDLE — a pre-EXPANSION hold is correct, not a stall", i, o.Outcome, o.Reason)
		}
	}
}

// THE WEDGE. A half-wired engine holds every tick fail-closed and produces exactly the silence of
// a quiet fleet. It must report BLOCKED with a stable reason so the streak escalates.
func TestSensingReportsBlockedWhenTheEngineIsUnwired(t *testing.T) {
	h := NewRunProbeSensingCoordinatorHandler(nil, nil, nil, nil, &fakePhase{inExpansion: true}, &shared.MockClock{CurrentTime: time.Now()})
	spy := &sensingStallSpy{}
	h.SetStallObserver(spy)

	for i := 0; i < health.StallEscalationTicks; i++ {
		if err := h.ReconcileOnce(context.Background(), sensingTestCmd()); err != nil {
			t.Fatalf("ReconcileOnce error: %v", err)
		}
	}

	got := spy.forCoordinator(sensingStallCoordinator)
	if len(got) != health.StallEscalationTicks {
		t.Fatalf("expected one verdict per tick, got %d", len(got))
	}
	for i, o := range got {
		if o.Outcome != health.StallBlocked || o.Reason != stallReasonPortsUnwired {
			t.Fatalf("tick %d: outcome/reason = %s/%q, want BLOCKED/%q", i, o.Outcome, o.Reason, stallReasonPortsUnwired)
		}
	}
	if k := spy.keys[0]; k.ContainerID != sensingTestCmd().ContainerID || k.PlayerID != testPlayerID {
		t.Fatalf("stall key must identify the container and player, got %+v", k)
	}
}

// A ledger that cannot be read at all is a BLOCK, reported BEFORE the error return — the tick
// cannot see its own world, which is the case that masquerades as "nothing to do".
func TestSensingReportsBlockedWhenTheLedgerIsUnreadable(t *testing.T) {
	world := newCutoverWorld(t)
	spy := &sensingStallSpy{}
	world.handler.SetStallObserver(spy)
	// Reuse the world's own fully-wired surface and swap ONLY the ledger, so the tick fails at
	// the read under test rather than at the ports check that precedes it.
	base := world.handler.newPorts(testPlayerID)
	base.Ledger = &errorLedger{psLedger: world.ledger, err: errors.New("select failed")}
	world.handler.SetEnginePortsFactory(func(int) SensingEnginePorts { return base })

	if err := world.handler.ReconcileOnce(world.ctx, world.cmd); err == nil {
		t.Fatalf("an unreadable ledger must still fail the tick")
	}

	got := spy.forCoordinator(sensingStallCoordinator)
	if len(got) != 1 || got[0].Outcome != health.StallBlocked || got[0].Reason != stallReasonLedgerUnreadable {
		t.Fatalf("expected one BLOCKED(%s) verdict, got %+v", stallReasonLedgerUnreadable, got)
	}
}

// A fully-wired, quiet world reports IDLE — not BLOCKED — however many ticks it runs, and reports
// a verdict on BOTH keys every tick so neither streak can freeze while the other advances.
func TestSensingReportsIdleOnAQuietButHealthyWorld(t *testing.T) {
	world := newCutoverWorld(t)
	spy := &sensingStallSpy{}
	world.handler.SetStallObserver(spy)

	const ticks = 3
	for i := 0; i < ticks; i++ {
		// Engine failures are legitimate in this fixture (the world is deliberately sparse);
		// what must never happen is a BLOCKED verdict on a tick that accomplished something.
		_ = world.handler.ReconcileOnce(world.ctx, world.cmd)
	}

	sensing := spy.forCoordinator(sensingStallCoordinator)
	expansion := spy.forCoordinator(expansionStallCoordinator)
	if len(sensing) != ticks {
		t.Fatalf("the sensing key must get exactly one verdict per tick, got %d", len(sensing))
	}
	if len(expansion) != ticks {
		t.Fatalf("the expansion key must get exactly one verdict per tick, got %d", len(expansion))
	}
	// The last tick of this world does nothing at all: the cutover is latched, no placements
	// move, and nothing is bought. It must read IDLE, never BLOCKED.
	if last := sensing[len(sensing)-1]; last.Outcome == health.StallBlocked && last.Reason == stallReasonBuyRefused {
		t.Fatalf("a quiet world must not be reported as a wedged drain, got %+v", last)
	}
}

// THE OFF-GATE PRODUCTION FAILURE, and the rest of the expansion pass's verdict table.
//
// Driven against the pass's REPORT rather than through a synthesised 400-line expansion fixture:
// the report is the DTO the engine hands the coordinator, and the behaviour under test is the
// coordinator's projection of it. Reaching OffGateDemanded through the real engine would require
// standing up a ledger, a gate graph, a slot book and a target ordering — i.e. testing the
// expansion engine, which owns its own tests, rather than this mapping.
func TestExpansionStallVerdictTable(t *testing.T) {
	cases := []struct {
		name       string
		rep        parkedsensing.ExpandReport
		err        error
		wantStatus health.StallOutcome
		wantReason health.StallReason
	}{
		{
			name:       "sealed pocket: demand raised, no warp target — the 33-system region behind an unread gate",
			rep:        parkedsensing.ExpandReport{OffGateDemanded: true},
			wantStatus: health.StallBlocked,
			wantReason: stallReasonOffGateNoTarget,
		},
		{
			name:       "off-gate demand WITH a target is not a stall — the fleet has somewhere to go",
			rep:        parkedsensing.ExpandReport{OffGateDemanded: true, OffGateTarget: "X1-QQ9"},
			wantStatus: health.StallIdle,
		},
		{
			name:       "a dispatched warp is progress",
			rep:        parkedsensing.ExpandReport{OffGateDemanded: true, OffGateTarget: "X1-QQ9", OffGateWarped: 1},
			wantStatus: health.StallProgress,
		},
		{
			name:       "charting progress outranks a failed selector — a moving frontier is not sealed",
			rep:        parkedsensing.ExpandReport{Discovered: 2, OffGateDemanded: true},
			wantStatus: health.StallProgress,
		},
		{
			// The operator's switch no longer skips the tick, so a spend pause that
			// found nothing to discover is graded exactly like any other quiet tick.
			name:       "a spend-paused tick with nothing to discover is correctly idle",
			rep:        parkedsensing.ExpandReport{SpendingPaused: true},
			wantStatus: health.StallIdle,
		},
		{
			// AND THE OTHER HALF, which is the whole reason the pause is not a Skipped
			// value: a paused tick that discovered systems did real work, and filing it
			// as idle would hide the one number the operator turned the switch to watch.
			name:       "a spend-paused tick that discovered systems is progress, not idle",
			rep:        parkedsensing.ExpandReport{SpendingPaused: true, Discovered: 3},
			wantStatus: health.StallProgress,
		},
		{
			name:       "budget-starved expansion is held back from work it wanted to do",
			rep:        parkedsensing.ExpandReport{Skipped: "budget"},
			wantStatus: health.StallBlocked,
			wantReason: health.StallReason("expansion_budget"),
		},
		{
			name:       "a failed pass is blocked",
			err:        errors.New("failed to list screened sensing systems"),
			wantStatus: health.StallBlocked,
			wantReason: stallReasonExpansionError,
		},
		{
			name:       "a quiet, fully-charted frontier is idle",
			rep:        parkedsensing.ExpandReport{},
			wantStatus: health.StallIdle,
		},
		{
			name:       "seed work in flight is progress",
			rep:        parkedsensing.ExpandReport{SeedsRequested: 1},
			wantStatus: health.StallProgress,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := expansionStallVerdict(tc.rep, tc.err)
			if got.Outcome != tc.wantStatus {
				t.Fatalf("outcome = %s, want %s", got.Outcome, tc.wantStatus)
			}
			if tc.wantReason != "" && got.Reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", got.Reason, tc.wantReason)
			}
		})
	}
}

// The sensing tick's own verdict table, over the tallies the reconcile already holds.
func TestSensingTickVerdictTable(t *testing.T) {
	cases := []struct {
		name       string
		tally      sensingTickTally
		wantStatus health.StallOutcome
		wantReason health.StallReason
	}{
		{
			name:       "a screened system is progress",
			tally:      sensingTickTally{screened: 1},
			wantStatus: health.StallProgress,
		},
		{
			name:       "a bought probe is progress",
			tally:      sensingTickTally{buy: parkedsensing.BuyReport{Bought: 1}},
			wantStatus: health.StallProgress,
		},
		{
			name:       "progress OUTRANKS a failing engine — a working fleet must not page",
			tally:      sensingTickTally{screened: 2, failures: 1},
			wantStatus: health.StallProgress,
		},
		{
			name:       "failures with nothing accomplished are a block",
			tally:      sensingTickTally{failures: 2},
			wantStatus: health.StallBlocked,
			wantReason: stallReasonEngineFailure,
		},
		{
			name:       "the drain tried and every counter refused",
			tally:      sensingTickTally{buy: parkedsensing.BuyReport{Attempts: 3}},
			wantStatus: health.StallBlocked,
			wantReason: stallReasonBuyRefused,
		},
		{
			name:       "at the probe cap is a CORRECT refusal, not a stall",
			tally:      sensingTickTally{buy: parkedsensing.BuyReport{Attempts: 3, CapHeld: true}},
			wantStatus: health.StallIdle,
		},
		{
			name:       "below the buy floor is a money guard doing its job (RULINGS #4), not a stall",
			tally:      sensingTickTally{buy: parkedsensing.BuyReport{Attempts: 3, FloorHeld: true}},
			wantStatus: health.StallIdle,
		},
		{
			// A tick that surged eight probes into space the fleet has never priced and did
			// nothing else would otherwise be filed IDLE — the silent-progress misreading
			// this verdict exists to prevent, and the one that would let a working surge look
			// like a wedged engine.
			name:       "a surge into charted-but-unpriced space is progress",
			tally:      sensingTickTally{surged: 3},
			wantStatus: health.StallProgress,
		},
		{
			name:       "a steady scan rotation with nothing to change is idle, not progress",
			tally:      sensingTickTally{rotation: 42},
			wantStatus: health.StallIdle,
		},
		{
			name:       "an empty tick is idle",
			tally:      sensingTickTally{},
			wantStatus: health.StallIdle,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sensingTickVerdict(tc.tally)
			if got.Outcome != tc.wantStatus {
				t.Fatalf("outcome = %s (reason %q), want %s", got.Outcome, got.Reason, tc.wantStatus)
			}
			if tc.wantReason != "" && got.Reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", got.Reason, tc.wantReason)
			}
		})
	}
}

// An unwired observer must degrade to silence, never a panic: the sensing tick runs with or
// without observability (RULINGS #4).
func TestSensingRunsWithNoStallObserverWired(t *testing.T) {
	h := NewRunProbeSensingCoordinatorHandler(nil, nil, nil, nil, &fakePhase{inExpansion: true}, &shared.MockClock{CurrentTime: time.Now()})
	if err := h.ReconcileOnce(context.Background(), sensingTestCmd()); err != nil {
		t.Fatalf("ReconcileOnce error: %v", err)
	}
}
