package contract

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Observability: the "Ship selection completed" log naming only the winner
// makes a pick impossible to audit - was a closer candidate even in the pool?
// The candidate summary must enumerate every candidate with its distance to
// the target and mark the command ship, e.g.
// "TORWIND-3@0.00, TORWIND-1@50.00(command)".
func TestSummarizeCandidates_EnumeratesEveryCandidateWithDistanceMarkingCommand(t *testing.T) {
	target, err := shared.NewWaypoint("X1-TW-MKT", 0, 0)
	if err != nil {
		t.Fatalf("build target waypoint: %v", err)
	}
	hauler := newCandidateShip(t, "TORWIND-3", "HAULER", 30, 0, 0)     // at target -> 0.00
	command := newCandidateShip(t, "TORWIND-1", "COMMAND", 40, 30, 40) // 3-4-5 -> 50.00, command

	summary := summarizeCandidates([]*navigation.Ship{hauler, command}, nil, target, nil)

	if !strings.Contains(summary, "TORWIND-3@0.00") {
		t.Fatalf("candidate summary %q must list the hauler with its distance", summary)
	}
	if !strings.Contains(summary, "TORWIND-1@50.00(command)") {
		t.Fatalf("candidate summary %q must list the command ship with its distance and a (command) mark", summary)
	}
}

// A candidate with a priced route ETA renders it alongside its straight-line
// distance, so the completion log stays auditable against the ETA that
// actually decided the pick, not just the straight-line figure it overrode.
func TestSummarizeCandidates_RendersETAWhenPresentForSymbol(t *testing.T) {
	target, err := shared.NewWaypoint("X1-TW-MKT", 0, 0)
	if err != nil {
		t.Fatalf("build target waypoint: %v", err)
	}
	priced := newCandidateShip(t, "TORWIND-3", "HAULER", 30, 0, 0)
	unpriced := newCandidateShip(t, "TORWIND-1", "COMMAND", 40, 30, 40)
	etas := map[string]float64{"TORWIND-3": 42}

	summary := summarizeCandidates([]*navigation.Ship{priced, unpriced}, nil, target, etas)

	if !strings.Contains(summary, "TORWIND-3@0.00/42.00s") {
		t.Fatalf("candidate summary %q must render the priced ETA alongside distance", summary)
	}
	if !strings.Contains(summary, "TORWIND-1@50.00(command)") {
		t.Fatalf("candidate summary %q must leave an unpriced candidate's entry unchanged", summary)
	}
}

// A candidate excluded before ranking - dropped by the estimator as unroutable, or excluded from
// a straight-line fallback for being in transit - must still appear in the audit line, marked
// /DROPPED, or an operator reading "Ship selection completed" cannot tell a nearer hull was ever
// in the pool at all.
func TestSummarizeCandidates_MarksDroppedCandidatesInAuditLine(t *testing.T) {
	target, err := shared.NewWaypoint("X1-TW-MKT", 0, 0)
	if err != nil {
		t.Fatalf("build target waypoint: %v", err)
	}
	kept := newCandidateShip(t, "TORWIND-3", "HAULER", 30, 0, 0) // at target -> 0.00
	droppedCommand := newCandidateShip(t, "TORWIND-1", "COMMAND", 40, 30, 40)

	summary := summarizeCandidates([]*navigation.Ship{kept}, []*navigation.Ship{droppedCommand}, target, nil)

	if !strings.Contains(summary, "TORWIND-3@0.00") {
		t.Fatalf("candidate summary %q must still list the kept candidate", summary)
	}
	if !strings.Contains(summary, "TORWIND-1@50.00/DROPPED(command)") {
		t.Fatalf("candidate summary %q must mark the dropped candidate /DROPPED with its distance, command mark included", summary)
	}
}

// A ship the estimator drops for having a nil CurrentLocation() (route_eta.go's own nil-location
// drop cause) now flows into summarizeCandidates' dropped slice - it must render safely rather
// than panic on DistanceTo's pointer-receiver nil deref.
func TestSummarizeCandidates_NilLocationDroppedCandidateDoesNotPanic(t *testing.T) {
	target, err := shared.NewWaypoint("X1-TW-MKT", 0, 0)
	if err != nil {
		t.Fatalf("build target waypoint: %v", err)
	}
	noLocation := newETAShipNilLocation(t, "TORWIND-N")

	summary := summarizeCandidates(nil, []*navigation.Ship{noLocation}, target, nil)

	if !strings.Contains(summary, "TORWIND-N") {
		t.Fatalf("candidate summary %q must still name the nil-location dropped candidate", summary)
	}
	if !strings.Contains(summary, "DROPPED") {
		t.Fatalf("candidate summary %q must still mark it DROPPED", summary)
	}
}

