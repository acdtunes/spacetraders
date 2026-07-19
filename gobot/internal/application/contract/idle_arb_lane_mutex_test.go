package contract

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// The lane mutex enforces ONE hull per (good, sink) per recovery window. These
// drive the dispatcher on a MockClock so the termination + recovery-hold
// lifecycle is deterministic.

// idleArbSingleSinkClockHarness builds a dispatcher over a hub and a SINGLE
// in-leash sink for MACHINERY, on a caller-supplied clock so the recovery hold
// can be advanced deterministically. All hulls start idle at the hub, so every
// hull's only lane is the one shared sink — the exact same-sink contention the
// mutex governs.
func idleArbSingleSinkClockHarness(t *testing.T, clock shared.Clock, hulls int, cfg IdleArbConfig) (*IdleArbDispatcher, *idleArbFakeShipRepo, *fakeIdleArbLauncher) {
	t.Helper()
	hub := idleArbWaypoint(t, "X1-HUB-E42", 0, 0)
	sink := idleArbWaypoint(t, "X1-HUB-D40", 0, 50)

	repo := &idleArbFakeShipRepo{}
	for i := 0; i < hulls; i++ {
		repo.ships = append(repo.ships, idleArbHull(t, fmt.Sprintf("TORWIND-%d", i+1), hub, testFleet))
	}
	graph := &fakeGraphProvider{waypoints: map[string]*shared.Waypoint{hub.Symbol: hub, sink.Symbol: sink}}
	markets := &idleArbFakeMarketRepo{markets: map[string]*market.Market{
		hub.Symbol:  marketAt(t, hub.Symbol, tradeGood(t, "MACHINERY", 90, 100)),
		sink.Symbol: marketAt(t, sink.Symbol, tradeGood(t, "MACHINERY", 350, 360)),
	}}
	launcher := &fakeIdleArbLauncher{repo: repo, clock: clock}
	d := NewIdleArbDispatcher(repo, markets, graph, launcher, nil, nil, clock, shared.MustNewPlayerID(1), testFleet, cfg)
	return d, repo, launcher
}

// releaseHull force-releases the named hull back to idle (clearing its container
// id), standing in for the leg's container terminating and the runner releasing
// the claim — the seam the lane mutex reconciles against.
func releaseHull(repo *idleArbFakeShipRepo, symbol string, clock shared.Clock) {
	for _, s := range repo.ships {
		if s.ShipSymbol() == symbol {
			s.ForceRelease("test_leg_complete", clock)
		}
	}
}

// Within one pass, surplus hulls sharing the only sink must launch exactly ONE
// leg — the first claims the lane, the rest are skipped:lane-held. (Reserve 1 of
// 3 hulls leaves TWO dispatchable onto the single sink — the contention the
// mutex governs.)
func TestIdleArbLaneMutex_WithinPass_SecondSameSinkHullSkipped(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	d, _, launcher := idleArbSingleSinkClockHarness(t, clock, 3, IdleArbConfig{ReserveHulls: 1})

	launched := d.DispatchOnce(context.Background())

	if launched != 1 || len(launcher.launches) != 1 {
		t.Fatalf("hulls sharing one sink must launch exactly ONE leg (lane mutex), got %d", launched)
	}
	if d.skipLaneHeld == 0 {
		t.Fatalf("the other same-sink hull(s) must be counted as lane-held skips, got %d", d.skipLaneHeld)
	}
}

// The mutex keys on (good, sink), NOT on good alone: two dispatchable hulls with
// two distinct sinks for the same good must BOTH launch — the guard prevents
// concurrent dumps, it does not throttle the harvest to one leg per good.
func TestIdleArbLaneMutex_DifferentSinksSameGood_BothLaunch(t *testing.T) {
	d, _, launcher := idleArbTwoSinkHarness(t, 3, IdleArbConfig{ReserveHulls: 1})

	launched := d.DispatchOnce(context.Background())

	if launched != 2 || len(launcher.launches) != 2 {
		t.Fatalf("two dispatchable hulls with two distinct sinks must BOTH launch (no over-blocking), got %d", launched)
	}
	sinks := map[string]bool{}
	for _, s := range launcher.launches {
		sinks[s.SellAt] = true
	}
	if len(sinks) != 2 {
		t.Fatalf("expected the two legs on two distinct sinks, got %v", sinks)
	}
}

