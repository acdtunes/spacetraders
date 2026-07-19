package navigation

import "testing"

// TestRouteStatusProjectsLifecycleState pins the lifecycle-state -> RouteStatus
// projection of Status(). Route uses domain-specific strings
// (PLANNED/EXECUTING/ABORTED), so these rows are the most divergent of the
// three aggregates and must stay identical.
//
// The Stopped -> ABORTED row is intentionally absent: Route exposes no stop
// transition and no reconstruct-from-persistence path, so its lifecycle can
// only ever reach PENDING/RUNNING/COMPLETED/FAILED. ABORTED is dead-but-preserved
// (kept verbatim in the production table); the switch default is likewise
// unreachable and covered by the shared primitive's own fallback test.
func TestRouteStatusProjectsLifecycleState(t *testing.T) {
	cases := []struct {
		name  string
		drive func(t *testing.T, r *Route)
		want  RouteStatus
	}{
		{"planned on construction", func(t *testing.T, r *Route) {}, RouteStatusPlanned},
		{"executing after start", func(t *testing.T, r *Route) {
			if err := r.StartExecution(); err != nil {
				t.Fatalf("StartExecution: %v", err)
			}
		}, RouteStatusExecuting},
		{"completed after final segment", func(t *testing.T, r *Route) {
			if err := r.StartExecution(); err != nil {
				t.Fatalf("StartExecution: %v", err)
			}
			if err := r.CompleteSegment(); err != nil {
				t.Fatalf("CompleteSegment: %v", err)
			}
		}, RouteStatusCompleted},
		{"failed after fail", func(t *testing.T, r *Route) {
			if err := r.FailRoute("engine trouble"); err != nil {
				t.Fatalf("FailRoute: %v", err)
			}
		}, RouteStatusFailed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := buildSingleSegmentRoute(t)
			tc.drive(t, r)
			if got := r.Status(); got != tc.want {
				t.Fatalf("Status() = %q, want %q", got, tc.want)
			}
		})
	}
}
