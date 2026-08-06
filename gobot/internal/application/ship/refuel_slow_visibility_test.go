package ship

// sp-ehf4x. A CONTRACT_WORKFLOW worker went silent for 3m15s holding contract cargo, and the
// diagnosis had to be made by counting log lines per minute by hand. The silence was NOT a wedge:
// the arrival event arrived, and the worker was blocked inside the alternate fuel stop's refuel —
// a bare dock/refuel/orbit trio with no logging of its own, while the API client retried
// underneath. It then failed, took the ordinary error path, and recovered on its own 5s later.
//
// These pin the visibility, which is the part that was actually missing. They do NOT pin a
// watchdog: the bead's acceptance #1 asks for one, and its own NOTES retract that — a worker that
// looks wedged here is about to recover, and killing it costs a re-selection cycle.

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// loggedLine is one captured container-log emission.
type loggedLine struct {
	level  string
	action string
	meta   map[string]interface{}
}

type capturingLogger struct{ lines []loggedLine }

func (c *capturingLogger) Log(level, _ string, meta map[string]interface{}) {
	action, _ := meta["action"].(string)
	c.lines = append(c.lines, loggedLine{level: level, action: action, meta: meta})
}

func (c *capturingLogger) find(action string) (loggedLine, bool) {
	for _, l := range c.lines {
		if l.action == action {
			return l, true
		}
	}
	return loggedLine{}, false
}

// blockingRefuelMediator models the incident's real shape: the refuel call itself consumes wall
// clock (the API client retrying inside it) and emits nothing. blockFor is charged to the mock
// clock on each RefuelShipCommand, so the executor observes elapsed time exactly as it would in
// production without the test sleeping.
type blockingRefuelMediator struct {
	clock    *shared.MockClock
	blockFor time.Duration
	failWith error
}

func (m *blockingRefuelMediator) Send(_ context.Context, request mediator.Request) (mediator.Response, error) {
	switch request.(type) {
	case *types.OrbitShipCommand:
		return &types.OrbitShipResponse{Status: "in_orbit"}, nil
	case *types.DockShipCommand:
		return &types.DockShipResponse{Status: "docked"}, nil
	case *types.RefuelShipCommand:
		m.clock.CurrentTime = m.clock.CurrentTime.Add(m.blockFor)
		if m.failWith != nil {
			return nil, m.failWith
		}
		return &types.RefuelShipResponse{}, nil
	}
	return nil, nil
}

func (m *blockingRefuelMediator) Register(reflect.Type, mediator.RequestHandler) error { return nil }
func (m *blockingRefuelMediator) RegisterMiddleware(mediator.Middleware)               {}

func slowRefuelFixture(t *testing.T, blockFor time.Duration, failWith error) (*RouteExecutor, *domainNavigation.Ship, *capturingLogger) {
	t.Helper()
	station := mustWaypoint(t, "X1-KP46-D38", 0, 0)
	station.HasFuel = true
	station.Traits = []string{"MARKETPLACE"}

	ship := newExecutorTestShip(t, 100, 600, station)
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 8, 5, 0, 24, 18, 0, time.UTC)}
	med := &blockingRefuelMediator{clock: clock, blockFor: blockFor, failWith: failWith}

	return NewRouteExecutor(nil, med, clock, nil, nil, nil, nil, stubSubscriber{}), ship, &capturingLogger{}
}

// THE DEFECT. A refuel that blocks for minutes must say so. Against the uninstrumented code this
// call emits nothing at all, which is exactly why the incident had to be read off log cadence.
func TestRefuelShip_ReportsARefuelThatBlockedPastTheThreshold(t *testing.T) {
	executor, ship, logger := slowRefuelFixture(t, 3*time.Minute+15*time.Second, nil)

	err := executor.refuelShip(common.WithLogger(context.Background(), logger), ship, shared.MustNewPlayerID(1), true)
	if err != nil {
		t.Fatalf("this refuel succeeds; the point is that it was SLOW: %v", err)
	}

	line, ok := logger.find("refuel_slow")
	if !ok {
		t.Fatalf("a refuel that blocked 3m15s emitted no refuel_slow line, so the stall is invisible; captured %d line(s)", len(logger.lines))
	}
	if line.level != "WARNING" {
		t.Errorf("a multi-minute silent block is a WARNING, got %q", line.level)
	}
	if got, _ := line.meta["elapsed_seconds"].(int); got != 195 {
		t.Errorf("elapsed_seconds = %d, want 195 (the observed 3m15s window)", got)
	}
}

