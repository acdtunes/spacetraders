package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// The tour coordinator's two peer scans read "is this a trade hull?" off the whole fleet. Both
// must see all three dedication tags: a legacy hull blind to a migrated peer co-locates on top
// of it (the anti-herd cap it was meant to respect), and a system holding only migrated hulls
// reports no hull to price a candidate tour for.

var tourTradeFamilyTags = []string{navigation.TradeFleet, navigation.TradeFleetMVT, navigation.TradeFleetLane}

func TestTradeHullsBySystem_TalliesEveryTradeFamilyTag(t *testing.T) {
	for _, tc := range []struct {
		fleet string
		want  int
	}{
		{navigation.TradeFleet, 1},
		{navigation.TradeFleetMVT, 1},
		{navigation.TradeFleetLane, 1},
		{"contract", 0},
		{"", 0},
	} {
		t.Run("fleet="+tc.fleet, func(t *testing.T) {
			fx := &tourFixture{cargo: map[string]int{}, location: "X1-ORIG-A", cargoCap: 100,
				activeHulls: []activeHull{{system: "X1-HERD", fleet: tc.fleet}}}
			h := newTourHandler(t, fx, &tourFakeRoutingClient{}, &tourFakeTelemetry{})

			counts, ok := h.activeTradeHullsBySystem(context.Background(), 1)
			require.True(t, ok)
			require.Equal(t, tc.want, counts["X1-HERD"])
		})
	}
}

func TestRelocationOriginHull_ResolvesAnyTradeFamilyTag(t *testing.T) {
	for _, tag := range tourTradeFamilyTags {
		t.Run(tag, func(t *testing.T) {
			fx := relocRegionFixture()
			fx.activeHulls = []activeHull{{system: "X1-ORIG", fleet: tag}}
			h := relocRegionHandler(t, fx, nil)

			ship, err := h.relocationOriginHull(context.Background(), 1, "X1-ORIG")
			require.NoError(t, err, "a migrated hull carries the same class facts the tour is priced on")
			require.NotNil(t, ship)
		})
	}

	// Still a class projection off the TRADE fleet: a contract hull is not one.
	fx := relocRegionFixture()
	fx.activeHulls = []activeHull{{system: "X1-ORIG", fleet: "contract"}}
	_, err := relocRegionHandler(t, fx, nil).relocationOriginHull(context.Background(), 1, "X1-ORIG")
	require.Error(t, err)
}
