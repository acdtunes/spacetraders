package commands

import (
	"context"
	"errors"
	"testing"
)

// The Admiral's cold-start arc: "the initial DATA phase should buy 2 probes and START a
// 3 probe tour on the home system." Buying and leaving the probes idle fails it by one verb,
// so these pin the verb — and pin that it does NOT grow back into the standing engine that
// was deleted.

// fakeTourStarter records the calls bootstrap makes to the operator's tour path.
type fakeTourStarter struct {
	calls   int
	systems []string
	hulls   int
	err     error
}

func (f *fakeTourStarter) StartHomeMarketTour(_ context.Context, _ int, homeSystem string) (int, error) {
	f.calls++
	f.systems = append(f.systems, homeSystem)
	if f.err != nil {
		return 0, f.err
	}
	return f.hulls, nil
}

// wiredForTour builds a cold-start handler with the tour starter spied.
func wiredForTour(obs Observation, starter HomeTourStarter) (*RunBootstrapCoordinatorHandler, *coldStartSpies) {
	h, spies := spiedHandler(obs, &fakeHandoff{})
	h.SetHomeTourStarter(starter)
	return h, spies
}

// The verb itself: probes bought, none scouting, home known ⇒ the tour starts, on HOME.
func TestBootstrap_ColdStart_StartsTheHomeMarketTour(t *testing.T) {
	obs := freshDataObs()
	obs.ProbeCount = probeTarget
	obs.ProbesScouting = 0
	starter := &fakeTourStarter{hulls: 3}
	h, _ := wiredForTour(obs, starter)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if starter.calls != 1 {
		t.Fatalf("cold start must START the tour, not merely buy probes; got %d calls", starter.calls)
	}
	if starter.systems[0] != "X1-HQ" {
		t.Fatalf("the tour must be started on the HOME system, got %v", starter.systems)
	}
	if res.TourHulls != 3 {
		t.Fatalf("the heartbeat must report the hulls put on tour, got %d", res.TourHulls)
	}
}

// IDEMPOTENCY, and the reason it matters: the tour path RE-PARTITIONS every hull it is
// handed, tearing down whatever is flying. A per-tick call without this guard would restart
// the tour forever and never finish a circuit.
func TestBootstrap_ColdStart_DoesNotRestartATourAlreadyFlying(t *testing.T) {
	obs := freshDataObs()
	obs.ProbeCount = probeTarget
	obs.ProbesScouting = probeTarget // already touring
	starter := &fakeTourStarter{hulls: 3}
	h, _ := wiredForTour(obs, starter)

	for tick := 1; tick <= 5; tick++ {
		if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
			t.Fatalf("tick %d: reconcileOnce: %v", tick, err)
		}
	}
	if starter.calls != 0 {
		t.Fatalf("a tour already flying must never be restarted (it would re-partition and never finish a circuit); got %d calls over 5 ticks", starter.calls)
	}
}

// THE RAMP. The probe fleet fills 1 → probeTarget across SUCCESSIVE ticks, so the post is cut on
// whatever existed first and must GROW to the rest, or those probes park for the whole cold start.
func TestBootstrap_ColdStart_TourGrowsToTheProbesTheRampBuys(t *testing.T) {
	obs := freshDataObs()
	obs.MarketsTotal = 27
	obs.ProbeCount = 1 // only the starting satellite, and it has never toured
	obs.ProbesScouting = 0
	obs.ProbesUntoured = 1
	starter := &fakeTourStarter{hulls: 1}
	h, spies := wiredForTour(obs, starter)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("tick 1: reconcileOnce: %v", err)
	}
	if starter.calls != 1 || res.TourHulls != 1 {
		t.Fatalf("tick 1 must put the one probe there is on tour; got calls=%d hulls=%d", starter.calls, res.TourHulls)
	}

	spies.observer.obs.ProbeCount = probeTarget // the ramp lands: two more, idle, never toured
	spies.observer.obs.ProbesScouting = 1
	spies.observer.obs.ProbesUntoured = 2
	starter.hulls = probeTarget

	res, err = h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("tick 2: reconcileOnce: %v", err)
	}
	if starter.calls != 2 {
		t.Fatalf("the post must be RE-CUT for the probes the ramp bought — they sit idle for the whole cold start otherwise; got %d calls", starter.calls)
	}
	if res.TourHulls != probeTarget {
		t.Fatalf("the re-cut must cover the WHOLE probe fleet (%d disjoint circuits), got %d hulls", probeTarget, res.TourHulls)
	}

	spies.observer.obs.ProbesScouting = probeTarget
	spies.observer.obs.ProbesUntoured = 0
	for tick := 3; tick <= 7; tick++ {
		if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
			t.Fatalf("tick %d: reconcileOnce: %v", tick, err)
		}
	}
	if starter.calls != 2 {
		t.Fatalf("a fully-manned post must never be re-cut again (it would re-partition forever and finish no circuit); got %d calls", starter.calls)
	}
}

// BOUNDED BY WHAT THE PARTITION CAN EMPLOY: a hull it can give no market never tours, so it
// would re-cut the live tours every tick.
func TestBootstrap_ColdStart_DoesNotRecutForAProbeTheHomeMarketsCannotEmploy(t *testing.T) {
	obs := freshDataObs()
	obs.MarketsTotal = 2 // fewer markets than probes ⇒ one hull can be given nothing
	obs.ProbeCount = probeTarget
	obs.ProbesScouting = 2
	obs.ProbesUntoured = 1
	starter := &fakeTourStarter{hulls: 2}
	h, _ := wiredForTour(obs, starter)

	for tick := 1; tick <= 5; tick++ {
		if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
			t.Fatalf("tick %d: reconcileOnce: %v", tick, err)
		}
	}
	if starter.calls != 0 {
		t.Fatalf("a probe the home markets cannot employ must not re-cut the post — that is an every-tick re-partition that finishes no circuit; got %d calls over 5 ticks", starter.calls)
	}
}

