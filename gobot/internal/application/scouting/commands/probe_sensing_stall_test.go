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

// The expansion pass's verdict table.
//
// Driven against the pass's REPORT rather than through a synthesised 400-line expansion fixture:
// the report is the DTO the engine hands the coordinator, and the behaviour under test is the
// coordinator's projection of it, not the expansion engine — which owns its own tests.
func TestExpansionStallVerdictTable(t *testing.T) {
	cases := []struct {
		name       string
		rep        parkedsensing.ExpandReport
		err        error
		wantStatus health.StallOutcome
		wantReason health.StallReason
	}{
		{
			name:       "charting progress is progress — a moving frontier is not idle",
			rep:        parkedsensing.ExpandReport{Discovered: 2},
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

// --- placement refusals are a wedge, not quiet (sp-biegl) -----------------------

// TestSensingTickVerdict_PlacementRefusalsAreBlockedNotIdle is the bead.
//
// A placement tick that issued moves and had every one of them refused was filed as IDLE. That
// understates it exactly as badly as filing it as PROGRESS: 266 probes can sit frozen
// for 28 hours while the health machinery reported healthy ticks throughout, because a refusal
// incremented rep.Actions and anyEffect() read Actions > 0 as "a placement advanced". sp-cwnwb
// stopped the false PROGRESS; a tick that refuses thirty moves and gets nowhere is still not idle.
//
// The discriminator is the one sp-j1i49 shipped for the relocator, so both coordinators share ONE
// rule: WAS THE WORK LICENSED AND THEN LOST, OR WAS THERE NO WORK? A move that was selected and
// then refused by the actuator was licensed and lost — blocked. A tick that selected nothing had
// no work — idle, and silent forever, which on a settled fleet is the common and correct state.
func TestSensingTickVerdict_PlacementRefusalsAreBlockedNotIdle(t *testing.T) {
	cases := []struct {
		name       string
		tally      sensingTickTally
		wantStatus health.StallOutcome
		wantReason health.StallReason
	}{
		{
			name:       "every move refused and nothing advanced — licensed and lost",
			tally:      sensingTickTally{place: parkedsensing.PlacementReport{Failures: 30}},
			wantStatus: health.StallBlocked,
			wantReason: stallReasonPlacementRefused,
		},
		{
			name:       "a single refusal with nothing advanced still counts as licensed and lost",
			tally:      sensingTickTally{place: parkedsensing.PlacementReport{Failures: 1}},
			wantStatus: health.StallBlocked,
			wantReason: stallReasonPlacementRefused,
		},
		{
			// The rule that keeps the streak clearable. Without it, any fleet where one slot is
			// ever contended never leaves the blocked state.
			name:       "PROGRESS outranks a same-tick refusal — one placement advanced, one refused",
			tally:      sensingTickTally{place: parkedsensing.PlacementReport{Actions: 1, Failures: 9}},
			wantStatus: health.StallProgress,
		},
		{
			name:       "progress ANYWHERE outranks a placement wedge",
			tally:      sensingTickTally{screened: 1, place: parkedsensing.PlacementReport{Failures: 30}},
			wantStatus: health.StallProgress,
		},
		{
			// No move was selected, so nothing was licensed and nothing was lost. This is what a
			// settled fleet looks like every tick and it must stay silent.
			name:       "a tick that selected no move at all is idle, not blocked",
			tally:      sensingTickTally{place: parkedsensing.PlacementReport{}},
			wantStatus: health.StallIdle,
		},
		{
			// An engine that ERRORED outranks a refusal: the harder signal names the cause.
			name:       "an engine failure outranks a placement refusal",
			tally:      sensingTickTally{failures: 1, place: parkedsensing.PlacementReport{Failures: 30}},
			wantStatus: health.StallBlocked,
			wantReason: stallReasonEngineFailure,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sensingTickVerdict(tc.tally)
			if got.Outcome != tc.wantStatus {
				t.Fatalf("outcome = %s (reason %q, detail %q), want %s", got.Outcome, got.Reason, got.Detail, tc.wantStatus)
			}
			if tc.wantReason != "" && got.Reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", got.Reason, tc.wantReason)
			}
		})
	}
}

// TestSensingTickVerdict_PlacementReasonCarriesNoLiveCount pins the subtlest way to leave this
// mechanism fully wired and completely useless.
//
// The StallReason KEYS THE STREAK. A reason string that moves with a live count is a different
// reason every tick, so the streak resets every tick and can never reach StallEscalationTicks —
// the detector reports blocked forever and escalates never. Counts belong in Detail, which is
// carried for the human and not compared.
//
// Asserted STRUCTURALLY — two wildly different refusal counts must produce the byte-identical
// reason, and that reason must be the constant itself. A multi-tick fixture would only catch this
// when the counts happened to differ, which is precisely when a real fleet's counts might not.
func TestSensingTickVerdict_PlacementReasonCarriesNoLiveCount(t *testing.T) {
	one := sensingTickVerdict(sensingTickTally{place: parkedsensing.PlacementReport{Failures: 1}})
	many := sensingTickVerdict(sensingTickTally{place: parkedsensing.PlacementReport{Failures: 987}})

	if one.Reason != many.Reason {
		t.Fatalf("reason moved with the refusal count: %q vs %q. The reason keys the streak, so a "+
			"count inside it restarts the streak every tick and the detector can NEVER escalate.", one.Reason, many.Reason)
	}
	if one.Reason != stallReasonPlacementRefused {
		t.Fatalf("reason = %q, want the constant %q — a reason assembled per tick is a reason that cannot key a streak",
			one.Reason, stallReasonPlacementRefused)
	}
	// The count is not lost, only moved: Detail is what carries it to the human.
	if one.Detail == many.Detail {
		t.Fatalf("detail is identical for 1 and 987 refusals (%q) — the count belongs in Detail, and dropping it "+
			"entirely leaves the operator no idea how wedged the machine is", one.Detail)
	}
}

// TestPlacementWedged_IsSelfContained tests the predicate DIRECTLY, because its Actions clause
// cannot be reached through sensingTickVerdict.
//
// A mutation removing `&& t.place.Actions == 0` survives every verdict-level fixture: anyEffect()
// already counts place.Actions > 0 and returns PROGRESS before placementWedged() is consulted, so
// the clause is unreachable-false at that call site. That makes it invisible to the table above
// while still being the thing that makes the predicate MEAN what its name says — a machine that
// berthed a hull is not wedged, whoever asks and from wherever.
//
// Kept rather than deleted, and tested here instead: a predicate that is only correct because of
// an invariant its single caller happens to maintain is the kind that breaks silently the first
// time it is reused. The progress-outranks-a-loss property at the VERDICT level lives in
// anyEffect() and is pinned by the table case above; this pins the predicate's own contract.
func TestPlacementWedged_IsSelfContained(t *testing.T) {
	cases := []struct {
		name  string
		place parkedsensing.PlacementReport
		want  bool
	}{
		{
			name:  "moves issued, every one refused, nothing advanced — wedged",
			place: parkedsensing.PlacementReport{Failures: 9},
			want:  true,
		},
		{
			// The clause the verdict path can never exercise.
			name:  "something advanced alongside the refusals — NOT wedged, whoever asks",
			place: parkedsensing.PlacementReport{Actions: 1, Failures: 9},
			want:  false,
		},
		{
			name:  "no move was ever issued — nothing licensed, nothing lost",
			place: parkedsensing.PlacementReport{},
			want:  false,
		},
		{
			name:  "a clean tick that advanced placements is not wedged",
			place: parkedsensing.PlacementReport{Actions: 4},
			want:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := (sensingTickTally{place: tc.place}).placementWedged(); got != tc.want {
				t.Fatalf("placementWedged() = %v, want %v for %+v", got, tc.want, tc.place)
			}
		})
	}
}
