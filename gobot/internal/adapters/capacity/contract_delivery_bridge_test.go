package capacity

// Behavioral tests for the contract-delivery demand bridge (st-x00, re-scoped
// by st-5le): the adapter that turns the capacity reconciler's emitted capital
// demand into a sp-1txd ClassDemandProvider read. Every test drives the two
// ports the bridge satisfies — CapitalDemandSink (write, reconciler side) and
// ClassDemandProvider (read, autosizer side) — and asserts the observable
// ClassDemand.
//
// Test budget (this file): 3 behaviors — (1) emitted gap → shortfall with the
// DISTINCT contract-delivery class + ROI evidence (ownership split), (2)
// never-emitted → fail closed, (3) readable zero-gap → no shortfall.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	domcap "github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
)

// B1 + B5: the emitted capital gap surfaces straight through as unmet demand on
// the DISTINCT contract-delivery class (never sp-1txd's production warehouse
// class), carrying the reconciler's ROI projection as the guard evidence.
func TestContractDeliveryBridge_SurfacesEmittedCapitalGapAsShortfall(t *testing.T) {
	b := NewContractDeliveryDemandBridge()
	b.EmitCapitalDemand(domcap.CapitalDemand{
		Hulls: 4, WarehouseHulls: 2, StockerHulls: 1, DeliveryHulls: 1,
		MarginalProjectedCrHr: 820000, FleetPerHullCrHr: 700000, RateReadable: true, Present: true,
	})

	d, err := b.Demand(context.Background(), 1, fleetCmd.DemandParams{})
	require.NoError(t, err)

	// The pool is the st-7zk contract-delivery class — NOT sp-1txd's production
	// warehouse class (the ownership split: demands ADD across providers, they
	// do not collide on one shared class).
	require.Equal(t, HullClassContractDelivery, d.Class)
	require.NotEqual(t, fleetCmd.HullClassWarehouse, d.Class)
	require.Equal(t, fleetCmd.HullClass("contract_delivery"), d.Class)

	require.True(t, d.Readable)
	require.Equal(t, 4, d.Shortfall())
	// ROI evidence for sp-1txd's era-payback + realized-rate guards.
	require.Equal(t, 820000.0, d.MarginalRate)
	require.Equal(t, 700000.0, d.FleetAvgRate)
	require.True(t, d.RateReadable)
}

// B5 (fail-closed): before the reconciler has emitted anything (unarmed, or
// pre-first-tick) the bridge reads UNREADABLE — sp-1txd never buys on a demand
// it has never been told (the autosizer's own fail-closed contract).
func TestContractDeliveryBridge_NoDemandEmittedYet_FailsClosed(t *testing.T) {
	b := NewContractDeliveryDemandBridge()

	d, err := b.Demand(context.Background(), 1, fleetCmd.DemandParams{})
	require.NoError(t, err)

	require.False(t, d.Readable, "an unarmed/pre-first-tick reconciler publishes no demand — fail closed, never buy")
	require.Equal(t, 0, d.Shortfall())
	require.Equal(t, HullClassContractDelivery, d.Class)
}

// B2: a reconciler that ran and found NO capital gap publishes a readable zero —
// distinct from the never-emitted case, and correctly yields no shortfall.
func TestContractDeliveryBridge_ReadableZeroGap_NoShortfall(t *testing.T) {
	b := NewContractDeliveryDemandBridge()
	b.EmitCapitalDemand(domcap.CapitalDemand{Hulls: 0, Present: true})

	d, err := b.Demand(context.Background(), 1, fleetCmd.DemandParams{})
	require.NoError(t, err)

	require.True(t, d.Readable, "a reconciler that ran and found no gap is READABLE (distinct from unarmed)")
	require.Equal(t, 0, d.Shortfall())
}
