package commands

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
)

// TestPublishYards_TheDarkMarketSurfaceReachesTheGauge pins the WIRING, which is
// the half of an observability change that fails silently.
//
// A surface measured in the drain and never published is worth nothing: the whole
// point of sp-xfdep's gauge is that a fleet spreading one probe per system instead
// of saturating should have been visible, and it was not. A collector added
// without a call site produces exactly the same silence as no collector at all.
func TestPublishYards_TheDarkMarketSurfaceReachesTheGauge(t *testing.T) {
	h := &RunProbeSensingCoordinatorHandler{}
	rec := newFakeRecorder()
	h.SetMetricsRecorder(rec)

	h.publishYards(testPlayerID, heartbeat{buy: parkedsensing.BuyReport{
		DarkMarkets:          135,
		DarkMarketsHeld:      804,
		DarkMarketsUnreached: 12,
		DarkMarketsReadable:  true,
	}})

	for component, want := range map[string]int{
		darkMarketsInReach:   135,
		darkMarketsHeld:      804,
		darkMarketsUnreached: 12,
		darkMarketsReadable:  1,
	} {
		if got, ok := rec.darkMarkets[component]; !ok || got != want {
			t.Fatalf("component %q published %d (present=%v), want %d. published=%v",
				component, got, ok, want, rec.darkMarkets)
		}
	}
}

// TestPublishYards_AnUnreadableSurfacePublishesZeroRatherThanNothing is the other
// half of the every-label-every-tick rule.
//
// A gauge that simply stops reporting leaves its last value standing in Prometheus
// until the series goes stale, so a backlog that drained to nothing reads as
// permanently jammed. `readable` at 0 is the one that matters most: it is what says
// the zeros beside it are a BLIND read rather than a verdict that nothing is out of
// reach, and a missing series cannot say that at all.
func TestPublishYards_AnUnreadableSurfacePublishesZeroRatherThanNothing(t *testing.T) {
	h := &RunProbeSensingCoordinatorHandler{}
	rec := newFakeRecorder()
	h.SetMetricsRecorder(rec)

	h.publishYards(testPlayerID, heartbeat{buy: parkedsensing.BuyReport{}})

	for _, component := range []string{
		darkMarketsInReach, darkMarketsHeld, darkMarketsUnreached, darkMarketsReadable,
	} {
		got, ok := rec.darkMarkets[component]
		if !ok {
			t.Fatalf("component %q was not published at all; every label value is set on every tick. published=%v",
				component, rec.darkMarkets)
		}
		if got != 0 {
			t.Fatalf("component %q published %d, want 0", component, got)
		}
	}
}
