package ship

import (
	"context"
	"errors"
	"fmt"
	"testing"

	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// mustExecutingRouteWithSegment builds a single-segment route already transitioned
// to EXECUTING, plus the segment, for the reactToSegmentFailure tests.
func mustExecutingRouteWithSegment(t *testing.T) (*domainNavigation.Route, *domainNavigation.RouteSegment) {
	t.Helper()
	from := mustWaypoint(t, "X1-PARK-A", 0, 0)
	to := mustWaypoint(t, "X1-PARK-B", 10, 0)
	seg := domainNavigation.NewRouteSegment(from, to, 10, 5, 60, shared.FlightModeCruise, false)
	route, err := domainNavigation.NewRoute("park-route", "ARRIVAL-WAIT-1", 1, []*domainNavigation.RouteSegment{seg}, 100, false)
	if err != nil {
		t.Fatalf("NewRoute: %v", err)
	}
	if err := route.StartExecution(); err != nil {
		t.Fatalf("StartExecution: %v", err)
	}
	return route, seg
}

// TestReactToSegmentFailure_ArrivalWaitExhausted_ParksNotFailsRoute is sp-arrwait's
// Fix C (recover-not-crash on the route/tour path). When a segment fails with a
// genuine *ErrArrivalWaitExhausted, the route executor must PARK/DEFER — leaving the
// route NOT FAILED and returning the error with its TYPE PRESERVED so callers keep
// the recoverable classification — rather than marking the route FAILED, which
// propagates a hard error that burns the container's restart budget to an
// unrecoverable crash.
func TestReactToSegmentFailure_ArrivalWaitExhausted_ParksNotFailsRoute(t *testing.T) {
	executor := NewRouteExecutor(nil, nil, nil, nil, nil, nil, nil, &fakeArrivalSubscriber{})
	route, seg := mustExecutingRouteWithSegment(t)
	ship := newArrivalWaitTestShip(t, domainNavigation.NavStatusInTransit)

	parkErr := &ErrArrivalWaitExhausted{ShipSymbol: ship.ShipSymbol(), Attempts: 2}
	got := executor.reactToSegmentFailure(context.Background(), route, ship, seg, 0, parkErr)

	var exhausted *ErrArrivalWaitExhausted
	if !errors.As(got, &exhausted) {
		t.Fatalf("expected the arrival-wait error preserved (recoverable classification), got %T: %v", got, got)
	}
	if route.Status() == domainNavigation.RouteStatusFailed {
		t.Fatalf("expected the route PARKED/DEFERRED (not FAILED) on arrival-wait exhaustion, got %s", route.Status())
	}
}

// TestReactToSegmentFailure_GenericError_FailsRoute pins that the recover-not-crash
// path is SCOPED to arrival-wait exhaustion: any other segment error still FAILS the
// route exactly as before (a real failure must not be silently deferred).
func TestReactToSegmentFailure_GenericError_FailsRoute(t *testing.T) {
	executor := NewRouteExecutor(nil, nil, nil, nil, nil, nil, nil, &fakeArrivalSubscriber{})
	route, seg := mustExecutingRouteWithSegment(t)
	ship := newArrivalWaitTestShip(t, domainNavigation.NavStatusInTransit)

	genErr := fmt.Errorf("navigate command failed: API 4203")
	got := executor.reactToSegmentFailure(context.Background(), route, ship, seg, 0, genErr)

	if !errors.Is(got, genErr) {
		t.Fatalf("expected the original error returned unchanged, got %v", got)
	}
	if route.Status() != domainNavigation.RouteStatusFailed {
		t.Fatalf("expected a generic segment error to FAIL the route (behavior unchanged), got %s", route.Status())
	}
}
