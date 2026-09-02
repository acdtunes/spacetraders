package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// The migration tags ("trade-mvt", "trade-lane") name hulls of the SAME trade fleet, so every
// reader that asks "is this a trade hull?" must see all three. A reader still testing
// == "trade" makes a migrated fleet read as an EMPTY trade fleet — which is how the bootstrap
// trade-seed re-triggers on a fleet it already seeded, and how fleet-growth under-counts the
// hold it is sizing against. The one deliberate exception is pinned at the bottom.

var tradeFamilyTags = []string{navigation.TradeFleet, navigation.TradeFleetMVT, navigation.TradeFleetLane}

func TestObserveFleetShape_CountsEveryTradeFamilyTag(t *testing.T) {
	for _, tag := range tradeFamilyTags {
		t.Run(tag, func(t *testing.T) {
			ships := []*navigation.Ship{
				homeReaderShip(t, "TORWIND-1", "X1-HQ-A1", commandRole, tag),
				homeReaderShip(t, "TRADE-2", "X1-HQ-B2", "HAULER", tag),
			}
			obs := bootstrapCmd.Observation{}
			observeFleetShape(ships, &obs)

			require.Equal(t, 1, obs.TradeHullCount, "a migrated hauler is still a trade hull")
			require.True(t, obs.CommandFrigateOnTrade, "a migrated frigate is still on the trade fleet")
		})
	}

	// A foreign dedication is still foreign — the widening is family-scoped, not a wildcard.
	obs := bootstrapCmd.Observation{}
	observeFleetShape([]*navigation.Ship{homeReaderShip(t, "CONTRACT-1", "X1-HQ-B2", "HAULER", contractFleetTag)}, &obs)
	require.Zero(t, obs.TradeHullCount)
}

// growthCountShipRepo serves a fixed fleet to countShips.
type growthCountShipRepo struct {
	navigation.ShipRepository
	ships []*navigation.Ship
}

func (r *growthCountShipRepo) FindAllByPlayer(context.Context, shared.PlayerID) ([]*navigation.Ship, error) {
	return r.ships, nil
}

func TestGrowthTradeHullCounter_CountsEveryTradeFamilyTag(t *testing.T) {
	for _, tag := range tradeFamilyTags {
		t.Run(tag, func(t *testing.T) {
			repo := &growthCountShipRepo{ships: []*navigation.Ship{
				homeReaderShip(t, "TRADE-1", "X1-HQ-B2", "HAULER", tag),
				homeReaderShip(t, "CONTRACT-1", "X1-HQ-B2", "HAULER", contractFleetTag),
			}}
			n, err := (&growthTradeHullCounter{shipRepo: repo}).TradeHulls(context.Background(), 1)
			require.NoError(t, err)
			require.Equal(t, 1, n, "the hold-fill multiplier sizes against the whole trade pool")
		})
	}
}

// The relocator is the OLD path's placement engine and stays DELIBERATELY narrow: a "trade-mvt"
// hull places itself through the MVT loop's CLAIM/TRAVEL and a "trade-lane" specialist is pinned
// to its fat lane, so offering either a relocation puts two engines on one hull.
func TestRelocatorFleetObserver_OffersRelocationToLegacyTradeHullsOnly(t *testing.T) {
	for _, tc := range []struct {
		tag  string
		want bool
	}{
		{navigation.TradeFleet, true},
		{navigation.TradeFleetMVT, false},
		{navigation.TradeFleetLane, false},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			hull := relocatorPortsHull(t, "TRADE-1", "HAULER")
			hull.SetDedicatedFleet(tc.tag)
			observer := NewRelocatorFleetObserver(
				&relocatorPortsShipRepo{ships: []*navigation.Ship{hull}},
				&relocatorPortsContainerRepo{},
			)

			hulls, err := observer.ObserveTradeHulls(context.Background(), 1)
			require.NoError(t, err)
			require.Equal(t, tc.want, len(hulls) == 1)

			_, err = observer.ObserveHull(context.Background(), 1, "TRADE-1")
			if tc.want {
				require.NoError(t, err)
			} else {
				require.Error(t, err, "the actuation re-check must refuse a hull the old path does not own")
			}
		})
	}
}
