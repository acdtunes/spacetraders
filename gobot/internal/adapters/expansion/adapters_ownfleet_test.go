package expansion

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/probebuy"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ---- sp-f082y: ownFleet parameterization (the stationed probe-buyer fleet reuse enabler) --------
//
// The whole point of a DEDICATED probe-buyer fleet is that ONE coordinator drives its hulls and
// everyone else skips them. The reused buy/station path (ProbePurchaser / ProbeBuyerPositioner)
// skips ALL dedicated hulls by default (undedicated-only), which is byte-identical for the frontier +
// freshness sizer callers. SetOwnFleet("probe-buyer") opens exactly one more class — hulls dedicated
// to that fleet — as drivable buyers, AND takes the single-writer claim under the fleet operation so
// ClaimShip's dedication guard accepts them. These tests pin both halves: the enablement and the
// byte-identical default, exercised only through the public ports (BuyProbe / PositionProbeBuyer).

// ownFleetShipRepo models the row-locked ClaimShip DEDICATION GUARD (ship_repository.go): a claim is
// rejected when the hull is dedicated to a fleet OTHER than the claim operation. Modeling the guard is
// what makes the claim-operation wiring FALSIFIABLE — had the purchaser claimed a probe-buyer hull
// under the plain "probe_buy" operation, this guard would reject it and the buy would fail closed, so
// the "buy succeeds" assertion below genuinely proves the operation is the hull's own fleet.
type ownFleetShipRepo struct {
	navigation.ShipRepository
	idle     []*navigation.Ship
	claims   map[string]string // shipSymbol -> owning containerID
	claimOps map[string]string // shipSymbol -> operation the claim was taken under (spy)
}

func (r *ownFleetShipRepo) FindIdleByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	out := make([]*navigation.Ship, 0, len(r.idle))
	for _, ship := range r.idle {
		if _, claimed := r.claims[ship.ShipSymbol()]; claimed {
			continue // a claimed hull is non-idle — mirrors the row-locked claim excluding it
		}
		out = append(out, ship)
	}
	return out, nil
}

func (r *ownFleetShipRepo) ClaimShip(_ context.Context, shipSymbol, containerID string, _ shared.PlayerID, operation string) error {
	for _, ship := range r.idle {
		if ship.ShipSymbol() != shipSymbol {
			continue
		}
		if fleet := ship.DedicatedFleet(); fleet != "" && fleet != operation {
			return shared.NewShipDedicatedToOtherFleetError(shipSymbol, fleet, operation)
		}
	}
	if r.claims == nil {
		r.claims = map[string]string{}
		r.claimOps = map[string]string{}
	}
	r.claims[shipSymbol] = containerID
	r.claimOps[shipSymbol] = operation
	return nil
}

func (r *ownFleetShipRepo) ReleaseContainerClaim(_ context.Context, shipSymbol string, _ shared.PlayerID, _ string) (bool, error) {
	_, held := r.claims[shipSymbol]
	delete(r.claims, shipSymbol)
	return held, nil
}

// probeBuyerHull is a probe (satellite) dedicated to a named fleet — a stationed probe-buyer, or a
// contract hauler that must never be poached.
func probeBuyerHull(t *testing.T, symbol, waypoint, fleet string) *navigation.Ship {
	ship := probeShip(t, symbol, waypoint)
	ship.SetDedicatedFleet(fleet)
	return ship
}

// With ownFleet="probe-buyer", the reused in-place buy path selects the DEDICATED probe-buyer hull as
// the buyer and buys through it — even when NO undedicated hull exists (the production catch-22) — and
// claims it under its own fleet so the dedication guard passes. A contract hull present at the same
// yard is never poached (RULINGS #7).
func TestBuyProbe_OwnFleetProbeBuyer_BuysThroughDedicatedStationedHull(t *testing.T) {
	med := &probeFakeMediator{
		listings:     map[string]int{"X1-YD": 25_000},
		boughtSymbol: "PROBE-NEW",
		boughtPrice:  25_000,
	}
	buyer := probeBuyerHull(t, "PB-1", "X1-YD", "probe-buyer")
	contractor := probeBuyerHull(t, "HAUL-9", "X1-YD", "contract") // must never be used (RULINGS #7)
	repo := &ownFleetShipRepo{idle: []*navigation.Ship{contractor, buyer}}
	p := NewProbePurchaser(med, repo, nil, &probeFakeLedger{}, nil).SetOwnFleet("probe-buyer")

	price, symbol, err := p.BuyProbe(context.Background(), shared.MustNewPlayerID(1), 50_000, probebuy.ProbeTarget{})

	require.NoError(t, err)
	require.Equal(t, 25_000, price)
	require.Equal(t, "PROBE-NEW", symbol)
	require.Len(t, med.purchases, 1)
	require.Equal(t, "PB-1", med.purchases[0].PurchasingShipSymbol, "the dedicated probe-buyer hull is the buyer, not the contract hauler")
	require.Equal(t, "X1-YD", med.purchases[0].ShipyardWaypoint)
	require.Equal(t, "probe-buyer", repo.claimOps["PB-1"], "the buyer is claimed under its OWN fleet so ClaimShip's dedication guard passes")
}