// It is NOT a re-manner. One probe dying leaves the others flying and is not corrected —
// re-growing the deleted standing engine one guard at a time is exactly what the ruling cut.
func TestBootstrap_ColdStart_PartialLossIsNotReMannedMidTour(t *testing.T) {
	obs := freshDataObs()
	obs.ProbeCount = probeTarget
	obs.ProbesScouting = 1 // two died, one still flying
	starter := &fakeTourStarter{hulls: 3}
	h, _ := wiredForTour(obs, starter)

	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if starter.calls != 0 {
		t.Fatalf("bootstrap must not re-man a partially-lost tour — that is the deleted watchdog's job, deliberately gone; got %d calls", starter.calls)
	}
}

// ...but a fleet with NOTHING scouting starts a fresh tour. Without this, the test above
// would be satisfied by a bootstrap that never starts a tour at all.
func TestBootstrap_ColdStart_TotalLossStartsAFreshTour(t *testing.T) {
	obs := freshDataObs()
	obs.ProbeCount = probeTarget
	obs.ProbesScouting = 0
	starter := &fakeTourStarter{hulls: 3}
	h, _ := wiredForTour(obs, starter)

	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if starter.calls != 1 {
		t.Fatalf("a fleet with nothing scouting must start a tour, got %d calls", starter.calls)
	}
}

// A start that fails is retried next tick, surfaces on its own line, and never touches
// res.Blocker — that field is single-valued and belongs to the money guards, so a tour that
// could not start must never mask the reason a BUY could not happen. It moves no credits.
func TestBootstrap_ColdStart_TourStartFailure_BlocksNothingAndRetries(t *testing.T) {
	obs := freshDataObs()
	obs.ProbeCount = 1 // below target, so the probe buy is still live as the control
	starter := &fakeTourStarter{err: errors.New("routing service down")}
	h, spies := wiredForTour(obs, starter)

	log := &capturingLogger{}
	res, err := h.reconcileOnce(ctxWithLogger(log), baseCmd())
	if err != nil {
		t.Fatalf("a failed tour start must not fail the tick: %v", err)
	}
	if res.Blocker == "tour_start_error" {
		t.Fatalf("the tour must NOT claim res.Blocker — that field is single-valued and belongs to the money guards; a tour that could not start must never mask why a BUY could not happen")
	}
	if !log.has("bootstrap_tour_start_error") {
		t.Fatalf("the failure must surface on its own log line")
	}
	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if starter.calls != 2 {
		t.Fatalf("a failed start must be RETRIED on the next tick, got %d calls", starter.calls)
	}
	if spies.probes.buys == 0 {
		t.Fatalf("control: the rest of cold start must keep running — probe buying is unaffected")
	}
}

// An unwired starter says so LOUDLY rather than failing silently — probes bought with
// nothing scanning is the exact invisible failure this bead exists to remove — but still on
// its own line, not by taking res.Blocker from a money guard.
func TestBootstrap_ColdStart_NoTourStarterWired_IsLoud(t *testing.T) {
	obs := freshDataObs()
	obs.ProbeCount = probeTarget
	h, _ := spiedHandler(obs, &fakeHandoff{})

	log := &capturingLogger{}
	res, err := h.reconcileOnce(ctxWithLogger(log), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if res.Blocker == "no_tour_starter" {
		t.Fatalf("an unwired starter must not claim res.Blocker — it surfaces on its own line")
	}
	if !log.has("bootstrap_tour_starter_missing") {
		t.Fatalf("the missing wiring must be loud")
	}
}

// No probes yet, or no home system: nothing to fly and nothing to fly it over. Silent, and
// retried by the ordinary tick — not a blocker, because there is no fault here.
func TestBootstrap_ColdStart_NoProbesOrNoHome_StartsNothingQuietly(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*Observation)
	}{
		{"no probes bought yet", func(o *Observation) { o.ProbeCount = 0 }},
		{"home system unresolved", func(o *Observation) { o.ProbeCount = probeTarget; o.HomeSystem = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obs := freshDataObs()
			tc.mut(&obs)
			starter := &fakeTourStarter{hulls: 3}
			h, _ := wiredForTour(obs, starter)

			log := &capturingLogger{}
			if _, err := h.reconcileOnce(ctxWithLogger(log), baseCmd()); err != nil {
				t.Fatalf("reconcileOnce: %v", err)
			}
			if starter.calls != 0 {
				t.Fatalf("nothing to start, so nothing must be attempted; got %d calls", starter.calls)
			}
			if log.has("bootstrap_tour_start_error") || log.has("bootstrap_tour_starter_missing") {
				t.Fatalf("an absent precondition is not a fault and must log no error")
			}
		})
	}
}

// A mature fleet past the gate never starts a tour: EXPANSION is parked sensing's, and the
// tour path would refuse anyway. This pins that bootstrap does not even ask.
func TestBootstrap_Expansion_NeverStartsATour(t *testing.T) {
	starter := &fakeTourStarter{hulls: 3}
	h, _ := wiredForTour(matureObs(), starter)

	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if starter.calls != 0 {
		t.Fatalf("past the jump gate, market freshness is parked sensing's; got %d tour starts", starter.calls)
	}
}
