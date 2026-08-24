package commands

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// routeETALogEntry captures one structured log call so this file's test can assert on the
// ranking_mode field the ship-selection completion log carries. completionCapturingLogger
// (run_fleet_coordinator_completion_test.go) discards the fields map since its tests only ever
// needed message text; shipSelectorCapturingLogger (internal/application/contract/ship_selector_test.go)
// keeps it but lives in a different package, so it is not directly reusable here.
type routeETALogEntry struct {
	level   string
	message string
	fields  map[string]interface{}
}

type routeETACapturingLogger struct {
	entries []routeETALogEntry
}

func (l *routeETACapturingLogger) Log(level, message string, fields map[string]interface{}) {
	l.entries = append(l.entries, routeETALogEntry{level: level, message: message, fields: fields})
}

// findByAction returns the last logged entry whose "action" field matches, searching
// newest-first (mirrors shipSelectorCapturingLogger.findByAction).
func (l *routeETACapturingLogger) findByAction(action string) (routeETALogEntry, bool) {
	for i := len(l.entries) - 1; i >= 0; i-- {
		if act, _ := l.entries[i].fields["action"].(string); act == action {
			return l.entries[i], true
		}
	}
	return routeETALogEntry{}, false
}

// TestFleetCoordinator_NoRouteETAEstimatorSet_SelectionFallsBackOpen pins the optional-port
// default for RunFleetCoordinatorHandler.routeETAEstimator: a coordinator whose
// SetRouteETAEstimator carries a nil estimator - the shape of a daemon boot with no routing
// client threaded, or simply never wired - must still drive its real Handle() dispatch loop all
// the way through the SelectClosestShip call site to a normal selection, never a nil-pointer
// panic and never a refusal. This mirrors the fail-open contract SetDedicatedFleetSeedMarker(nil)
// and SetDepotRegistryProvider(nil) already provide elsewhere in this package.
//
// Full estimator ranking behaviour (route_eta mode, per-candidate drops, a globally-failed
// estimate) is Task 2/3's coverage (internal/application/contract/ship_selector_test.go and
// route_eta_test.go); this test's only job is the coordinator-level WIRING of the nil-estimator
// path end to end through Handle().
func TestFleetCoordinator_NoRouteETAEstimatorSet_SelectionFallsBackOpen(t *testing.T) {
	hauler := settleWindowHauler(t, "TORWIND-7")
	repo := &singleHullFakeShipRepo{ships: []*navigation.Ship{hauler}}
	daemonClient := newSettleWindowDaemonClient()
	containerRepo := &reclaimFakeContainerRepo{}
	workerCh := make(chan navigation.WorkerCompletedEvent)
	mockClock := &shared.MockClock{CurrentTime: time.Now()}
	logger := &routeETACapturingLogger{}

	handler := &RunFleetCoordinatorHandler{
		workerLifecycleManager: contractServices.NewWorkerLifecycleManager(daemonClient, containerRepo, repo),
		contractMarketService:  contractServices.NewContractMarketService(nil, &activeContractRepo{c: fulfillVerifyContract(t, "C-1", "LIQUID_NITROGEN", 500, 0)}),
		shipRepo:               repo,
		daemonClient:           daemonClient,
		graphProvider:          &placementStubGraphProvider{graph: settleWindowGraph(t)},
		clock:                  mockClock,
		eventSubscriber:        &reclaimFakeSubscriber{workerCompleted: workerCh},
	}
	handler.SetInventoryFinder(inventoryFinderStub{good: "LIQUID_NITROGEN", units: 500, waypoint: "X1-TEST-A1"})
	// The port under test: explicitly wired to nil, the "no estimator" shape the optional-port
	// contract must degrade quietly from (mirrors the sibling SetXxx(nil) calls referenced above).
	handler.SetRouteETAEstimator(nil)

	ctx, cancel := context.WithTimeout(common.WithLogger(context.Background(), logger), 900*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_, _ = handler.Handle(ctx, contractSpawnCommand())
		close(done)
	}()

	select {
	case <-daemonClient.startedSignal:
		// A dispatch happened: the nil estimator did not block or panic selection.
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the dispatch - a nil route-ETA estimator must never block selection")
	}

	cancel()
	<-done

	if got := len(daemonClient.started); got != 1 {
		t.Fatalf("expected exactly one dispatch with a nil estimator, got %d: %v", got, daemonClient.started)
	}

	entry, ok := logger.findByAction("ship_selected")
	if !ok {
		t.Fatalf("expected a ship_selected completion log entry, got %+v", logger.entries)
	}
	if entry.level != "INFO" {
		t.Fatalf("expected the ship-selection completion log at INFO, got %s", entry.level)
	}
	if mode, _ := entry.fields["ranking_mode"].(string); mode != "fallback_straight_line" {
		t.Fatalf("expected ranking_mode=fallback_straight_line with no estimator set, got %v", entry.fields["ranking_mode"])
	}
	if _, warned := logger.findByAction("route_eta_fallback"); warned {
		t.Fatalf("a nil estimator must never log the route-eta-unavailable warning - it was never attempted")
	}
}