// --- SelectClosestShip: estimator-mode selection -------------------------
//
// These fakes/helpers reuse the same-package fixtures already established for
// the pool (stubShipRepo, ship_pool_manager_test.go), the route ETA estimator
// (fakeRoutingClient/fakeAnswer/testClock, route_eta_test.go) and the graph
// provider (fakeGraphProvider, idle_arb_test.go) rather than redeclaring them.

// shipSelectorLogEntry captures one structured log call so a test can assert
// on a specific field (e.g. ranking_mode) - the other capturingLogger fakes in
// this package (ship_pool_manager_test.go, idle_arb_test.go) discard the
// fields map since their tests only ever needed message text.
type shipSelectorLogEntry struct {
	level   string
	message string
	fields  map[string]interface{}
}

type shipSelectorCapturingLogger struct {
	entries []shipSelectorLogEntry
}

func (l *shipSelectorCapturingLogger) Log(level, message string, fields map[string]interface{}) {
	l.entries = append(l.entries, shipSelectorLogEntry{level: level, message: message, fields: fields})
}

// findByAction returns the last logged entry whose "action" field matches,
// searching newest-first so a completion line is found even after an earlier
// warning logged its own distinct action.
func (l *shipSelectorCapturingLogger) findByAction(action string) (shipSelectorLogEntry, bool) {
	for i := len(l.entries) - 1; i >= 0; i-- {
		if act, _ := l.entries[i].fields["action"].(string); act == action {
			return l.entries[i], true
		}
	}
	return shipSelectorLogEntry{}, false
}

// newRankedCandidateShip builds an idle, docked hauler at (x,y) under its own
// waypoint symbol, so a test can control straight-line distance (via x,y) and
// route ETA (keyed by waypointSymbol in fakeRoutingClient.perShip)
// independently - neither newCandidateShip (one shared waypoint symbol for
// every ship) nor newETAShip (fixed origin coordinates) can vary both at once.
func newRankedCandidateShip(t *testing.T, symbol, waypointSymbol string, x, y float64) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("build cargo: %v", err)
	}
	fuel, err := shared.NewFuel(80, 100)
	if err != nil {
		t.Fatalf("build fuel: %v", err)
	}
	wp, err := shared.NewWaypoint(waypointSymbol, x, y)
	if err != nil {
		t.Fatalf("build waypoint: %v", err)
	}
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), wp, fuel, 100, 40, cargo, 30, "FRAME_LIGHT_HAULER", "HAULER", nil, navigation.NavStatusDocked)
	if err != nil {
		t.Fatalf("build ship: %v", err)
	}
	return ship
}

// selectClosestShipHarness wires the fakes SelectClosestShip needs beyond the
// ships themselves: a repo serving exactly those ships, and a graph provider
// resolving the goal waypoint the estimator and the domain selector both need.
func selectClosestShipHarness(ships []*navigation.Ship, target *shared.Waypoint) (*stubShipRepo, *fakeGraphProvider) {
	repo := &stubShipRepo{ships: ships}
	graph := &fakeGraphProvider{waypoints: map[string]*shared.Waypoint{etaTestGoal: target}}
	return repo, graph
}

