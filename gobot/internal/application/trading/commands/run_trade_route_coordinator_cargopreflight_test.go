package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Pre-flight cargo check: a hull with no free hold parks BEFORE flying, with a
// structured reason (good/needed/free) carried in the log MESSAGE TEXT, instead of
// burning starvation circuits on a non-empty hull or buying a useless sliver
// mid-buy — do not burn a multi-circuit starvation cycle on a non-empty hull.

// fillHold fills the hull to capacity so AvailableCargoSpace()==0, modelling an
// idle-but-not-empty hull (a benched factory hauler, a pool hull with residual cargo).
func fillHold(t *testing.T, ship interface {
	CargoCapacity() int
	SetCargo(*shared.Cargo)
}) {
	t.Helper()
	cap := ship.CargoCapacity()
	item, err := shared.NewCargoItem("RESIDUAL", "Residual Cargo", "leftover from a prior task", cap)
	if err != nil {
		t.Fatalf("cargo item: %v", err)
	}
	full, err := shared.NewCargo(cap, cap, []*shared.CargoItem{item})
	if err != nil {
		t.Fatalf("full cargo: %v", err)
	}
	ship.SetCargo(full)
}

func TestTradeRoute_PreflightCargo_FullHullParksWithoutTrading(t *testing.T) {
	ship := newTradeHauler(t, "FULL-HAULER")
	fillHold(t, ship) // AvailableCargoSpace() == 0

	h := newTradeHarness(t, ship)
	logger := &laneLogCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	resp, err := h.handler.Handle(ctx, &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: trSystem,
		PlayerID:     1,
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	r, ok := resp.(*RunTradeRouteCoordinatorResponse)
	if !ok {
		t.Fatalf("unexpected response type %T", resp)
	}

	if !r.CargoBlocked {
		t.Fatalf("expected CargoBlocked=true for a full hull, got %+v", r)
	}
	if r.ExitReason != exitReasonCargoBlocked {
		t.Fatalf("expected exit reason %q, got %q", exitReasonCargoBlocked, r.ExitReason)
	}
	// Parked pre-flight: the run never committed a circuit and never bought anything —
	// no wasted round trip, no sliver buy.
	if r.Circuits != 0 {
		t.Fatalf("expected the run to park pre-flight (0 circuits committed), got %d", r.Circuits)
	}
	if len(h.mediator.purchases) != 0 {
		t.Fatalf("a cargo-blocked hull must not buy anything, got %d purchases", len(h.mediator.purchases))
	}
	if r.CargoBlockReason == "" {
		t.Fatalf("expected a CargoBlockReason prose on the response, got empty")
	}
	// The structured park reason must be greppable in the MESSAGE TEXT (the
	// `container logs` renderer drops the metadata map), naming the free space.
	if !hasLogContaining(logger, "Pre-flight cargo check parked hull", "free=0") {
		t.Fatalf("expected a structured pre-flight cargo park log with free=0 in the text, got %+v", logger.entries)
	}
}

// A hull with free hold is NOT cargo-blocked: the pre-flight gate is specific to the
// no-free-space case and must not park a hull that can actually trade.
func TestTradeRoute_PreflightCargo_EmptyHullNotBlocked(t *testing.T) {
	ship := newTradeHauler(t, "EMPTY-HAULER") // capacity 40, 0 used -> 40 free
	h := newTradeHarness(t, ship)

	resp, err := h.handler.Handle(context.Background(), &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: trSystem,
		PlayerID:     1,
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	r := resp.(*RunTradeRouteCoordinatorResponse)

	if r.CargoBlocked {
		t.Fatalf("an empty hull must not be cargo-blocked, got reason %q", r.CargoBlockReason)
	}
	if len(h.mediator.purchases) == 0 {
		t.Fatalf("an empty hull on a disciplined lane should trade, got 0 purchases")
	}
}
