package expansion

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/probebuy"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-1bme8 — the probe-buy relay must be the SINGLE WRITER of its buyer hull for the whole
// source→yard reposition (RULINGS #3). Two coordinators (frontier expansion + market-freshness
// sizer) independently pick idle hulls as probe buyers off the SAME ProbePurchaser; with no
// exclusive claim, both grab the same idle hull, and the second actor moving it mid-journey
// desyncs the no-reload multi-hop jump into a physically-impossible self-jump. These tests pin the
// claim lifecycle at the ProbePurchaser port: acquire before the relay, release on EVERY exit,
// exclude an already-claimed hull, and fail CLOSED (no drive, no spend) when the claim race is lost.

// The buyer is CLAIMED before it is relayed/bought and RELEASED after the buy — a single-writer
// journey claim owned by the driving coordinator's container id.
func TestBuyProbe_ClaimsBuyerBeforeRelay_AndReleasesAfterBuy(t *testing.T) {
	var events []string
	med := &probeFakeMediator{
		listings: map[string]int{
			"X1-HOME-YD": 25_000, // where the idle hull sits
			"X1-NEAR-YD": 30_000, // the demand-proximal yard, re-priced at the dock after the relay
		},
		boughtSymbol: "PROBE-NEW",
		boughtPrice:  30_000,
		events:       &events,
	}
	ships := &probeFakeShipRepo{idle: []*navigation.Ship{probeShip(t, "BUYER-1", "X1-HOME-YD")}, events: &events}
	finder := &probeFakeYardFinder{candidates: []shipyardQueries.YardCandidate{
		yard("X1-NEAR-YD", "X1-NEAR", 1, 30_000),
	}}
	p := NewProbePurchaser(med, ships, finder, &probeFakeLedger{}, nil)

	target := probebuy.ProbeTarget{System: "X1-NEAR", HopPenaltyCredits: probebuy.DefaultHopPenaltyCredits, ClaimOwnerContainerID: "frontier-XYZ"}
	_, _, err := p.BuyProbe(context.Background(), shared.MustNewPlayerID(1), 50_000, target)
	require.NoError(t, err)

	// Ordering IS the guarantee: the exclusive claim is held for the whole reposition — taken
	// BEFORE the relay and the buy, and only released AFTER the buy lands. The owner is the
	// driving coordinator's container id.
	require.Equal(t, []string{
		"claim:BUYER-1:frontier-XYZ",
		"navigate:BUYER-1",
		"buy:BUYER-1",
		"release:BUYER-1",
	}, events)
	require.NotContains(t, ships.claims, "BUYER-1", "the journey claim is not left dangling after a successful buy")
}

// Even when the relay fails closed, the claim is RELEASED — a failed/crashed relay must never
// strand the buyer permanently out of the idle pool.
func TestBuyProbe_ReleasesClaim_WhenRelayFails(t *testing.T) {
	var events []string
	med := &probeFakeMediator{
		listings: map[string]int{"X1-HOME-YD": 25_000},
		navErr:   errors.New("no jump gate connection — unroutable relay"),
		events:   &events,
	}
	ships := &probeFakeShipRepo{idle: []*navigation.Ship{probeShip(t, "BUYER-1", "X1-HOME-YD")}, events: &events}
	finder := &probeFakeYardFinder{candidates: []shipyardQueries.YardCandidate{
		yard("X1-NEAR-YD", "X1-NEAR", 1, 30_000),
	}}
	p := NewProbePurchaser(med, ships, finder, &probeFakeLedger{}, nil)

	target := probebuy.ProbeTarget{System: "X1-NEAR", HopPenaltyCredits: probebuy.DefaultHopPenaltyCredits, ClaimOwnerContainerID: "frontier-XYZ"}
	_, _, err := p.BuyProbe(context.Background(), shared.MustNewPlayerID(1), 50_000, target)

	require.Error(t, err, "an unroutable relay fails the buy closed")
	require.Empty(t, med.purchases, "RULINGS #4: no spend on a fail-closed relay")
	require.NotContains(t, ships.claims, "BUYER-1", "the claim is released on the failure path — the buyer is never stranded")
	require.Equal(t, []string{"claim:BUYER-1:frontier-XYZ", "release:BUYER-1"}, events)
}

