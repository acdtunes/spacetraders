package navigation

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

func buildSingleSegmentRoute(t *testing.T) *Route {
	t.Helper()
	from, err := shared.NewWaypoint("X1-A1", 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint failed: %v", err)
	}
	to, err := shared.NewWaypoint("X1-B2", 10, 0)
	if err != nil {
		t.Fatalf("NewWaypoint failed: %v", err)
	}
	segment := NewRouteSegment(from, to, 10, 5, 60, shared.FlightModeCruise, false)
	route, err := NewRoute("route-1", "SHIP-1", 1, []*RouteSegment{segment}, 100, false)
	if err != nil {
		t.Fatalf("NewRoute failed: %v", err)
	}
	return route
}

func TestFailRouteSurfacesErrorWhenRouteAlreadyCompleted(t *testing.T) {
	route := buildSingleSegmentRoute(t)
	if err := route.StartExecution(); err != nil {
		t.Fatalf("StartExecution failed: %v", err)
	}
	if err := route.CompleteSegment(); err != nil {
		t.Fatalf("CompleteSegment failed: %v", err)
	}
	if route.Status() != RouteStatusCompleted {
		t.Fatalf("expected COMPLETED status, got %s", route.Status())
	}

	err := route.FailRoute("late failure")

	if err == nil {
		t.Fatal("expected error when failing a completed route, got nil")
	}
	if route.Status() != RouteStatusCompleted {
		t.Fatalf("expected route to remain COMPLETED, got %s", route.Status())
	}
}

func TestFailRouteMarksExecutingRouteFailed(t *testing.T) {
	route := buildSingleSegmentRoute(t)
	if err := route.StartExecution(); err != nil {
		t.Fatalf("StartExecution failed: %v", err)
	}

	err := route.FailRoute("engine trouble")

	if err != nil {
		t.Fatalf("expected no error failing an executing route, got %v", err)
	}
	if route.Status() != RouteStatusFailed {
		t.Fatalf("expected FAILED status, got %s", route.Status())
	}
	if route.LastError() == nil {
		t.Fatal("expected LastError to be recorded")
	}
}
