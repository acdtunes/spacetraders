package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// C1 (sp-64je) — the stock-withdrawal executor fails SAFE: when the storage
// subsystem is not fully wired (or no co-located warehouse holds the good), an
// is_stock leg degrades to (false, nil) so the tour re-plans and falls back to a
// market buy — it never crashes and never mis-executes as a market purchase. The
// happy-path reserve->align-transfer->confirm protocol is identical to the
// deployed contract inventory withdrawal (trySourceFromInventory).

func TestTour_StockWithdrawal_DegradesSafelyWhenUnwired(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-W", cargoCap: 80,
		markets: map[string][]string{"X1-S1": {"X1-S1-W"}},
	}
	// newTourHandler wires apiClient=nil and leaves the storage subsystem unwired.
	h := newTourHandler(t, fx, &tourFakeRoutingClient{}, &tourFakeTelemetry{})

	trade := routing.TourTrade{Good: "CLOTHING", Units: 40, ExpectedUnitPrice: 500, IsBuy: true, IsStock: true}
	resp := &RunTourCoordinatorResponse{}
	netBought := map[string]int{}

	ok, err := h.executeStockWithdrawal(context.Background(),
		&RunTourCoordinatorCommand{ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1"},
		leg("X1-S1-W", "X1-S1"), 0, trade, resp, netBought)

	if ok || err != nil {
		t.Fatalf("an unwired stock withdrawal must degrade to (false, nil), got ok=%v err=%v", ok, err)
	}
	if netBought["CLOTHING"] != 0 || resp.TradesExecuted != 0 {
		t.Fatalf("no units may be acquired when the subsystem is unwired: netBought=%v trades=%d", netBought, resp.TradesExecuted)
	}
}
