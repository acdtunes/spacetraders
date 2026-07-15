package autooutfit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainOutfit "github.com/andrescamacho/spacetraders-go/internal/domain/outfitting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// ---- fakes (all at port boundaries) ----------------------------------------

type fakeTelemetry struct {
	legs []trading.TourLegTelemetry
	err  error
}

func (f *fakeTelemetry) ListByPlayer(_ context.Context, _ int, _ time.Time) ([]trading.TourLegTelemetry, error) {
	return f.legs, f.err
}

type fakeFleet struct {
	ships []*navigation.Ship
	err   error
}

func (f *fakeFleet) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return f.ships, f.err
}

type fakeCatalog struct {
	offers   []domainOutfit.ModuleOffer
	readable bool
	err      error
}

func (f *fakeCatalog) ReadCatalog(_ context.Context, _ int, _, _ []string) ([]domainOutfit.ModuleOffer, bool, error) {
	return f.offers, f.readable, f.err
}

type fakeTreasury struct {
	credits int
	err     error
}

func (f *fakeTreasury) LiveCredits(_ context.Context, _ shared.PlayerID) (int, error) {
	return f.credits, f.err
}

type installCall struct {
	shipSymbol, moduleSymbol, waypoint string
}

type spyOutfitter struct {
	calls []installCall
	err   error
}

func (s *spyOutfitter) AcquireAndInstall(_ context.Context, _ int, shipSymbol, moduleSymbol, waypoint string) (int, error) {
	s.calls = append(s.calls, installCall{shipSymbol, moduleSymbol, waypoint})
	return 160, s.err
}

type watchlistCall struct {
	moduleSymbol, waypoint string
}

type fakeWatchlist struct {
	calls     []watchlistCall
	announced bool
}

func (f *fakeWatchlist) AnnounceInReach(_ context.Context, _ int, moduleSymbol, waypoint string, _ int) (bool, error) {
	f.calls = append(f.calls, watchlistCall{moduleSymbol, waypoint})
	return f.announced, nil
}

type fakeNewHullCost struct {
	costPerUnit float64
	readable    bool
}

func (f *fakeNewHullCost) CostPerUnitCapacity(_ context.Context, _ int) (float64, bool, error) {
	return f.costPerUnit, f.readable, nil
}

// ---- helpers ---------------------------------------------------------------

// hauler builds a HAULER hull with the given cargo capacity and a free module slot,
// loaded at X1-TORWIND-A1 so the catalog's TORWIND market is in-system.
func hauler(t *testing.T, symbol string, cargoCapacity, moduleSlots int) *navigation.Ship {
	t.Helper()
	loc, err := shared.NewWaypoint("X1-TORWIND-A1", 0, 0)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(400, 400)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(cargoCapacity, 0, nil)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), loc, fuel, 400, cargoCapacity, cargo, 30, "FRAME_LIGHT_FREIGHTER", "HAULER", nil, navigation.NavStatusInOrbit)
	require.NoError(t, err)
	ship.SetSlots(moduleSlots, 0)
	return ship
}

func buyLeg(ship string, realized int) trading.TourLegTelemetry {
	return trading.TourLegTelemetry{
		ShipSymbol: ship, Good: "IRON", IsBuy: true, RealizedUnits: realized,
		PlannedAt: time.Now().Add(-time.Hour), PlayerID: 1,
	}
}

func cargoOffer() domainOutfit.ModuleOffer {
	return domainOutfit.ModuleOffer{
		Symbol: "MODULE_CARGO_HOLD_II", Class: domainOutfit.ModuleClassCargo,
		Price: 50000, CapacityGained: 80, Waypoint: "X1-TORWIND-MKT", System: "X1-TORWIND", ReachHops: 0,
	}
}

// legsFor emits `count` identical buy legs for a hull, so a hull clears the
// min_telemetry_samples floor.
func legsFor(ship string, realized, count int) []trading.TourLegTelemetry {
	out := make([]trading.TourLegTelemetry, count)
	for i := range out {
		out[i] = buyLeg(ship, realized)
	}
	return out
}

type harness struct {
	handler   *RunAutoOutfitCoordinatorHandler
	cmd       *RunAutoOutfitCoordinatorCommand
	telemetry *fakeTelemetry
	fleet     *fakeFleet
	catalog   *fakeCatalog
	treasury  *fakeTreasury
	outfitter *spyOutfitter
	watchlist *fakeWatchlist
	newHull   *fakeNewHullCost
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		telemetry: &fakeTelemetry{},
		fleet:     &fakeFleet{},
		catalog:   &fakeCatalog{readable: true},
		treasury:  &fakeTreasury{credits: 10_000_000},
		outfitter: &spyOutfitter{},
		watchlist: &fakeWatchlist{announced: true},
		newHull:   &fakeNewHullCost{readable: false},
	}
	handler := NewRunAutoOutfitCoordinatorHandler(h.telemetry, h.fleet, h.catalog, nil)
	handler.SetTreasuryReader(h.treasury)
	handler.SetOutfitter(h.outfitter)
	handler.SetWatchlistNotifier(h.watchlist)
	handler.SetNewHullCostReader(h.newHull)
	h.handler = handler
	h.cmd = &RunAutoOutfitCoordinatorCommand{
		PlayerID:            shared.MustNewPlayerID(1),
		ContainerID:         "auto-outfit-1",
		MinTelemetrySamples: 8,
		PriceCeiling:        500_000,
		MaxInstallsPerTick:  1,
		PaybackHorizonHours: 0,
		TreasuryReserve:     50_000,
		MaxTreasuryFractionPct: 25,
		InstallFeeEstimate:  1000,
		WantedModules:       []string{"MODULE_CARGO_HOLD_II", "MODULE_CARGO_HOLD_III", "MODULE_FUEL_TANK"},
	}
	return h
}

