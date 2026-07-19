package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// The planned-manifest log line splits projected profit into fresh-trade
// profit and held-cargo liquidation revenue so a laden-hull plan's margin is
// not misread as pure fresh-trade profit. The TOTAL still ranks selection
// (unchanged); this pins that the SPLIT is greppable in both the message TEXT
// (`container logs` drops metadata) and the structured payload.
// Fresh = total - liquidation.
func TestTour_PlannedLog_CarriesProfitSplit(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{"MEDICINE": 40}, location: "X1-S1-A", cargoCap: 80,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"MEDICINE": 1800}},
		ask:     map[string]map[string]int{"X1-S1-A": {"MEDICINE": 999}, "X1-S1-B": {"MEDICINE": 999}},
		tv:      map[string]map[string]int{"X1-S1-A": {"MEDICINE": 1000}, "X1-S1-B": {"MEDICINE": 1000}},
	}
	// A plan whose total (80000) is fresh-trade profit (8000) plus held-cargo
	// liquidation revenue (72000). The log reads these plan fields directly, so the
	// synthetic split is what the line must report.
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true, ProjectedProfit: 80000, HeldLiquidation: 72000,
		Legs: []routing.TourLeg{leg("X1-S1-B", "X1-S1", sell("MEDICINE", 40, 1800))},
	}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	logger := &laneLogCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	if _, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-LOG", PlayerID: 1, ContainerID: "ctr-log", ModelArtifactPath: writeTourArtifact(t),
	}); err != nil {
		t.Fatalf("tour returned error: %v", err)
	}

	var planned *laneLogEntry
	for i := range logger.entries {
		if strings.HasPrefix(logger.entries[i].message, "Tour planned") {
			planned = &logger.entries[i]
			break
		}
	}
	if planned == nil {
		t.Fatalf("expected a 'Tour planned' log entry, got %+v", logger.entries)
	}

	// The split is greppable in the MESSAGE TEXT (metadata is dropped by `container logs`).
	for _, want := range []string{"projected profit 80000", "fresh 8000", "liquidation 72000"} {
		if !strings.Contains(planned.message, want) {
			t.Fatalf("expected planned-manifest message to contain %q, got: %s", want, planned.message)
		}
	}

	// And carried in the structured payload for dashboards.
	if planned.metadata["projected_profit"] != int64(80000) {
		t.Fatalf("projected_profit metadata = %v (%T), want int64(80000)", planned.metadata["projected_profit"], planned.metadata["projected_profit"])
	}
	if planned.metadata["projected_fresh_profit"] != int64(8000) {
		t.Fatalf("projected_fresh_profit metadata = %v (%T), want int64(8000)", planned.metadata["projected_fresh_profit"], planned.metadata["projected_fresh_profit"])
	}
	if planned.metadata["projected_held_liquidation"] != int64(72000) {
		t.Fatalf("projected_held_liquidation metadata = %v (%T), want int64(72000)", planned.metadata["projected_held_liquidation"], planned.metadata["projected_held_liquidation"])
	}
}
