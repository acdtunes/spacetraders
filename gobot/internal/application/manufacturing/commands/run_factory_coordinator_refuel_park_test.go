package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship"
)

// sp-vsfn added ship.ErrRefuelUnrecoverable so a refuel that exhausts its
// retry-then-reroute budget can be PARKED by its caller instead of crashing
// the whole chain the way goods_factory-SHIP_PARTS-c7e2ecb2 did (mirrors
// sp-pafv's arrival-wait park pattern: ErrArrivalWaitExhausted). sp-npyr asks
// this coordinator to catch it the same way sp-pafv's callers do.
//
// executeLevelParallel's collection loop parks (excludes from results, does
// NOT abort the level or the run for) every worker error except a container
// shutdown signal (see isContainerShutdownSignal) — a catch-all, not an
// allow-list, so the next unclassified transient (like the
// orbit-while-in-transit 400/4214 race that reopened this issue once
// already) is parked too instead of re-crashing the coordinator. This test
// proves an unrecoverable refuel (a) no longer fails the coordinator run at
// all — Handle returns a completed, error-free response — and (b) still gets
// a distinct, verbatim-named WARNING instead of the opaque generic "Worker
// failed" ERROR, so the park remains diagnosable at a glance.
func TestExecuteLevelParallel_RefuelUnrecoverable_LogsDistinctParkWarning(t *testing.T) {
	f := newFactoryFixture(t)
	f.mediator.navigateRouteErr = &ship.ErrRefuelUnrecoverable{
		ShipSymbol: "CRAFTY-3",
		Waypoint:   testFactoryWaypoint,
		Attempts:   3,
		Cause:      errors.New("server error (500)"),
	}

	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	resp, err := f.handler.Handle(ctx, f.cmd)
	if err != nil {
		t.Fatalf("expected the coordinator to PARK the unrecoverable refuel and complete cleanly, got error: %v", err)
	}
	coordResp, ok := resp.(*RunFactoryCoordinatorResponse)
	if !ok || !coordResp.Completed {
		t.Fatalf("expected a completed response despite the parked node, got: %+v", resp)
	}

	foundParkWarning := false
	foundGenericFailure := false
	for _, e := range logger.entries {
		if e.level == "WARNING" && strings.Contains(e.message, "unrecoverable refuel") {
			foundParkWarning = true
		}
		if e.level == "ERROR" && e.message == "Worker failed" {
			foundGenericFailure = true
		}
	}
	if !foundParkWarning {
		t.Fatalf("expected a distinct WARNING naming the unrecoverable refuel, got entries: %+v", logger.entries)
	}
	if foundGenericFailure {
		t.Fatalf("expected the generic 'Worker failed' ERROR log replaced by the distinct park WARNING for this error type, got entries: %+v", logger.entries)
	}
}

// Regression: an ordinary, unclassified worker failure (not an unrecoverable
// refuel, arrival-wait exhaustion, or cargo-space error) is ALSO parked by
// the sp-vsfn catch-all, not just the specifically classified error types. It
// keeps logging through the original generic "Worker failed" ERROR path (the
// switch's default case in executeLevelParallel) — so an unrecognized
// failure stays visible as a candidate for its own classified branch later —
// but per the catch-all design it must NOT fail the coordinator run: Handle
// still returns a completed, error-free response.
func TestExecuteLevelParallel_OrdinaryWorkerFailure_KeepsGenericErrorLog(t *testing.T) {
	f := newFactoryFixture(t)
	f.mediator.navigateRouteErr = errors.New("boom: unrelated navigation failure")

	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	resp, err := f.handler.Handle(ctx, f.cmd)
	if err != nil {
		t.Fatalf("expected the coordinator to PARK the ordinary worker failure and complete cleanly, got error: %v", err)
	}
	coordResp, ok := resp.(*RunFactoryCoordinatorResponse)
	if !ok || !coordResp.Completed {
		t.Fatalf("expected a completed response despite the parked node, got: %+v", resp)
	}

	foundGenericFailure := false
	for _, e := range logger.entries {
		if e.level == "ERROR" && e.message == "Worker failed" {
			foundGenericFailure = true
		}
	}
	if !foundGenericFailure {
		t.Fatalf("expected the generic 'Worker failed' ERROR log preserved for a non-refuel failure, got entries: %+v", logger.entries)
	}
}