// A leg that is still flying holds its lane across passes: the next pass must not
// re-dump the sink even though another hull is idle.
func TestIdleArbLaneMutex_CrossPass_FlyingLaneBlocksNextPass(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	d, _, launcher := idleArbSingleSinkClockHarness(t, clock, 3, IdleArbConfig{ReserveHulls: 1})

	// Pass 1: one leg launches onto the single sink; its hull stays claimed.
	if launched := d.DispatchOnce(context.Background()); launched != 1 {
		t.Fatalf("pass 1 must launch one leg, got %d", launched)
	}

	// Pass 2: the leg is STILL flying, so its sink is held — the other idle hull
	// cannot re-dump it. Zero launches, no collision.
	if launched := d.DispatchOnce(context.Background()); launched != 0 {
		t.Fatalf("a still-flying lane must block the next pass, got %d launches", launched)
	}
	if len(launcher.launches) != 1 {
		t.Fatalf("total launches across both passes must stay at one, got %d", len(launcher.launches))
	}
}

// The lane frees only AFTER its leg terminates AND the recovery hold elapses:
// inside the hold the sink stays closed even though a hull is idle again; past
// the hold it reopens. Driven on a MockClock.
func TestIdleArbLaneMutex_LaneFreesAfterTerminationPlusHold(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	const hold = 20 * time.Minute
	d, repo, launcher := idleArbSingleSinkClockHarness(t, clock, 3, IdleArbConfig{ReserveHulls: 1, RecoveryHold: hold})

	// Pass 1: TORWIND-1 launches onto the sink (TORWIND-2 is lane-held this pass).
	if launched := d.DispatchOnce(context.Background()); launched != 1 {
		t.Fatalf("pass 1 must launch one leg, got %d", launched)
	}
	launchedSym := launcher.launches[0].ShipSymbol

	// The leg completes: its hull is released back to idle.
	releaseHull(repo, launchedSym, clock)

	// Pass 2, INSIDE the recovery hold: the terminated lane is still held, so the
	// sink is not re-dumped even though a hull is idle again.
	clock.Advance(hold / 2)
	if launched := d.DispatchOnce(context.Background()); launched != 0 {
		t.Fatalf("inside the recovery hold the freed sink must stay closed, got %d launches", launched)
	}

	// Pass 3, PAST the hold: the lane frees and a hull may work the sink again.
	clock.Advance(hold + time.Minute)
	if launched := d.DispatchOnce(context.Background()); launched != 1 {
		t.Fatalf("past the recovery hold the sink must reopen, got %d launches", launched)
	}
}

// The per-candidate verdict log names the lane-held holder, so a collision the
// mutex prevented is legible in the candidate line the analyst scan diffs against.
func TestIdleArbLaneMutex_CandidateLog_NamesLaneHeldHolder(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	logger := &idleArbCapturingLogger{}
	d, _, _ := idleArbSingleSinkClockHarness(t, clock, 3, IdleArbConfig{ReserveHulls: 1})

	d.DispatchOnce(common.WithLogger(context.Background(), logger))

	var laneHeldLine string
	for _, m := range logger.messages {
		if strings.HasPrefix(m, "Idle-arb candidate:") && strings.Contains(m, "verdict skipped:lane-held") {
			laneHeldLine = m
		}
	}
	if laneHeldLine == "" {
		t.Fatalf("expected a candidate line with verdict skipped:lane-held, got %v", logger.messages)
	}
	if !strings.Contains(laneHeldLine, "flying") {
		t.Fatalf("a lane-held verdict must name the still-flying holding leg, got: %s", laneHeldLine)
	}
	// The harvest summary surfaces the lane-held skip count in message text too.
	summary := logger.messageWithPrefix(t, "Idle-arb harvest:")
	if !strings.Contains(summary, "lane-held") {
		t.Fatalf("harvest summary must carry the lane-held count, got: %s", summary)
	}
}

// compile-time guard: the harness's graph provider must satisfy the interface.
var _ system.ISystemGraphProvider = (*fakeGraphProvider)(nil)