// ---- tests -----------------------------------------------------------------

// Headline acceptance: the coordinator READS tour_leg_telemetry saturation, folds it
// per hull (realized/capacity), and INSTALLS the cargo module on the SATURATED hull
// via the outfitter port — TORWIND-16 loads 76/80 (0.95), TORWIND-7 loads 40/80 (0.5).
// The busiest-but-empty hull is never touched. End-to-end proof through the driving port.
func TestReconcileOnce_ReadsTelemetrySaturation_InstallsOnSaturatedHull(t *testing.T) {
	h := newHarness(t)
	h.fleet.ships = []*navigation.Ship{
		hauler(t, "TORWIND-7", 80, 3),
		hauler(t, "TORWIND-16", 80, 3),
	}
	legs := append(legsFor("TORWIND-7", 40, 10), legsFor("TORWIND-16", 76, 10)...)
	h.telemetry.legs = legs
	h.catalog.offers = []domainOutfit.ModuleOffer{cargoOffer()}

	err := h.handler.ReconcileOnce(context.Background(), h.cmd)
	require.NoError(t, err)

	require.Len(t, h.outfitter.calls, 1, "exactly one upgrade installed this tick")
	got := h.outfitter.calls[0]
	require.Equal(t, "TORWIND-16", got.shipSymbol, "must upgrade the SATURATED hull, not the busiest-but-empty TORWIND-7")
	require.Equal(t, "MODULE_CARGO_HOLD_II", got.moduleSymbol)
	require.Equal(t, "X1-TORWIND-MKT", got.waypoint, "install sources from the module's market")
}

// The guard stack fails CLOSED: any unreadable input or a breached spend ceiling
// installs NOTHING. Each case drives a full tick and asserts the outfitter was never
// called.
func TestReconcileOnce_GuardsFailClosed(t *testing.T) {
	saturatedFleet := func(t *testing.T) []*navigation.Ship {
		return []*navigation.Ship{hauler(t, "TORWIND-16", 80, 3)}
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, h *harness)
	}{
		{"telemetry unreadable → no install", func(t *testing.T, h *harness) {
			h.telemetry.err = errors.New("db down")
		}},
		{"catalog unreadable → no install", func(t *testing.T, h *harness) {
			h.catalog.readable = false
		}},
		{"treasury unreadable → no install", func(t *testing.T, h *harness) {
			h.treasury.err = errors.New("agent read failed")
		}},
		{"over price ceiling → no install", func(t *testing.T, h *harness) {
			h.cmd.PriceCeiling = 40_000 // module is 50000
		}},
		{"over treasury fraction → no install", func(t *testing.T, h *harness) {
			h.treasury.credits = 100_000 // 50000*100 > 100000*25
		}},
		{"below reserve floor → no install", func(t *testing.T, h *harness) {
			h.treasury.credits = 60_000 // 60000 - 50000 - 1000 < 50000 reserve
		}},
		{"no outfitter wired → no install (and no panic)", func(t *testing.T, h *harness) {
			h.handler.SetOutfitter(nil)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.fleet.ships = saturatedFleet(t)
			h.telemetry.legs = legsFor("TORWIND-16", 76, 10)
			h.catalog.offers = []domainOutfit.ModuleOffer{cargoOffer()}
			tc.setup(t, h)

			err := h.handler.ReconcileOnce(context.Background(), h.cmd)
			require.NoError(t, err, "a fail-closed guard is a no-op, not a hard error")
			require.Empty(t, h.outfitter.calls, "guard must install nothing")
		})
	}
}

// The watchlist emits a captain-facing announcement when a wanted module first enters
// reach. DryRun still announces (news is observe-safe) but installs nothing.
func TestReconcileOnce_AnnouncesWatchlistOnFirstAppearance(t *testing.T) {
	h := newHarness(t)
	h.cmd.DryRun = true
	h.fleet.ships = []*navigation.Ship{hauler(t, "TORWIND-16", 80, 3)}
	h.telemetry.legs = legsFor("TORWIND-16", 76, 10)
	h.catalog.offers = []domainOutfit.ModuleOffer{cargoOffer()}

	err := h.handler.ReconcileOnce(context.Background(), h.cmd)
	require.NoError(t, err)

	require.Len(t, h.watchlist.calls, 1, "the wanted module in reach is announced")
	require.Equal(t, "MODULE_CARGO_HOLD_II", h.watchlist.calls[0].moduleSymbol)
	require.Empty(t, h.outfitter.calls, "dry-run installs nothing")
}
