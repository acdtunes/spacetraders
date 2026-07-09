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
// It already does, generically: executeCoordination's error path
// unconditionally releases every ship assignment before returning any worker
// error (never crashes, never leaves a zombie claim), and this coordinator
// carries no persistent in-memory chain state beyond ship DB rows to
// preserve. What was missing was purely observability — executeLevelParallel
// logged every worker failure identically ("Worker failed", ERROR, cause
// buried in a metadata field), giving no way to tell a transient refuel
// exhaustion (which resolves itself once the ship is re-claimed on a later
// run) apart from a genuine production bug. This test proves an unrecoverable
// refuel now gets a distinct, verbatim-named WARNING instead.
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

	_, err := f.handler.Handle(ctx, f.cmd)
	if err == nil {
		t.Fatal("expected the coordinator to surface the unrecoverable refuel failure, got nil error")
	}

	var refuelErr *ship.ErrRefuelUnrecoverable
	if !errors.As(err, &refuelErr) {
		t.Fatalf("expected the propagated error to unwrap to *ship.ErrRefuelUnrecoverable, got: %v", err)
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

// Regression: an ordinary worker failure (not an unrecoverable refuel) must
// keep logging through the original generic "Worker failed" ERROR path -
// this fix narrows to ErrRefuelUnrecoverable only, it does not touch any
// other failure's observability.
func TestExecuteLevelParallel_OrdinaryWorkerFailure_KeepsGenericErrorLog(t *testing.T) {
	f := newFactoryFixture(t)
	f.mediator.navigateRouteErr = errors.New("boom: unrelated navigation failure")

	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	_, err := f.handler.Handle(ctx, f.cmd)
	if err == nil {
		t.Fatal("expected the coordinator to surface the ordinary worker failure, got nil error")
	}

	var refuelErr *ship.ErrRefuelUnrecoverable
	if errors.As(err, &refuelErr) {
		t.Fatalf("did not expect an ordinary error to unwrap to *ship.ErrRefuelUnrecoverable, got: %v", err)
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
