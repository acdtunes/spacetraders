package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
)

// run_opportunity_relocator_stall_test.go — the tick verdict and its two consumers.
//
// The relocator shipped with no metrics, no stall observer, and a Skipped map nothing read, which
// made a relocator losing EVERY decision indistinguishable from one with nothing to do. That is
// sp-biegl inverted: there, refusal ticks were filed as PROGRESS and a 28-hour freeze looked healthy;
// here nothing was filed at all and a total failure looks idle.
//
// THE BOUNDARY IS THE WHOLE TEST. Getting BLOCKED-vs-IDLE wrong reproduces one of those two failures:
// too eager and a correctly-quiet fleet pages forever (which is how an alarm becomes worthless and
// then ignored); too shy and the racing relocator stays invisible. So the verdict is a PURE function
// judged on facts the tick already holds, and every branch is pinned by name.

// --- the pure verdict ---

// relocStallResult builds a tick result with the given skip reasons, so a case reads as the fact it
// is testing rather than as map plumbing.
func relocStallResult(evaluated int, relocated, resumed []string, skips map[string]int) *RelocatorTickResult {
	result := newRelocatorTickResult()
	result.Evaluated = evaluated
	result.Relocated = relocated
	result.Resumed = resumed
	for reason, n := range skips {
		for i := 0; i < n; i++ {
			result.skip(reason)
		}
	}
	return result
}

