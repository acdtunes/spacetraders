package parkedsensing

import "testing"

// The chart queue is open for exactly the systems seedlessTargets reads as
// charting work: uncharted waypoints, or a waypoint list never swept — an
// unswept system reports zero uncharted precisely because nobody has looked, so
// a count-only rule would close the queue on the map's darkest systems.
func TestChartQueueDepth_CountsUnchartedAndUnsweptSystems(t *testing.T) {
	systems := []ExpandSystem{
		{System: "X1-DONE", CatalogKnown: true},
		{System: "X1-DARK", CatalogKnown: true, UnchartedCount: 7},
		{System: "X1-UNSWEPT"},
	}
	if got := ChartQueueDepth(systems); got != 2 {
		t.Fatalf("ChartQueueDepth = %d, want 2: the uncharted system and the unswept one both hold the queue open", got)
	}
}

func TestChartQueueDepth_ZeroOnAChartedThroughMap(t *testing.T) {
	systems := []ExpandSystem{
		{System: "X1-A", CatalogKnown: true},
		{System: "X1-B", CatalogKnown: true, SeedShip: "PROBE-1", SeedState: SeedStateDone},
	}
	if got := ChartQueueDepth(systems); got != 0 {
		t.Fatalf("ChartQueueDepth = %d, want 0: with every catalog swept and nothing uncharted the queue is closed", got)
	}
}