// A hull already carrying another relay's live journey claim is EXCLUDED from a second
// coordinator's buyer selection: coordinator B fails closed and NEVER drives the hull relay A holds
// (acceptance #1 — two coordinators never concurrently drive one buyer).
func TestBuyProbe_ExcludesBuyerAlreadyClaimedByAnotherRelay(t *testing.T) {
	med := &probeFakeMediator{listings: map[string]int{"X1-HOME-YD": 25_000}}
	ships := &probeFakeShipRepo{
		idle:   []*navigation.Ship{probeShip(t, "BUYER-1", "X1-HOME-YD")},
		claims: map[string]string{"BUYER-1": "frontier-AAA"}, // relay A already holds this buyer
	}
	finder := &probeFakeYardFinder{candidates: []shipyardQueries.YardCandidate{
		yard("X1-NEAR-YD", "X1-NEAR", 1, 30_000),
	}}
	p := NewProbePurchaser(med, ships, finder, &probeFakeLedger{}, nil)

	target := probebuy.ProbeTarget{System: "X1-NEAR", HopPenaltyCredits: probebuy.DefaultHopPenaltyCredits, ClaimOwnerContainerID: "freshness-BBB"}
	_, _, err := p.BuyProbe(context.Background(), shared.MustNewPlayerID(1), 50_000, target)

	require.Error(t, err, "the only idle hull is claimed by another relay → no buyer available, fail closed")
	require.Empty(t, med.navigations, "coordinator B never relays a hull relay A already holds")
	require.Empty(t, med.purchases)
	require.Equal(t, "frontier-AAA", ships.claims["BUYER-1"], "B never steals relay A's claim")
}

// When the atomic claim race is LOST (the buyer got claimed by the other coordinator between
// selection and the claim), the buy fails CLOSED — no relay is issued and no probe is bought, so
// two coordinators can never concurrently drive one hull and no bad spend escapes (RULINGS #4).
func TestBuyProbe_FailsClosed_WhenClaimRaceLost(t *testing.T) {
	var events []string
	med := &probeFakeMediator{
		listings:     map[string]int{"X1-HOME-YD": 25_000, "X1-NEAR-YD": 30_000},
		boughtSymbol: "PROBE-NEW",
		boughtPrice:  30_000,
		events:       &events,
	}
	ships := &probeFakeShipRepo{
		idle:     []*navigation.Ship{probeShip(t, "BUYER-1", "X1-HOME-YD")},
		claimErr: shared.NewShipAlreadyAssignedError("BUYER-1", "frontier-AAA"), // lost the row-locked race
		events:   &events,
	}
	finder := &probeFakeYardFinder{candidates: []shipyardQueries.YardCandidate{
		yard("X1-NEAR-YD", "X1-NEAR", 1, 30_000),
	}}
	p := NewProbePurchaser(med, ships, finder, &probeFakeLedger{}, nil)

	target := probebuy.ProbeTarget{System: "X1-NEAR", HopPenaltyCredits: probebuy.DefaultHopPenaltyCredits, ClaimOwnerContainerID: "freshness-BBB"}
	_, _, err := p.BuyProbe(context.Background(), shared.MustNewPlayerID(1), 50_000, target)

	require.Error(t, err, "a lost claim race fails the buy closed — never a concurrent second driver")
	require.Empty(t, med.navigations, "no relay is issued when the buyer claim was lost")
	require.Empty(t, med.purchases, "RULINGS #4: no spend when the claim is lost")
	require.Empty(t, events, "no journey side effects at all when the claim cannot be taken")
}