// PROGRESS and BLOCKED and IDLE, one case per branch, each naming why that branch must be what it is.
func TestRelocatorTickVerdictShould_MapEachTickShapeToItsHonestOutcome(t *testing.T) {
	for name, tc := range map[string]struct {
		result   *RelocatorTickResult
		err      error
		disabled bool
		want     health.StallOutcome
		wantWhy  health.StallReason
	}{
		// --- PROGRESS ---
		"a hull was relocated": {
			result: relocStallResult(1, []string{"HAULER-A"}, nil, nil),
			want:   health.StallProgress,
		},
		"an interrupted move was resumed": {
			result: relocStallResult(0, nil, []string{"HAULER-A"}, nil),
			want:   health.StallProgress,
		},

		// --- IDLE: every one of these must stay silent FOREVER, however long it persists ---
		"the operator stood the relocator down": {
			// A kill-switch tick is an INTENDED no-op. Escalating it would page continuously for as
			// long as an operator chooses to keep auto-relocation off, which is how an alarm gets
			// muted and then ignored. The tour metrics make the same call in writing: "the
			// kill-switch ... [is] NOT counted - [it is] not [an] evaluation".
			result:   relocStallResult(0, nil, nil, map[string]int{"reposition_disabled": 1}),
			disabled: true,
			want:     health.StallIdle,
		},
		"the fleet holds no trade hull at all": {
			result: relocStallResult(0, nil, nil, nil),
			want:   health.StallIdle,
		},
		"every hull is out working a tour": {
			// A BUSY fleet is a HEALTHY fleet. There is nothing to relocate because everything is
			// earning, which is the best state the system has.
			result: relocStallResult(0, nil, nil, map[string]int{"mid_tour": 4}),
			want:   health.StallIdle,
		},
		"every hull is inside its post-relocation cooldown": {
			result: relocStallResult(0, nil, nil, map[string]int{"within_cooldown": 3}),
			want:   health.StallIdle,
		},
		"every hull is protected by RULINGS #7": {
			// Refusing to poach a pinned hull is the ownership model working, not a stall.
			result: relocStallResult(0, nil, nil, map[string]int{"pinned_hull_protected": 2, "command_frigate_protected": 1}),
			want:   health.StallIdle,
		},
		"hulls were scored and no ground was worth the move": {
			// THE MOST IMPORTANT IDLE. The relocator looked at real ground and decided the economics
			// did not justify a relocation. That is the reconciler succeeding at its job — it will be
			// the common case on a settled fleet, and paging on it would page every tick forever.
			result: relocStallResult(3, nil, nil, map[string]int{"no_uplift": 5, "below_npv_threshold": 2}),
			want:   health.StallIdle,
		},

		// --- BLOCKED: work was available and could not be done ---
		"the fleet or the intent store could not be read": {
			result:  relocStallResult(0, nil, nil, nil),
			err:     errors.New("ship repository unavailable"),
			want:    health.StallBlocked,
			wantWhy: stallReasonRelocatorTickError,
		},
		"a licensed relocation lost its hull to the claim race": {
			// THE BEAD'S CASE, and the reason this whole slice exists. The economics said YES and the
			// actuation said NO. Measured live: 3 of the relocator's first 4 decisions died here, and
			// nothing recorded it.
			result:  relocStallResult(1, nil, nil, map[string]int{"claimed_at_actuation": 1}),
			want:    health.StallBlocked,
			wantWhy: stallReasonRelocatorCommitFailed,
		},
		"a licensed relocation could not prove the hull was still free": {
			result:  relocStallResult(1, nil, nil, map[string]int{"actuation_recheck_unreadable": 1}),
			want:    health.StallBlocked,
			wantWhy: stallReasonRelocatorCommitFailed,
		},
		"a licensed relocation could not persist its intent": {
			result:  relocStallResult(1, nil, nil, map[string]int{"intent_persist_failed": 1}),
			want:    health.StallBlocked,
			wantWhy: stallReasonRelocatorCommitFailed,
		},
		"a licensed relocation's jump failed": {
			result:  relocStallResult(1, nil, nil, map[string]int{"relocate_failed": 1}),
			want:    health.StallBlocked,
			wantWhy: stallReasonRelocatorCommitFailed,
		},
		"every scored hull's ground was unreadable": {
			// This one MASQUERADES as "no good ground" — the same trap the autosizer names in
			// stallReasonDemandUnreadable: "distinct from 'no shortfall' precisely because it is the
			// case that MASQUERADES as no shortfall". An unreadable region set is not evidence that
			// the map is poor.
			result:  relocStallResult(2, nil, nil, map[string]int{"regions_unreadable": 2}),
			want:    health.StallBlocked,
			wantWhy: stallReasonRelocatorRegionsUnreadable,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := relocatorTickVerdict(tc.result, tc.err, tc.disabled)

			if got.Outcome != tc.want {
				t.Fatalf("%s produced %s, want %s", name, got.Outcome, tc.want)
			}
			if tc.wantWhy != "" && got.Reason != tc.wantWhy {
				t.Fatalf("%s produced reason %q, want %q — the reason KEYS the streak, so a wrong or varying one can never escalate", name, got.Reason, tc.wantWhy)
			}
			if tc.want != health.StallBlocked && got.Reason != "" {
				t.Fatalf("%s is not BLOCKED but carries reason %q; a non-blocked outcome must name none", name, got.Reason)
			}
		})
	}
}

// PROGRESS WINS OVER A REFUSAL IN THE SAME TICK. The relocator may relocate one hull and lose another
// to the race on the very same tick; that tick did work, so it must clear the streak. Otherwise a
// working relocator escalates purely because it is also busy — and the streak would never clear on a
// fleet where any hull is ever contended.
func TestRelocatorTickVerdictShould_TreatATickThatBothRelocatedAndLostAHullAsProgress(t *testing.T) {
	result := relocStallResult(2, []string{"HAULER-A"}, nil, map[string]int{"claimed_at_actuation": 1})

	if got := relocatorTickVerdict(result, nil, false); got.Outcome != health.StallProgress {
		t.Fatalf("a tick that relocated one hull and lost another produced %s (%s); partial success is still progress and must clear the streak", got.Outcome, got.Reason)
	}
}