// A nil estimator must produce the pre-estimator behavior byte-for-byte, plus
// the new ranking_mode field - never attempting a route call, never logging
// the fallback warning (there was nothing to fail).
func TestSelectClosestShip_NilEstimator_FallsBackToStraightLine(t *testing.T) {
	target, err := shared.NewWaypoint(etaTestGoal, 0, 0)
	if err != nil {
		t.Fatalf("build target waypoint: %v", err)
	}
	near := newRankedCandidateShip(t, "TORWIND-NEAR", "X1-TW-NEAR", 5, 0)
	far := newRankedCandidateShip(t, "TORWIND-FAR", "X1-TW-FAR", 50, 0)
	repo, graph := selectClosestShipHarness([]*navigation.Ship{near, far}, target)
	logger := &shipSelectorCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	selected, _, err := SelectClosestShip(ctx, []string{"TORWIND-NEAR", "TORWIND-FAR"}, repo, graph, nil,
		etaTestGoal, "", 10, 1, nil, nil, nil)
	if err != nil {
		t.Fatalf("SelectClosestShip: %v", err)
	}
	if selected != "TORWIND-NEAR" {
		t.Fatalf("expected the straight-line-nearest hull TORWIND-NEAR with a nil estimator, got %s", selected)
	}

	entry, ok := logger.findByAction("ship_selected")
	if !ok {
		t.Fatalf("expected a ship_selected completion log entry, got %+v", logger.entries)
	}
	if mode, _ := entry.fields["ranking_mode"].(string); mode != "fallback_straight_line" {
		t.Fatalf("expected ranking_mode=fallback_straight_line for a nil estimator, got %v", entry.fields["ranking_mode"])
	}
	if _, warned := logger.findByAction("route_eta_fallback"); warned {
		t.Fatalf("a nil estimator must never log the route-eta-unavailable warning - it was never attempted")
	}
}

// An in-transit hull's CurrentLocation() IS its destination, so a straight-line fallback pricing
// it from there prices its entire remaining transit as ZERO - a fictional number that can beat a
// genuinely closer idle hull. An in-transit hull must only ever be a candidate when route-ETA
// mode is LIVE and priced it. newInTransitETAShip places its ship at (0,0) - exactly the
// target's coordinates in every test in this file - so an un-excluded in-transit hull would win
// with a straight-line distance of 0.00 against ANY idle candidate, proving true exclusion
// rather than a mere tiebreak loss.
func TestSelectClosestShip_NilEstimator_ExcludesUnclaimedInTransitFromFallback(t *testing.T) {
	target, err := shared.NewWaypoint(etaTestGoal, 0, 0)
	if err != nil {
		t.Fatalf("build target waypoint: %v", err)
	}
	idle := newRankedCandidateShip(t, "TORWIND-IDLE", "X1-TW-IDLE", 5, 0)
	transiting := newInTransitETAShip(t, "TORWIND-TRANSIT", "X1-TW-TRANSIT", nil)
	repo, graph := selectClosestShipHarness([]*navigation.Ship{idle, transiting}, target)
	logger := &shipSelectorCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	selected, _, err := SelectClosestShip(ctx, []string{"TORWIND-IDLE", "TORWIND-TRANSIT"}, repo, graph, nil,
		etaTestGoal, "", 10, 1, nil, nil, nil)
	if err != nil {
		t.Fatalf("SelectClosestShip: %v", err)
	}
	if selected != "TORWIND-IDLE" {
		t.Fatalf("expected the idle hull TORWIND-IDLE; an in-transit hull must never win a straight-line fallback on a fictional zero distance, got %s", selected)
	}

	entry, ok := logger.findByAction("ship_selected")
	if !ok {
		t.Fatalf("expected a ship_selected completion log entry, got %+v", logger.entries)
	}
	candidates, _ := entry.fields["candidates"].(string)
	if !strings.Contains(candidates, "TORWIND-TRANSIT@0.00/DROPPED") {
		t.Fatalf("expected the excluded in-transit hull marked /DROPPED (not even ranked) in the audit line, got %q", candidates)
	}
}

