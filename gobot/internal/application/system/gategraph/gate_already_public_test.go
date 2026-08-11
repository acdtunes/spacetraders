package gategraph

// Charting is GLOBAL: a gate another agent charted is charted for us too. The store-miss branch
// keyed idempotence on stored EDGES, so a gate whose edge read keeps failing was re-charted on
// every single arrival — an unbounded run of doomed 4230s. Idempotence keys on charted-ness now.

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

func TestService_ChartPresentGate_SkipsTheChartWhenTheGraphShowsItAlreadyCharted(t *testing.T) {
	api := &perSystemGateAPI{connectionsBySystem: map[string][]string{"X1-DA78": {"X1-GQ22-GATE"}}}
	graph := &fakeGraphProvider{waypointsBySystem: map[string][]*shared.Waypoint{
		"X1-DA78": {jumpGateWaypoint("X1-DA78-GATE", true)},
	}}
	svc := NewService(&perSystemMissStore{}, api, graph, &stubPlayerRepo{token: "tok"})

	if _, err := svc.ChartPresentGate(context.Background(), "X1-DA78", "TORWIND-16", 1); err != nil {
		t.Fatalf("skipping a needless chart must not fail the gate read, got %v", err)
	}

	if len(api.chartCalls) != 0 {
		t.Fatalf("a gate already charted by ANY agent must not be re-charted, got %d chart call(s)", len(api.chartCalls))
	}
}

// The skip must not swallow the frontier self-heal: a genuinely uncharted gate is still charted,
// which is what makes it GetJumpGate-readable forever without a hull present.
func TestService_ChartPresentGate_StillChartsAGateTheGraphShowsUncharted(t *testing.T) {
	api := &perSystemGateAPI{connectionsBySystem: map[string][]string{"X1-DA78": {"X1-GQ22-GATE"}}}
	graph := &fakeGraphProvider{waypointsBySystem: map[string][]*shared.Waypoint{
		"X1-DA78": {jumpGateWaypoint("X1-DA78-GATE", false)},
	}}
	svc := NewService(&perSystemMissStore{}, api, graph, &stubPlayerRepo{token: "tok"})

	if _, err := svc.ChartPresentGate(context.Background(), "X1-DA78", "TORWIND-16", 1); err != nil {
		t.Fatalf("an uncharted frontier gate must still read, got %v", err)
	}

	if len(api.chartCalls) != 1 {
		t.Fatalf("an uncharted gate must still be charted exactly once, got %d chart call(s)", len(api.chartCalls))
	}
}