// A nil result must not panic the verdict. Reconcile can return (nil, err) on an early failure, and
// observability is never allowed to take down the tick it is reporting on.
func TestRelocatorTickVerdictShould_SurviveANilTickResult(t *testing.T) {
	if got := relocatorTickVerdict(nil, errors.New("boom"), false); got.Outcome != health.StallBlocked {
		t.Fatalf("a nil result with an error produced %s, want BLOCKED", got.Outcome)
	}
	if got := relocatorTickVerdict(nil, nil, false); got.Outcome != health.StallIdle {
		t.Fatalf("a nil result with no error produced %s, want IDLE", got.Outcome)
	}
}

// --- the escalation seam ---

// relocFakeStall records what the handler reported, so a test can assert the ONCE-PER-TICK contract
// the escalator depends on: it is the tick counter itself, so a double report inflates the streak and
// a skipped one stalls it.
type relocFakeStall struct {
	keys     []health.StallKey
	outcomes []health.TickOutcome
}

func (f *relocFakeStall) Observe(_ context.Context, key health.StallKey, outcome health.TickOutcome) {
	f.keys = append(f.keys, key)
	f.outcomes = append(f.outcomes, outcome)
}

// The handler must report EXACTLY ONE verdict per tick, keyed to its own container.
func TestOpportunityRelocatorShould_ReportExactlyOneStallVerdictPerTick(t *testing.T) {
	h := newRelocHarness(t)
	stall := &relocFakeStall{}
	h.handler.SetStallObserver(stall)

	h.reconcileWithStall(t)

	if len(stall.outcomes) != 1 {
		t.Fatalf("reported %d verdicts for one tick, want exactly 1 — the streak IS the tick count, so a double report inflates it and a skipped one stalls it", len(stall.outcomes))
	}
	if stall.outcomes[0].Outcome != health.StallProgress {
		t.Fatalf("the baseline tick relocates a hull but reported %s", stall.outcomes[0].Outcome)
	}
	if stall.keys[0].Coordinator != relocatorStallCoordinator || stall.keys[0].ContainerID != "relocator-1" || stall.keys[0].PlayerID != 1 {
		t.Fatalf("verdict keyed %+v; it must name this coordinator and THIS container so one container's stall is never closed out by a sibling's healthy tick", stall.keys[0])
	}
}

// N+1 CONSECUTIVE REFUSAL TICKS. The escalation threshold is StallEscalationTicks (3), so the fixture
// runs FOUR ticks that all lose their hull to the race — strictly more than the threshold, or the
// threshold is never consulted and a mutation removing it survives.
//
// Each tick must report BLOCKED with the SAME reason: the reason keys the streak, so a reason that
// varied tick to tick would restart the count every time and could never escalate, which is the
// subtlest way this whole mechanism can be made useless while looking wired.
func TestOpportunityRelocatorShould_ReportAConsecutiveBlockedStreakLongerThanTheEscalationThreshold(t *testing.T) {
	h := newRelocHarness(t)
	stall := &relocFakeStall{}
	h.handler.SetStallObserver(stall)
	// Eligible at observation, taken at actuation, on every tick — the live claim-race shape.
	h.fleet.atActuation = map[string]RelocatorHull{
		"HAULER-A": {ShipSymbol: "HAULER-A", CurrentSystem: "X1-HOME", OnTour: true},
	}

	ticks := health.StallEscalationTicks + 1
	for i := 0; i < ticks; i++ {
		h.reconcileWithStall(t)
	}

	if len(stall.outcomes) != ticks {
		t.Fatalf("reported %d verdicts over %d ticks, want one each", len(stall.outcomes), ticks)
	}
	for i, got := range stall.outcomes {
		if got.Outcome != health.StallBlocked {
			t.Fatalf("tick %d of a permanently-racing relocator reported %s; every one must be BLOCKED or the streak never reaches the threshold", i+1, got.Outcome)
		}
		if got.Reason != stallReasonRelocatorCommitFailed {
			t.Fatalf("tick %d reported reason %q, want %q on every tick — a reason that varies restarts the streak and can never escalate", i+1, got.Reason, stallReasonRelocatorCommitFailed)
		}
	}
}

