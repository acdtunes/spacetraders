package commands

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// backstopFixture builds a one-market tour world whose single good carries the given
// activity level and cached-read age, so a test can drive one row through the whole
// snapshot → constraints → planner path and read what the solver would receive.
func backstopFixture(activity string, age time.Duration) *tourFixture {
	return &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets:            map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:                map[string]map[string]int{"X1-S1-B": {"G": 200}},
		ask:                map[string]map[string]int{"X1-S1-A": {"G": 100}, "X1-S1-B": {"G": 200}},
		tv:                 map[string]map[string]int{"X1-S1-A": {"G": 1000}, "X1-S1-B": {"G": 1000}},
		activityByWaypoint: map[string]string{"X1-S1-A": activity, "X1-S1-B": activity},
		ageByWaypoint:      map[string]time.Duration{"X1-S1-A": age, "X1-S1-B": age},
	}
}

// planStalenessInputs runs one tour through the coordinator's command entry point against an
// infeasible plan (no legs execute, so the assertions read PLANNING inputs only) and returns
// the snapshot plus the staleness backstop the coordinator handed the planner.
func planStalenessInputs(t *testing.T, fx *tourFixture, caps trading.RankerAgeCaps) ([]routing.TourGoodSnapshot, int) {
	t.Helper()
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{Feasible: false, InfeasibleReason: "planning-only"}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	h.SetRankerAgeCaps(caps)
	if _, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1", ModelArtifactPath: writeTourArtifact(t),
	}); err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	if len(planner.snapshots) != 1 {
		t.Fatalf("expected exactly one plan call, got %d", len(planner.snapshots))
	}
	return planner.snapshots[0], planner.snapshotAgeCaps[0]
}

// A WEAK row the Go-side cap DELIBERATELY preserves must reach the solver intact: present in
// the snapshot AND inside the staleness backstop the same request carries. A backstop tighter
// than the row's age would re-drop inside the solver what the Go side kept.
func TestTour_WeakRowPastTheFlatCutoffSurvivesIntoTheSolverRequest(t *testing.T) {
	const age = 120 * time.Minute
	snapshot, backstopMinutes := planStalenessInputs(t, backstopFixture("WEAK", age), trading.RankerAgeCaps{})

	if len(snapshot) == 0 {
		t.Fatalf("expected the WEAK rows to survive the per-activity snapshot filter, got none")
	}
	if backstopMinutes < int(age.Minutes()) {
		t.Fatalf("solver staleness backstop = %d min, but the snapshot carries rows aged %d min: "+
			"the solver would silently re-drop what the activity cap kept",
			backstopMinutes, int(age.Minutes()))
	}
}

// The upstream per-activity filter is the real guard and must keep binding: a row past its
// activity's backstop stays dropped before the solver ever sees it. Pricing staleness does
// not mean ranking a quote of any age.
func TestTour_RowPastItsBackstopIsStillDroppedBeforeTheSolver(t *testing.T) {
	caps := trading.DefaultRankerAgeCaps()
	age := caps.For("STRONG") + time.Hour
	snapshot, _ := planStalenessInputs(t, backstopFixture("STRONG", age), trading.RankerAgeCaps{})

	if len(snapshot) != 0 {
		t.Fatalf("expected the STRONG rows to be dropped past their %v backstop, got %d rows: %+v",
			caps.For("STRONG"), len(snapshot), snapshot)
	}
}

// The backstop is DERIVED from the one cap table, so a retune moves it with no code change —
// including a retune that makes an activity other than WEAK the widest.
func TestTour_SolverBackstopIsTheWidestCapInTheTable(t *testing.T) {
	cases := []struct {
		name string
		caps trading.RankerAgeCaps
		want int
	}{
		{
			name: "unset table falls to the fitted armed defaults",
			caps: trading.RankerAgeCaps{},
			want: int(trading.DefaultRankerAgeCapWeak.Minutes()),
		},
		{
			name: "retuned table",
			caps: trading.RankerAgeCaps{Weak: 90 * time.Minute, Restricted: 45 * time.Minute, Growing: 20 * time.Minute, Strong: 10 * time.Minute},
			want: 90,
		},
		{
			name: "retuned so RESTRICTED is the widest",
			caps: trading.RankerAgeCaps{Weak: 30 * time.Minute, Restricted: 200 * time.Minute, Growing: 20 * time.Minute, Strong: 10 * time.Minute},
			want: 200,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, backstopMinutes := planStalenessInputs(t, backstopFixture("WEAK", time.Minute), tc.caps)
			if backstopMinutes != tc.want {
				t.Fatalf("solver staleness backstop = %d min, want %d", backstopMinutes, tc.want)
			}
		})
	}
}
