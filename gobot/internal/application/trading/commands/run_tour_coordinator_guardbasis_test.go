package commands

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// --- The execution money guards band the UNDISCOUNTED plan basis -----------------
//
// BuildTourSnapshot marks a stale ask UP and a stale bid DOWN so the solver — which has no
// age model — ranks an old quote at a haircut. That haircut is a RANKING device, and for
// four days it also reached the executor: the buy ceiling and sell floor are banded around
// the plan's projected price, so a marked-up ask raised the ask a hull would tolerate and a
// marked-down bid lowered the bid it would accept. Both directions loosen a money guard,
// which RULINGS #4 forbids outright.
//
// These pin the fix end to end, through the real BuildTourSnapshot → solve → annotate →
// execute path rather than a hand-set field: the same plan against the same live prices
// must behave identically when the quote is fresh (nothing is charged, so nothing moves)
// and must refuse strictly more once the quote is old enough to be charged.

const (
	gbGood   = "IRON_ORE"
	gbSystem = "X1-GB11"
	gbSource = "X1-GB11-SRC"
	gbSink   = "X1-GB11-SNK"
	// A STRONG board at this age is charged most of the fitted curve without reaching the
	// backstop cap, so the row is priced rather than dropped.
	gbAge = 6 * time.Hour
	// gbPlanned is the plan's projected ask; gbLiveAsk sits inside the tolerated band over
	// it (so today's guard buys) and outside the band over the undiscounted basis.
	gbPlanned = 107
	gbLiveAsk = 118
)

// gbBuyFixture quotes gbLiveAsk at the source. aged ages ONLY the source market, so the
// two runs differ in exactly one input: whether the source quote carries a haircut.
func gbBuyFixture(aged bool) *tourFixture {
	fx := &tourFixture{
		cargo: map[string]int{}, location: gbSource, cargoCap: 100,
		markets: map[string][]string{gbSystem: {gbSource, gbSink}},
		bid:     map[string]map[string]int{gbSink: {gbGood: 400}},
		ask:     map[string]map[string]int{gbSource: {gbGood: gbLiveAsk}, gbSink: {gbGood: 500}},
		tv:      map[string]map[string]int{gbSource: {gbGood: 1000}, gbSink: {gbGood: 1000}},
	}
	if aged {
		fx.ageByWaypoint = map[string]time.Duration{gbSource: gbAge}
	}
	return fx
}

func gbBuyPlanner() *tourFakeRoutingClient {
	return &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true, Legs: []routing.TourLeg{
			leg(gbSource, gbSystem, buy(gbGood, 40, gbPlanned)),
			leg(gbSink, gbSystem, sell(gbGood, 40, 400)),
		},
	}}}
}

func gbRun(t *testing.T, fx *tourFixture, ship string) *laneLogCapturingLogger {
	t.Helper()
	h := newTourHandler(t, fx, gbBuyPlanner(), &tourFakeTelemetry{})
	logger := &laneLogCapturingLogger{}
	if _, err := h.Handle(common.WithLogger(context.Background(), logger), &RunTourCoordinatorCommand{
		ShipSymbol: ship, PlayerID: 1, ContainerID: "ctr-" + ship,
		ModelArtifactPath: writeTourArtifact(t),
	}); err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	return logger
}

// gbSawPriceGate reports whether the leg's live-vs-plan gate actually ran and refused, so a
// zero-buy run cannot pass for the wrong reason (the leg never being flown at all).
func gbSawPriceGate(l *laneLogCapturingLogger) bool {
	for i := range l.entries {
		if _, ok := l.entries[i].metadata["degradation_pct"]; ok {
			return true
		}
	}
	return false
}

// A quote too fresh to be charged leaves every bound exactly where it is: an ask 10.3% over
// the plan is inside the tolerated band and still buys. This is the no-op half of the fix —
// whatever the guards do today when the discount contributes nothing, they must still do.
func TestTourGuardBasis_FreshQuoteLeavesTheBuyGuardsUnchanged(t *testing.T) {
	fx := gbBuyFixture(false)

	gbRun(t, fx, "TORWIND-GB-FRESH")

	if fx.buys != 1 {
		t.Fatalf("an ask of %d against a planned %d is inside the band and must still buy, got %d buys",
			gbLiveAsk, gbPlanned, fx.buys)
	}
}

// The same plan, the same live ask, against a source quote old enough to be charged: the
// plan's projection is inflated by the haircut, so banding around it would tolerate an ask
// the undiscounted basis refuses. It must refuse.
func TestTourGuardBasis_AgedQuoteNoLongerBuysPermissionToOverpay(t *testing.T) {
	fx := gbBuyFixture(true)

	logger := gbRun(t, fx, "TORWIND-GB-AGED")

	if fx.buys != 0 {
		t.Fatalf("a %dh-old STRONG quote must not widen the tolerated ask: %d buys at a live ask of %d",
			int(gbAge.Hours()), fx.buys, gbLiveAsk)
	}
	if !gbSawPriceGate(logger) {
		t.Fatalf("the refusal must come from the live-vs-plan gate, not from a leg that never flew: %+v", logger.entries)
	}
}

// The sell-side mirror. The armed floor is banded UNDER the plan basis, so a marked-DOWN
// bid drops it; measured from the undiscounted basis it can only rise. Both runs sell every
// tranche — the assertion is about where the floor sat, not about a refusal.
//
// The sink is declared an IMPORT because the snapshot zeroes an EXPORT's bid outright: an
// exporter is never a real sink, so a sell leg into one has no quote to reprice and none of
// this arithmetic applies to it.
func TestTourGuardBasis_AgedSinkRaisesTheArmedSellFloor(t *testing.T) {
	sinkImports := map[string]map[string]string{sfSink: {sfGood: "IMPORT"}}

	fresh := sfFixture(nil, sfSink)
	fresh.tradeType = sinkImports
	sfRun(t, fresh, sfPlanner(3, sfSink))

	aged := sfFixture(nil, sfSink)
	aged.tradeType = sinkImports
	aged.ageByWaypoint = map[string]time.Duration{sfSink: gbAge}
	sfRun(t, aged, sfPlanner(3, sfSink))

	freshFloors, agedFloors := sfFloors(fresh), sfFloors(aged)
	if len(freshFloors) < 3 || len(agedFloors) < 3 {
		t.Fatalf("both runs must dispatch the three planned tranches; fresh=%v aged=%v", freshFloors, agedFloors)
	}
	for _, floors := range [][]int{freshFloors, agedFloors} {
		if floors[0] != 0 || floors[1] != 0 {
			t.Fatalf("the depth bound must still hold the first two tranches unarmed, got %v", floors)
		}
	}
	if freshFloors[2] != sfFloor {
		t.Fatalf("an uncharged quote must leave the floor exactly where it is: want %d, got %d", sfFloor, freshFloors[2])
	}
	if agedFloors[2] <= sfFloor {
		t.Fatalf("a %dh-old STRONG sink must RAISE the armed floor above %d, got %d",
			int(gbAge.Hours()), sfFloor, agedFloors[2])
	}
	if fresh.sells != aged.sells {
		t.Fatalf("neither run refuses a tranche here, so both must sell the same; fresh=%d aged=%d",
			fresh.sells, aged.sells)
	}
}