// An UNWIRED escalator must never be a precondition for running a tick: observability is not allowed
// to break the thing it observes.
func TestOpportunityRelocatorShould_StillRelocateWithNoStallObserverWired(t *testing.T) {
	h := newRelocHarness(t)

	if got := h.reconcileWithStall(t); len(got.Relocated) != 1 {
		t.Fatalf("an unwired stall observer changed the tick's behaviour; skips %v", got.Skipped)
	}
}

// --- the heartbeat (the consumer Skipped's comment always promised) ---

// EVERY exclusion reason the tick computes must reach an operator. RelocatorTickResult.Skipped shipped
// documented as "keyed by reason, for the heartbeat" with no heartbeat reading it, so the counts were
// computed and discarded — which is precisely why "3 of 4 decisions lost" had to be hand-derived by
// joining daemon.log against the containers table.
//
// It is asserted in the MESSAGE TEXT because `container logs` drops the structured metadata map (the
// sp-149h/sp-iqyq renderer defect), so anything only in the map is invisible to the operator reading
// logs.
func TestOpportunityRelocatorShould_NameEveryExclusionReasonInTheTickHeartbeat(t *testing.T) {
	h := newRelocHarness(t)
	h.fleet.atActuation = map[string]RelocatorHull{
		"HAULER-A": {ShipSymbol: "HAULER-A", CurrentSystem: "X1-HOME", OnTour: true},
	}
	logger := &tradeCaptureLogger{}

	// Driven through `tick` — the unit Handle runs — NOT by calling the renderer directly. Calling it
	// directly proves only that the function formats a map, and a mutation removing the call from the
	// tick survives: the reason counts would be computed and discarded exactly as before.
	if _, err := h.handler.tick(common.WithLogger(context.Background(), logger), h.cmd); err != nil {
		t.Fatalf("tick returned an error: %v", err)
	}

	if !logger.loggedContaining("claimed_at_actuation") {
		t.Fatalf("the tick heartbeat never named the exclusion reason; the skip counts are computed and discarded, which is the whole defect. logged: %v", logger.messages)
	}
	if !logger.loggedContaining("evaluated") {
		t.Fatalf("the heartbeat named no evaluated count, so a reader cannot tell a refusal tick from an idle one. logged: %v", logger.messages)
	}
}

// reconcileWithStall drives the handler through `tick` — reconcile PLUS the reporting Handle performs —
// so an observability test exercises the same path production does rather than a private helper.
func (h *relocHarness) reconcileWithStall(t *testing.T) *RelocatorTickResult {
	t.Helper()
	result, err := h.handler.tick(common.WithLogger(context.Background(), &tradeCaptureLogger{}), h.cmd)
	if err != nil {
		t.Fatalf("tick returned an error: %v", err)
	}
	return result
}

// THE REASON MUST NOT CARRY LIVE NUMBERS, and this is asserted structurally rather than by running
// several ticks, because a multi-tick fixture only catches it when the counts happen to differ between
// ticks — which they often do not.
//
// StallReason KEYS the streak. A reason that varies tick to tick restarts the count every tick and can
// therefore NEVER escalate, no matter how badly wedged the coordinator is. That is the subtlest way this
// whole mechanism can be made useless while looking perfectly wired, so the invariant is: counts live in
// Detail, never in Reason.
func TestRelocatorTickVerdictShould_KeepLiveNumbersOutOfTheStreakKeyingReason(t *testing.T) {
	for _, lost := range []int{1, 2, 7} {
		got := relocatorTickVerdict(relocStallResult(lost, nil, nil, map[string]int{"claimed_at_actuation": lost}), nil, false)

		if got.Reason != stallReasonRelocatorCommitFailed {
			t.Fatalf("with %d hulls lost the reason was %q, want the CONSTANT %q on every tick; a reason that moves with the count restarts the streak and can never escalate", lost, got.Reason, stallReasonRelocatorCommitFailed)
		}
		if !strings.Contains(got.Detail, fmt.Sprintf("%d", lost)) {
			t.Fatalf("the detail %q does not carry the count %d; the numbers belong in Detail, which is free text for the payload and log only", got.Detail, lost)
		}
	}
}