// Byte-identical default: with ownFleet unset, a probe-buyer-dedicated hull is NOT a buyer, so a fleet
// of only-dedicated hulls fails the buy closed — exactly as before sp-f082y. This is the "skipped by
// everyone else" half: the frontier + freshness sizer purchasers never touch a probe-buyer hull.
func TestBuyProbe_DefaultOwnFleet_SkipsDedicatedProbeBuyerHull(t *testing.T) {
	med := &probeFakeMediator{listings: map[string]int{"X1-YD": 25_000}}
	buyer := probeBuyerHull(t, "PB-1", "X1-YD", "probe-buyer")
	repo := &ownFleetShipRepo{idle: []*navigation.Ship{buyer}}
	p := NewProbePurchaser(med, repo, nil, &probeFakeLedger{}, nil) // ownFleet unset

	_, _, err := p.BuyProbe(context.Background(), shared.MustNewPlayerID(1), 50_000, probebuy.ProbeTarget{})

	require.Error(t, err, "a probe-buyer hull is invisible to the default (undedicated-only) purchaser")
	require.Empty(t, med.purchases, "no buy through a dedicated hull the default purchaser must skip")
}

// The positioner mirrors the purchaser: ownFleet="probe-buyer" relays a stationed-away probe-buyer
// hull to a reachable yard so its in-place price reads next tick.
func TestPositionProbeBuyer_OwnFleetProbeBuyer_RelaysDedicatedHull(t *testing.T) {
	buyer := probeBuyerHull(t, "PB-1", "X1-HOME-A2", "probe-buyer")
	med := &probeFakeMediator{}
	ships := &probeFakeShipRepo{idle: []*navigation.Ship{buyer}}
	finder := &probeFakeYardFinder{candidates: []shipyardQueries.YardCandidate{yard("X1-HOME-YD", "X1-HOME", 0, 20_000)}}
	p := NewProbeBuyerPositioner(med, ships, finder).SetOwnFleet("probe-buyer")

	dispatched, err := p.PositionProbeBuyer(context.Background(), shared.MustNewPlayerID(1))

	require.NoError(t, err)
	require.True(t, dispatched, "the dedicated probe-buyer hull is relayed to its reachable yard")
	require.Len(t, med.navigations, 1)
	require.Equal(t, "PB-1", med.navigations[0].ShipSymbol)
	require.Equal(t, "X1-HOME-YD", med.navigations[0].Destination)
}

// Byte-identical default: the frontier's positioner (ownFleet unset) never relays a probe-buyer hull.
func TestPositionProbeBuyer_DefaultOwnFleet_SkipsProbeBuyerHull(t *testing.T) {
	buyer := probeBuyerHull(t, "PB-1", "X1-HOME-A2", "probe-buyer")
	med := &probeFakeMediator{}
	ships := &probeFakeShipRepo{idle: []*navigation.Ship{buyer}}
	finder := &probeFakeYardFinder{candidates: []shipyardQueries.YardCandidate{yard("X1-HOME-YD", "X1-HOME", 0, 20_000)}}
	p := NewProbeBuyerPositioner(med, ships, finder) // default ""

	dispatched, err := p.PositionProbeBuyer(context.Background(), shared.MustNewPlayerID(1))

	require.NoError(t, err)
	require.False(t, dispatched, "byte-identical default: a probe-buyer hull is not positioned by the default positioner")
	require.Empty(t, med.navigations)
}
