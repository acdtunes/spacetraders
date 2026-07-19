package commands

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// With a treasury-percent stamped (production tours resolve it to 40% by default), the
// tour buy-time floor becomes max(50k, min(reserve, pct% × live treasury)) instead of the
// flat absolute reserve. Below ~2.5M treasury a reserve above the balance no longer
// strands the hull. These tests drive the REAL executeBuy seam through Handle with the
// pct set, exercising the shared common.EffectiveReserveFloor resolver; the absolute-floor
// suites (no pct set) prove the absolute floor is untouched when the counter-cyclical mode
// is off.

// propFloorCapturingLogger records log lines so the counter-cyclical INFO can be asserted.
type propFloorCapturingLogger struct {
	mu      sync.Mutex
	entries []struct{ level, message string }
}

func (l *propFloorCapturingLogger) Log(level, message string, _ map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, struct{ level, message string }{level, message})
}

func (l *propFloorCapturingLogger) infoContains(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.entries {
		if e.level == "INFO" && strings.Contains(e.message, sub) {
			return true
		}
	}
	return false
}

func propFloorCtx(token string, logger *propFloorCapturingLogger) context.Context {
	return common.WithLogger(auth.WithPlayerToken(context.Background(), token), logger)
}

// Proportional binds below 2.5M: at 800k treasury with a 1M reserve and 40%, the floor
// resolves to 320k (allowance 480k), so a buy that the OLD absolute 1M floor would have
// SKIPPED (headroom 800k − 1M = −200k) now PROCEEDS. This is the deadlock fix: the fleet
// can trade at sub-reserve treasury. The counter-cyclical INFO must fire (the watch signal).
func TestTour_ProportionalFloor_ProceedsAtSub2p5MWhereAbsoluteWouldDeadlock(t *testing.T) {
	fx := floorRoundTripFixture()
	api := &tourSeqAPIClient{balances: []int{800_000}} // below the 1M reserve → old floor deadlocks
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{floorRoundTripPlan(100)}}
	h := newTourHandlerWithAPI(t, fx, planner, &tourFakeTelemetry{}, api)

	logger := &propFloorCapturingLogger{}
	resp, err := h.Handle(propFloorCtx("TOUR-PROP-PROCEED", logger), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-PROP-PROCEED", PlayerID: 1, ContainerID: "ctr-prop-proceed",
		MaxSpend: 10_000_000, WorkingCapitalReserve: 1_000_000, WorkingCapitalReserveTreasuryPct: 40,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("a proportional-floor buy must not error: %v", err)
	}
	r := tourResponse(t, resp)

	if fx.buys != 1 {
		t.Fatalf("at 800k treasury the 40%% proportional floor (320k) admits the buy where the absolute 1M floor would skip it — expected 1 buy, got %d", fx.buys)
	}
	if r.TotalSpent != 100*1000 {
		t.Fatalf("the 100-unit buy fits under the 480k allowance and proceeds whole (spend 100,000), got %d", r.TotalSpent)
	}
	if !logger.infoContains("320000") {
		t.Fatalf("the counter-cyclical INFO must report the 320,000 proportional floor engaging — the watch's signal; got none matching")
	}
}

// Proportional-regime shrink: at 150k treasury with a 1M reserve and 40%, the floor is 60k,
// headroom 90k, so a planned 100-unit buy at ask 1000 SHRINKS to the 90 units the allowance
// affords (spend 90,000) — the shrink mechanic, now driven by the proportional floor rather
// than the absolute reserve.
func TestTour_ProportionalFloor_ShrinksToProportionalAllowance(t *testing.T) {
	fx := floorRoundTripFixture()
	api := &tourSeqAPIClient{balances: []int{150_000}}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{floorRoundTripPlan(100)}}
	h := newTourHandlerWithAPI(t, fx, planner, &tourFakeTelemetry{}, api)

	logger := &propFloorCapturingLogger{}
	resp, err := h.Handle(propFloorCtx("TOUR-PROP-SHRINK", logger), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-PROP-SHRINK", PlayerID: 1, ContainerID: "ctr-prop-shrink",
		MaxSpend: 10_000_000, WorkingCapitalReserve: 1_000_000, WorkingCapitalReserveTreasuryPct: 40,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("a proportional-floor shrink must not error: %v", err)
	}
	r := tourResponse(t, resp)

	if fx.buys != 1 {
		t.Fatalf("expected exactly ONE shrunk buy, got %d", fx.buys)
	}
	if r.TotalSpent != 90*1000 {
		t.Fatalf("the 60k proportional floor leaves 90k headroom → 90 units (spend 90,000), got %d", r.TotalSpent)
	}
	if !logger.infoContains("60000") {
		t.Fatalf("the counter-cyclical INFO must report the 60,000 proportional floor; got none matching")
	}
}