// The heartbeat's skip list must be SORTED. Go randomises map iteration, so an unsorted render reshuffles
// the line every tick — which defeats grep, defeats diffing two ticks against each other, and makes the
// one aggregate the relocator produces unreadable in exactly the situation it exists for.
//
// Three reasons whose natural map order will not be alphabetical, checked over repeated renders so a
// single lucky iteration cannot pass.
func TestRelocatorHeartbeatShould_RenderSkipReasonsInAStableSortedOrder(t *testing.T) {
	skipped := map[string]int{"within_cooldown": 3, "claimed_at_actuation": 1, "mid_tour": 2, "region_herded": 4}
	want := "skipped[claimed_at_actuation=1,mid_tour=2,region_herded=4,within_cooldown=3]"

	for i := 0; i < 20; i++ {
		if got := renderSkipCounts(skipped); got != want {
			t.Fatalf("render %d produced %q, want %q — Go map order is randomised, so an unsorted render reshuffles the heartbeat every tick and defeats grep and diff", i, got, want)
		}
	}
}

// An empty skip map must say so rather than render an empty bracket, so a clean tick reads as a clean
// tick instead of looking like a truncated line.
func TestRelocatorHeartbeatShould_NameAnUneventfulTickExplicitly(t *testing.T) {
	if got := renderSkipCounts(nil); got != "no exclusions" {
		t.Fatalf("an empty skip map rendered %q; a clean tick must say so rather than look truncated", got)
	}
}

// --- the metrics sink (sp-j1i49 item 1) ---

// relocMetric is one recorded emission, so a test asserts the OBSERVABLE series rather than reaching
// into a collector.
type relocMetric struct {
	kind  string // tick | decision | skip
	label string
	count int
}

type relocFakeMetrics struct{ recorded []relocMetric }

func (f *relocFakeMetrics) RecordTick(_ int, verdict string) {
	f.recorded = append(f.recorded, relocMetric{kind: "tick", label: verdict, count: 1})
}

func (f *relocFakeMetrics) RecordDecision(_ int, outcome string) {
	f.recorded = append(f.recorded, relocMetric{kind: "decision", label: outcome, count: 1})
}

func (f *relocFakeMetrics) RecordSkip(_ int, reason string, count int) {
	f.recorded = append(f.recorded, relocMetric{kind: "skip", label: reason, count: count})
}

func (f *relocFakeMetrics) find(kind, label string) (relocMetric, bool) {
	for _, m := range f.recorded {
		if m.kind == kind && m.label == label {
			return m, true
		}
	}
	return relocMetric{}, false
}

// THE COUNTER THE BEAD EXISTS FOR. Every per-reason skip count must reach the metrics sink, because a
// log line is greppable but not countable — and "did slice 1 reduce the claim-race losses?" is a RATE
// question that no amount of grepping answers.
func TestOpportunityRelocatorShould_EmitEveryPerReasonSkipCountToTheMetricsSink(t *testing.T) {
	h := newRelocHarness(t)
	sink := &relocFakeMetrics{}
	h.handler.SetMetricsSink(sink)
	// The live claim-race shape: eligible at observation, taken at actuation.
	h.fleet.atActuation = map[string]RelocatorHull{
		"HAULER-A": {ShipSymbol: "HAULER-A", CurrentSystem: "X1-HOME", OnTour: true},
	}

	h.reconcileWithStall(t)

	got, ok := sink.find("skip", "claimed_at_actuation")
	if !ok {
		t.Fatalf("the claim-race loss never reached the metrics sink; it stays a log line and the rate question stays unanswerable. recorded %+v", sink.recorded)
	}
	if got.count != 1 {
		t.Fatalf("recorded claimed_at_actuation=%d, want 1 — the COUNT is emitted, not merely the fact that it happened", got.count)
	}
	if _, ok := sink.find("tick", "BLOCKED"); !ok {
		t.Fatalf("the tick verdict never reached the sink, so the refusal-tick FRACTION cannot be computed. recorded %+v", sink.recorded)
	}
}