// THE INCIDENT ENDED IN AN ERROR, and a report only on success would have stayed silent through
// exactly the case it exists to surface.
func TestRefuelShip_ReportsASlowRefuelThatEndedInFailure(t *testing.T) {
	executor, ship, logger := slowRefuelFixture(t, 3*time.Minute+15*time.Second, errors.New("max retries exceeded: server error (500)"))

	if err := executor.refuelShip(common.WithLogger(context.Background(), logger), ship, shared.MustNewPlayerID(1), true); err == nil {
		t.Fatal("precondition: this refuel must fail")
	}

	if _, ok := logger.find("refuel_slow"); !ok {
		t.Fatal("a slow refuel that FAILED emitted no refuel_slow line; the observed incident took exactly this path")
	}
}

// A healthy fleet must stay quiet, or the signal drowns in routine noise the way the bead's own
// #3 warns about.
func TestRefuelShip_SaysNothingAboutAPromptRefuel(t *testing.T) {
	executor, ship, logger := slowRefuelFixture(t, time.Second, nil)

	if err := executor.refuelShip(common.WithLogger(context.Background(), logger), ship, shared.MustNewPlayerID(1), true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if line, ok := logger.find("refuel_slow"); ok {
		t.Fatalf("a 1s refuel must not be reported as slow, got %v", line.meta)
	}
}

// The reroute path must NAME the call that is about to block, before it blocks. In the incident
// the last line for 3m15s was "Ship arrival event received" at the alternate — which reads as a
// healthy arrival, not as the start of a blocking refuel, and is what made the silence look like a
// wedge. reportSlowRefuel measures the window afterwards; this line attributes it while it is open.
func TestRefuelAtAlternateStop_AnnouncesTheBlockingRefuelBeforeItBlocks(t *testing.T) {
	origin := mustWaypoint(t, "X1-KP46-D39", 0, 0)
	origin.HasFuel = true
	origin.Traits = []string{"MARKETPLACE"}

	alt := mustWaypoint(t, "X1-KP46-D38", 30, 0)
	alt.HasFuel = true
	alt.Traits = []string{"MARKETPLACE"}

	ship := newExecutorTestShip(t, 100, 600, origin)
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 8, 5, 0, 23, 44, 0, time.UTC)}
	med := &recordingMediator{
		fuel:                    100,
		capacity:                600,
		distByDest:              map[string]float64{alt.Symbol: 30},
		refuelAlwaysErr:         refuelIncidentSignature(),
		refuelFailsUntilReroute: true,
		clock:                   clock,
	}
	repo := &fakeWaypointRepo{bySystemTrait: map[string][]*shared.Waypoint{
		origin.SystemSymbol + "|MARKETPLACE": {origin, alt},
	}}
	executor := NewRouteExecutor(nil, med, clock, nil, nil, nil, repo, stubSubscriber{})
	logger := &capturingLogger{}

	if err := executor.refuelShipWithRetry(common.WithLogger(context.Background(), logger), ship, shared.MustNewPlayerID(1), true); err != nil {
		t.Fatalf("the alternate refuels fine here: %v", err)
	}

	started, ok := logger.find("refuel_alternate_started")
	if !ok {
		t.Fatal("the alternate-stop refuel was never announced, so its blocking window has no attribution while it is open")
	}
	if got, _ := started.meta["alternate"].(string); got != alt.Symbol {
		t.Errorf("the announcement must name the alternate it is refuelling at, got %q", got)
	}
	// Ordering is the whole point: announced BEFORE the reroute log that precedes the block, and
	// before any slow report, which can only be emitted afterwards.
	var sawReroute bool
	for _, l := range logger.lines {
		if l.action == "refuel_reroute" {
			sawReroute = true
		}
		if l.action == "refuel_alternate_started" && !sawReroute {
			t.Fatal("the announcement must follow the reroute decision it belongs to")
		}
	}
}

// The threshold must sit above a healthy refuel and below the windows actually observed
// (3m15s and 3m20s), stated as inequalities against the incident rather than equality with the
// constant, so a retune that still brackets the incident is free.
func TestSlowRefuelThreshold_BracketsTheObservedIncident(t *testing.T) {
	const observedWindow = 3*time.Minute + 15*time.Second
	if SlowRefuelThreshold >= observedWindow {
		t.Errorf("SlowRefuelThreshold = %v would not report the %v window this bead exists for", SlowRefuelThreshold, observedWindow)
	}
	if SlowRefuelThreshold < 10*time.Second {
		t.Errorf("SlowRefuelThreshold = %v is inside the range of a healthy three-call refuel and will report routine traffic", SlowRefuelThreshold)
	}
}
