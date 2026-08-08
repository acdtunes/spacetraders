package grpc

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/hullbuy"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// fleetHullPurchaser is the SHARED buy primitive: the fleet autosizer and the dedicated contract
// scaler both execute their purchases through it. These tests pin the two behaviours a caller
// cannot see and cannot recover from — which hull is sent to do the buying, and which fleet tag the
// bought hull lands with — at the adapter seam, where the policy is actually applied.

// dedicateAtPurchaseBuyer wires the primitive against a fleet and a mediator that answers a batch
// purchase with one hull.
func dedicateAtPurchaseBuyer(fleet []*navigation.Ship) (*fleetHullPurchaser, *fakeReclaimShipRepo, *recordingBuyMediator) {
	repo := &fakeReclaimShipRepo{all: fleet}
	med := &recordingBuyMediator{}
	return &fleetHullPurchaser{med: med, shipRepo: repo}, repo, med
}

// purchasingHullUsed reports the hull the batch purchase was told to send, and whether a purchase
// was dispatched at all.
func purchasingHullUsed(m *recordingBuyMediator) (string, bool) {
	for _, r := range m.sent {
		if buy, ok := r.(*shipyardCmd.BatchPurchaseShipsCommand); ok {
			return buy.PurchasingShipSymbol, true
		}
	}
	return "", false
}

// THE DEDICATE-AT-PURCHASE STAMP, asserted through the primitive rather than through the policy
// function alone: this is where the tag reaches the ship repository, and a hull that lands untagged
// when it should be tagged is poached by the next coordinator tick before it ever reaches its role.
//
// The LIGHT row is the load-bearing one and is deliberately not "the case with no assertion": it
// requires ZERO AssignFleet calls. A light hauler is a HAULER worker on arrival, and the depot
// grower's re-dedication (and the factory chain's adoption) is what a tag would break.
func TestAutosizerPurchaser_StampsTheDedicateAtPurchaseTagPerClass(t *testing.T) {
	cases := []struct {
		name          string
		class         hullbuy.HullClass
		wantFleet     string
		wantDedicated bool
	}{
		{"heavy lands in the trade fleet", hullbuy.HullClassHeavy, "trade", true},
		{"explorer lands in the explorer fleet", hullbuy.HullClassExplorer, "explorer", true},
		{"contract delivery lands EXCLUSIVE to the contract fleet", hullbuy.HullClassContractDelivery, "contract", true},
		{"light lands UNTAGGED so the grower re-dedicates it", hullbuy.HullClassLight, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, repo, _ := dedicateAtPurchaseBuyer([]*navigation.Ship{
				reclaimHull(t, "BUYER", 40, navigation.PurchasingFleet, navigation.NavStatusInOrbit),
			})

			res, err := p.BuyAndDedicate(context.Background(), hullbuy.BuyOrder{
				PlayerID: 1, Class: tc.class, ShipType: "SHIP_LIGHT_HAULER", Yard: "X1-HQ-YARD",
			})
			require.NoError(t, err)
			require.Equal(t, "TORWIND-99", res.ShipSymbol)
			require.Equal(t, tc.wantDedicated, res.Dedicated)

			if tc.wantFleet == "" {
				require.Empty(t, repo.assigned, "a light must be stamped with NOTHING — any tag blocks the grower/factory adoption it is bought for")
				return
			}
			require.Equal(t, []assignFleetCall{{symbol: "TORWIND-99", fleet: tc.wantFleet}}, repo.assigned,
				"the bought hull must be stamped for its fleet in the same breath as the buy")
		})
	}
}

// A failed stamp is an ERROR, not a quietly untagged hull: the hull exists and is unclaimed, so the
// caller must learn that the exclusivity it paid for was not applied.
func TestAutosizerPurchaser_DedicationFailureSurfaces(t *testing.T) {
	repo := &fakeReclaimShipRepo{
		all:       []*navigation.Ship{reclaimHull(t, "BUYER", 40, navigation.PurchasingFleet, navigation.NavStatusInOrbit)},
		assignErr: errors.New("db down"),
	}
	p := &fleetHullPurchaser{med: &recordingBuyMediator{}, shipRepo: repo}

	_, err := p.BuyAndDedicate(context.Background(), hullbuy.BuyOrder{PlayerID: 1, Class: hullbuy.HullClassHeavy})
	require.ErrorContains(t, err, "failed to dedicate")
}

// THE PURCHASER-HULL PREFERENCE. The pivoted command frigate is the deterministic, PROTECTED buy
// ship, so it wins over any other idle hull — the plain idle hull is listed FIRST here precisely so
// a first-match-wins implementation fails this test.
func TestAutosizerPurchaser_PrefersTheProtectedPurchasingHull(t *testing.T) {
	p, _, med := dedicateAtPurchaseBuyer([]*navigation.Ship{
		reclaimHull(t, "IDLE-OTHER", 40, "", navigation.NavStatusInOrbit),
		reclaimHull(t, "BUYER", 40, navigation.PurchasingFleet, navigation.NavStatusInOrbit),
	})

	_, err := p.BuyAndDedicate(context.Background(), hullbuy.BuyOrder{PlayerID: 1, Class: hullbuy.HullClassHeavy})
	require.NoError(t, err)

	used, dispatched := purchasingHullUsed(med)
	require.True(t, dispatched)
	require.Equal(t, "BUYER", used, "the protected purchasing hull must be preferred whenever it is idle")
}

// The preference is a PREFERENCE, not a requirement: a momentarily busy or not-yet-established
// purchasing hull must not stall scaling, so any idle hull can execute the buy.
func TestAutosizerPurchaser_FallsBackToAnyIdleHull(t *testing.T) {
	busyBuyer := reclaimHull(t, "BUYER", 40, navigation.PurchasingFleet, navigation.NavStatusInOrbit)
	require.NoError(t, busyBuyer.AssignToContainer("worker-1", shared.NewRealClock()))

	cases := []struct {
		name  string
		fleet []*navigation.Ship
	}{
		{"purchasing hull mid-task", []*navigation.Ship{busyBuyer, reclaimHull(t, "IDLE-OTHER", 40, "", navigation.NavStatusInOrbit)}},
		{"no purchasing hull established yet", []*navigation.Ship{reclaimHull(t, "IDLE-OTHER", 40, "", navigation.NavStatusInOrbit)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, _, med := dedicateAtPurchaseBuyer(tc.fleet)

			_, err := p.BuyAndDedicate(context.Background(), hullbuy.BuyOrder{PlayerID: 1, Class: hullbuy.HullClassHeavy})
			require.NoError(t, err)

			used, dispatched := purchasingHullUsed(med)
			require.True(t, dispatched)
			require.Equal(t, "IDLE-OTHER", used)
		})
	}
}

// No idle hull ⇒ NO purchase is dispatched at all. The buy needs a hull to travel to the yard, and
// spending first and discovering that second would leave a paid-for hull nobody was sent to collect.
func TestAutosizerPurchaser_NoIdleHullBuysNothing(t *testing.T) {
	busy := reclaimHull(t, "BUYER", 40, navigation.PurchasingFleet, navigation.NavStatusInOrbit)
	require.NoError(t, busy.AssignToContainer("worker-1", shared.NewRealClock()))
	p, _, med := dedicateAtPurchaseBuyer([]*navigation.Ship{busy})

	_, err := p.BuyAndDedicate(context.Background(), hullbuy.BuyOrder{PlayerID: 1, Class: hullbuy.HullClassHeavy})
	require.ErrorContains(t, err, "no idle hull")
	require.False(t, med.purchaseAttempted(), "a buy with no hull to execute it must never reach the money path")
}