// A successful relocation must be countable too, or the numerator has no denominator.
func TestOpportunityRelocatorShould_EmitARelocationDecisionAndAProgressTick(t *testing.T) {
	h := newRelocHarness(t)
	sink := &relocFakeMetrics{}
	h.handler.SetMetricsSink(sink)

	h.reconcileWithStall(t)

	if _, ok := sink.find("decision", relocatorDecisionRelocated); !ok {
		t.Fatalf("a completed relocation emitted no decision; recorded %+v", sink.recorded)
	}
	if _, ok := sink.find("tick", "PROGRESS"); !ok {
		t.Fatalf("a productive tick emitted no PROGRESS verdict; recorded %+v", sink.recorded)
	}
}

// An UNWIRED sink must never gate a tick: metrics are observability, and observability is not allowed
// to break the thing it observes (the same contract as the stall seam).
func TestOpportunityRelocatorShould_StillRelocateWithNoMetricsSinkWired(t *testing.T) {
	h := newRelocHarness(t)

	if got := h.reconcileWithStall(t); len(got.Relocated) != 1 {
		t.Fatalf("an unwired metrics sink changed the tick's behaviour; skips %v", got.Skipped)
	}
}

// AN ERRORED TICK IS STILL OBSERVED, and both consumers must be told the SAME thing about it.
//
// This is the case where the two reporting paths can silently diverge: the verdict depends on the
// tick's ERROR and on the kill-switch, neither of which is visible in the tick result. A reporting path
// that re-derives the verdict from the result alone looks correct on every healthy fixture and is wrong
// exactly when something has gone wrong — which is when the signal is the only thing anyone has.
//
// So: an unreadable fleet must reach the escalator as BLOCKED(tick_error) AND the counter as BLOCKED.
func TestOpportunityRelocatorShould_ReportAnErroredTickAsBlockedToBothConsumersAlike(t *testing.T) {
	h := newRelocHarness(t)
	stall := &relocFakeStall{}
	sink := &relocFakeMetrics{}
	h.handler.SetStallObserver(stall)
	h.handler.SetMetricsSink(sink)
	h.fleet.err = errors.New("ship repository unavailable")

	// tick surfaces the error to the runner; the reporting must happen anyway.
	if _, err := h.handler.tick(common.WithLogger(context.Background(), &tradeCaptureLogger{}), h.cmd); err == nil {
		t.Fatal("an unreadable fleet returned no error; the runner would never retry")
	}

	if len(stall.outcomes) != 1 {
		t.Fatalf("an errored tick reported %d verdicts, want exactly 1 — a tick that fails is still a tick, and a skipped report stalls the streak", len(stall.outcomes))
	}
	if stall.outcomes[0].Outcome != health.StallBlocked || stall.outcomes[0].Reason != stallReasonRelocatorTickError {
		t.Fatalf("the escalator was told %s/%q; an unreadable fleet is a BLOCKED tick_error, and re-deriving the verdict from the tick RESULT alone cannot see the error at all", stall.outcomes[0].Outcome, stall.outcomes[0].Reason)
	}
	if _, ok := sink.find("tick", string(health.StallBlocked)); !ok {
		t.Fatalf("the counter was told something different from the escalator about the same tick; both must read ONE verdict or the dashboard and the alarm disagree. recorded %+v", sink.recorded)
	}
}