// The OK=false sibling of the test above: a globally-failed estimate (transport error, budget
// overrun, or every candidate unroutable) degrades to the SAME straight-line fallback, so the
// same exclusion must apply regardless of WHY the estimator didn't produce usable ETAs.
func TestSelectClosestShip_EstimatorOKFalse_ExcludesUnclaimedInTransitFromFallback(t *testing.T) {
	target, err := shared.NewWaypoint(etaTestGoal, 0, 0)
	if err != nil {
		t.Fatalf("build target waypoint: %v", err)
	}
	idle := newRankedCandidateShip(t, "TORWIND-IDLE", "X1-TW-IDLE", 5, 0)
	transiting := newInTransitETAShip(t, "TORWIND-TRANSIT", "X1-TW-TRANSIT", nil)
	repo, graph := selectClosestShipHarness([]*navigation.Ship{idle, transiting}, target)
	logger := &shipSelectorCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)
	fake := &fakeRoutingClient{perShip: map[string]fakeAnswer{
		"X1-TW-IDLE": {err: context.DeadlineExceeded}, // transport-class error: fails the whole batch open
	}}
	estimator := NewRouteETAEstimator(fake, testClock())

	selected, _, err := SelectClosestShip(ctx, []string{"TORWIND-IDLE", "TORWIND-TRANSIT"}, repo, graph, nil,
		etaTestGoal, "", 10, 1, estimator, nil, nil)
	if err != nil {
		t.Fatalf("SelectClosestShip: %v", err)
	}
	if selected != "TORWIND-IDLE" {
		t.Fatalf("expected the idle hull TORWIND-IDLE once the estimator fails globally; an in-transit hull must never win the fallback on a fictional zero distance, got %s", selected)
	}

	entry, ok := logger.findByAction("ship_selected")
	if !ok {
		t.Fatalf("expected a ship_selected completion log entry, got %+v", logger.entries)
	}
	if mode, _ := entry.fields["ranking_mode"].(string); mode != "fallback_straight_line" {
		t.Fatalf("expected ranking_mode=fallback_straight_line once the estimator fails globally, got %v", entry.fields["ranking_mode"])
	}
	candidates, _ := entry.fields["candidates"].(string)
	if !strings.Contains(candidates, "TORWIND-TRANSIT@0.00/DROPPED") {
		t.Fatalf("expected the excluded in-transit hull marked /DROPPED (not even ranked) in the audit line, got %q", candidates)
	}
}

// ETAs that invert the straight-line order must flip the winner: the domain
// ranking (Task 1) already ranks on a supplied eta map over cruise time: this
// proves the estimator's output actually reaches it end to end.
func TestSelectClosestShip_EstimatorInvertsOrder_ETAWinnerSelected(t *testing.T) {
	target, err := shared.NewWaypoint(etaTestGoal, 0, 0)
	if err != nil {
		t.Fatalf("build target waypoint: %v", err)
	}
	near := newRankedCandidateShip(t, "TORWIND-NEAR", "X1-TW-NEAR", 5, 0)
	far := newRankedCandidateShip(t, "TORWIND-FAR", "X1-TW-FAR", 50, 0)
	repo, graph := selectClosestShipHarness([]*navigation.Ship{near, far}, target)
	logger := &shipSelectorCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)
	fake := &fakeRoutingClient{perShip: map[string]fakeAnswer{
		"X1-TW-NEAR": {seconds: 500}, // slow route despite being straight-line-nearest
		"X1-TW-FAR":  {seconds: 10},  // fast route despite being straight-line-farthest
	}}
	estimator := NewRouteETAEstimator(fake, testClock())

	selected, _, err := SelectClosestShip(ctx, []string{"TORWIND-NEAR", "TORWIND-FAR"}, repo, graph, nil,
		etaTestGoal, "", 10, 1, estimator, nil, nil)
	if err != nil {
		t.Fatalf("SelectClosestShip: %v", err)
	}
	if selected != "TORWIND-FAR" {
		t.Fatalf("expected the route-ETA winner TORWIND-FAR despite being straight-line-farthest, got %s", selected)
	}

	entry, ok := logger.findByAction("ship_selected")
	if !ok {
		t.Fatalf("expected a ship_selected completion log entry, got %+v", logger.entries)
	}
	if mode, _ := entry.fields["ranking_mode"].(string); mode != "route_eta" {
		t.Fatalf("expected ranking_mode=route_eta, got %v", entry.fields["ranking_mode"])
	}
	candidates, _ := entry.fields["candidates"].(string)
	if !strings.Contains(candidates, "/500.00s") || !strings.Contains(candidates, "/10.00s") {
		t.Fatalf("expected the candidate summary to render both priced ETAs, got %q", candidates)
	}
}

