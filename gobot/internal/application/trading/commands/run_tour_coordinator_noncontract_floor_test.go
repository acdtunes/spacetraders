package commands

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// sp-q8bon (Admiral 2026-07-24: "We can't go down 150K! Or else we risk contracts
// stalling"): gate-fill margin-blind buys dragged the treasury 638k→142k, and the contract
// engine — the sole earner — parked its next 106,400 source-buy against the 50k floor: a
// full-economy deadlock. The fix raises the NON-CONTRACT reserve DEFAULT to
// common.NonContractWorkingCapitalFloor (150k) so the 50k–150k working-capital band is
// contract-exclusive. This suite pins the TRADE side of that default: a tour whose launch
// config carried NO reserve now floors every buy at 150k, while an EXPLICITLY configured
// reserve — even one below 150k — still wins verbatim (only the absent-config default rose).

// A tour with NO WorkingCapitalReserve (launch config carried none) must resolve the
// default to the 150k non-contract floor: with the live balance at 100,000 — inside the
// contract-exclusive 50k–150k band — every headroom is negative, so the planned buy is
// SKIPPED entirely. Under the old 50k default this exact run bought 50 units (50,000
// headroom): the delta this test exists to pin.
func TestTour_DefaultReserve_ParksBuysInContractBand(t *testing.T) {
	fx := floorRoundTripFixture()
	api := &tourSeqAPIClient{balances: []int{100_000}} // inside the 50k–150k contract band
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		floorRoundTripPlan(100),
		{Feasible: false, InfeasibleReason: "no_profitable_tour"},
	}}
	h := newTourHandlerWithAPI(t, fx, planner, &tourFakeTelemetry{}, api)

	logger := &laneLogCapturingLogger{}
	ctx := common.WithLogger(auth.WithPlayerToken(context.Background(), "TOUR-BAND"), logger)
	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-BAND", PlayerID: 1, ContainerID: "ctr-band",
		MaxSpend: 10_000_000, // fixed cap → the only GetAgent read is the buy-time floor read
		// WorkingCapitalReserve deliberately unset (0) → the non-contract default.
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("a floor-skipped buy must not error: %v", err)
	}
	r := tourResponse(t, resp)

	if fx.buys != 0 {
		t.Fatalf("a default-reserve tour at a 100,000 balance sits inside the contract-exclusive 50k–150k band and must dispatch ZERO buys, got %d — a buy here recreates the sp-q8bon contract-stall deadlock", fx.buys)
	}
	if r.TotalSpent != 0 {
		t.Fatalf("a fully-skipped buy spends nothing, got %d", r.TotalSpent)
	}

	// The default-resolution WARNING (RULINGS #4: never resolve a reserve silently) must
	// now name the 150k non-contract default in its MESSAGE text.
	want := fmt.Sprintf("resolved to the %d default", common.NonContractWorkingCapitalFloor)
	found := false
	for i := range logger.entries {
		if strings.Contains(logger.entries[i].message, want) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected the reserve-resolution WARNING to name the %d non-contract default in its message text, got entries: %+v", common.NonContractWorkingCapitalFloor, logger.entries)
	}
}

// An EXPLICITLY configured reserve still wins verbatim — even one BELOW the 150k default.
// A 60,000 launch-config reserve with a 100,000 live balance leaves 40,000 of headroom, so
// the planned 100-unit buy is SHRUNK to 40 units and executed. Guards against the wrong
// implementation max(explicit, default), which would skip this buy; only the absent-config
// DEFAULT rose to 150k (sp-q8bon preserves the explicit-reserve semantics).
func TestTour_ExplicitReserveBelowDefault_StillWins(t *testing.T) {
	fx := floorRoundTripFixture()
	api := &tourSeqAPIClient{balances: []int{100_000}}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{floorRoundTripPlan(100)}}
	h := newTourHandlerWithAPI(t, fx, planner, &tourFakeTelemetry{}, api)

	ctx := auth.WithPlayerToken(context.Background(), "TOUR-EXPLICIT")
	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-EXPLICIT", PlayerID: 1, ContainerID: "ctr-explicit",
		MaxSpend: 10_000_000, WorkingCapitalReserve: 60_000, // explicit, below the 150k default
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("an explicit-reserve tour must not error: %v", err)
	}
	r := tourResponse(t, resp)

	if fx.buys != 1 {
		t.Fatalf("an explicit 60,000 reserve must win over the 150k default (one shrunk buy), got %d buys — zero means the default incorrectly overrode the configured reserve", fx.buys)
	}
	if r.TotalSpent != 40*1000 {
		t.Fatalf("expected the tranche shrunk to the explicit reserve's 40,000 headroom (40 units), got spend %d", r.TotalSpent)
	}
}