// The immutable 50k lower bound binds at very low treasury: at 100k with 40%, the raw
// proportional term is 40k, clamped UP to the non-tunable 50k (RULINGS #5). Headroom is
// 50k → a 100-unit buy shrinks to 50 (spend 50,000). The floor is never weakened below 50k.
func TestTour_ProportionalFloor_Immutable50kBindsAt100k(t *testing.T) {
	fx := floorRoundTripFixture()
	api := &tourSeqAPIClient{balances: []int{100_000}}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{floorRoundTripPlan(100)}}
	h := newTourHandlerWithAPI(t, fx, planner, &tourFakeTelemetry{}, api)

	logger := &propFloorCapturingLogger{}
	resp, err := h.Handle(propFloorCtx("TOUR-PROP-IMM", logger), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-PROP-IMM", PlayerID: 1, ContainerID: "ctr-prop-imm",
		MaxSpend: 10_000_000, WorkingCapitalReserve: 1_000_000, WorkingCapitalReserveTreasuryPct: 40,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("an immutable-floor buy must not error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.TotalSpent != 50*1000 {
		t.Fatalf("the 40%%-of-100k proportional (40k) is clamped UP to the immutable 50k → 50k headroom → 50 units (spend 50,000), got %d", r.TotalSpent)
	}
}

// Above 2.5M the configured absolute binds EXACTLY as the flat absolute reserve would: at
// 3M with a 1M reserve and 40%, the proportional term (1.2M) exceeds the absolute, so min
// picks 1M — the floor is NOT lowered and the counter-cyclical INFO must NOT fire. Guards
// against the proportional path silently altering high-treasury behavior.
func TestTour_ProportionalFloor_AbsoluteBindsAbove2p5M(t *testing.T) {
	fx := floorRoundTripFixture()
	api := &tourSeqAPIClient{balances: []int{3_000_000}}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{floorRoundTripPlan(100)}}
	h := newTourHandlerWithAPI(t, fx, planner, &tourFakeTelemetry{}, api)

	logger := &propFloorCapturingLogger{}
	resp, err := h.Handle(propFloorCtx("TOUR-PROP-ABS", logger), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-PROP-ABS", PlayerID: 1, ContainerID: "ctr-prop-abs",
		MaxSpend: 10_000_000, WorkingCapitalReserve: 1_000_000, WorkingCapitalReserveTreasuryPct: 40,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("an absolute-bind buy must not error: %v", err)
	}
	r := tourResponse(t, resp)

	if fx.buys != 1 || r.TotalSpent != 100*1000 {
		t.Fatalf("at 3M treasury the 1M absolute binds (2M headroom) → the 100-unit buy proceeds whole (spend 100,000), got %d buys / spend %d", fx.buys, r.TotalSpent)
	}
	if logger.infoContains("Counter-cyclical") {
		t.Fatalf("above 2.5M the absolute binds — the counter-cyclical floor must NOT engage, but an INFO fired")
	}
}

// Fail-closed is NOT weakened by the pct (RULINGS #4): an unreadable live balance still
// yields zero spend even with the proportional floor active — the guard never computes a
// lowered floor against a treasury it could not read.
func TestTour_ProportionalFloor_UnreadableStillFailsClosed(t *testing.T) {
	fx := floorRoundTripFixture()
	api := &tourErrAPIClient{}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		floorRoundTripPlan(100),
		{Feasible: false, InfeasibleReason: "no_profitable_tour"},
	}}
	h := newTourHandlerWithAPI(t, fx, planner, &tourFakeTelemetry{}, api)

	logger := &propFloorCapturingLogger{}
	resp, err := h.Handle(propFloorCtx("TOUR-PROP-BLIND", logger), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-PROP-BLIND", PlayerID: 1, ContainerID: "ctr-prop-blind",
		MaxSpend: 10_000_000, WorkingCapitalReserve: 1_000_000, WorkingCapitalReserveTreasuryPct: 40,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("an unreadable balance must fail closed gracefully, not error: %v", err)
	}
	r := tourResponse(t, resp)

	if fx.buys != 0 || r.TotalSpent != 0 {
		t.Fatalf("a blind buy-time floor must dispatch ZERO buys even with the pct set (fail-closed), got %d buys / spend %d", fx.buys, r.TotalSpent)
	}
}