// A globally-failed estimate (OK=false) must fall back to straight-line for
// EVERY candidate - never a mix of real ETAs and straight-line numbers - and
// name the cause via a WARN so an operator can see ranking degraded.
func TestSelectClosestShip_EstimatorFailsGlobally_FallsBackWithWarning(t *testing.T) {
	target, err := shared.NewWaypoint(etaTestGoal, 0, 0)
	if err != nil {
		t.Fatalf("build target waypoint: %v", err)
	}
	near := newRankedCandidateShip(t, "TORWIND-NEAR", "X1-TW-NEAR", 5, 0)
	far := newRankedCandidateShip(t, "TORWIND-FAR", "X1-TW-FAR", 50, 0)
	repo, graph := selectClosestShipHarness([]*navigation.Ship{near, far}, target)
	logger := &shipSelectorCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)
	fake := &fakeRoutingClient{perShip: map[string]fakeAnswer{
		"X1-TW-NEAR": {err: context.DeadlineExceeded}, // transport-class error: fails the whole batch open
		"X1-TW-FAR":  {seconds: 10},
	}}
	estimator := NewRouteETAEstimator(fake, testClock())

	selected, _, err := SelectClosestShip(ctx, []string{"TORWIND-NEAR", "TORWIND-FAR"}, repo, graph, nil,
		etaTestGoal, "", 10, 1, estimator, nil, nil)
	if err != nil {
		t.Fatalf("SelectClosestShip: %v", err)
	}
	if selected != "TORWIND-NEAR" {
		t.Fatalf("expected the straight-line winner TORWIND-NEAR once the estimator fails globally, got %s", selected)
	}

	entry, ok := logger.findByAction("ship_selected")
	if !ok {
		t.Fatalf("expected a ship_selected completion log entry, got %+v", logger.entries)
	}
	if mode, _ := entry.fields["ranking_mode"].(string); mode != "fallback_straight_line" {
		t.Fatalf("expected ranking_mode=fallback_straight_line once the estimator fails globally, got %v", entry.fields["ranking_mode"])
	}

	warn, warned := logger.findByAction("route_eta_fallback")
	if !warned {
		t.Fatalf("expected a route_eta_fallback warning naming the cause, got %+v", logger.entries)
	}
	if warn.level != "WARNING" {
		t.Fatalf("expected the route-eta-unavailable log at WARNING, got %s", warn.level)
	}
	// The WARN must actually NAME the cause, not just exist.
	if cause, _ := warn.fields["cause"].(string); cause != "transport_error" {
		t.Fatalf("expected the route_eta_fallback warning to carry cause=transport_error, got %v", warn.fields["cause"])
	}
}

// A candidate the estimator drops as individually unroutable must never win,
// even when it is the nearest by straight-line - it is excluded from the
// domain ranking entirely rather than merely deprioritized.
func TestSelectClosestShip_DroppedCandidateNeverWins(t *testing.T) {
	target, err := shared.NewWaypoint(etaTestGoal, 0, 0)
	if err != nil {
		t.Fatalf("build target waypoint: %v", err)
	}
	unroutable := newRankedCandidateShip(t, "TORWIND-NEAR", "X1-TW-NEAR", 1, 0) // straight-line nearest, but unroutable
	routable := newRankedCandidateShip(t, "TORWIND-FAR", "X1-TW-FAR", 100, 0)   // straight-line farthest, routable
	repo, graph := selectClosestShipHarness([]*navigation.Ship{unroutable, routable}, target)
	logger := &shipSelectorCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)
	fake := &fakeRoutingClient{perShip: map[string]fakeAnswer{
		"X1-TW-NEAR": {err: errors.New("no route found")}, // unroutable: this hull only, does not fail the batch
		"X1-TW-FAR":  {seconds: 20},
	}}
	estimator := NewRouteETAEstimator(fake, testClock())

	selected, _, err := SelectClosestShip(ctx, []string{"TORWIND-NEAR", "TORWIND-FAR"}, repo, graph, nil,
		etaTestGoal, "", 10, 1, estimator, nil, nil)
	if err != nil {
		t.Fatalf("SelectClosestShip: %v", err)
	}
	if selected != "TORWIND-FAR" {
		t.Fatalf("expected the routable hull TORWIND-FAR; the dropped nearest-by-distance hull must never win, got %s", selected)
	}

	entry, ok := logger.findByAction("ship_selected")
	if !ok {
		t.Fatalf("expected a ship_selected completion log entry, got %+v", logger.entries)
	}
	if mode, _ := entry.fields["ranking_mode"].(string); mode != "route_eta" {
		t.Fatalf("expected ranking_mode=route_eta (one candidate still priced), got %v", entry.fields["ranking_mode"])
	}
}
