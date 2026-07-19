package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// The tour as an absorption-ledger writer (reserve at plan-accept, convert
// at sale, release on re-plan/exit) and reader (net outstanding depth into each plan).
// These drive the FULL coordinator against the REAL DB-backed ledger, so the reservation
// lifecycle is proven end-to-end, not against a stubbed port. STRONG is the fixture
// market's activity tier (tourFakeMarketRepo), so a converted shadow decays on a real
// (tagged) half-life.

func writeTourRecoveryArtifact(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "recovery.json")
	body := `{"fit_version":1,"era":"test-era","recovery":{
	  "":{"half_life_minutes":1000.0},
	  "WEAK":{"half_life_minutes":60.0},
	  "STRONG":{"half_life_minutes":120.0}}}`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

func setupTourLedger(t *testing.T) (*persistence.AbsorptionLedgerGORM, *gorm.DB) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	ledger := persistence.NewAbsorptionLedger(db, writeTourRecoveryArtifact(t), persistence.AbsorptionLedgerConfig{}, nil)
	return ledger, db
}

func tourLedgerRows(t *testing.T, db *gorm.DB, containerID string) []persistence.MarketAbsorptionLedgerModel {
	t.Helper()
	var rows []persistence.MarketAbsorptionLedgerModel
	require.NoError(t, db.Where("container_id = ?", containerID).Find(&rows).Error)
	return rows
}

// A simple A→B arb fixture: buy G1 at A, sell it into sink B. tv is large so the
// reservation cap never binds unless a test deliberately pre-fills it.
func arbFixture(tv int) *tourFixture {
	return &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"G1": 200}},
		ask:     map[string]map[string]int{"X1-S1-A": {"G1": 100}, "X1-S1-B": {"G1": 200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G1": tv}, "X1-S1-B": {"G1": tv}},
	}
}

func arbPlan() *routing.TourPlan {
	return &routing.TourPlan{Feasible: true, ProjectedProfit: 4000, Legs: []routing.TourLeg{
		leg("X1-S1-A", "X1-S1", buy("G1", 40, 100)),
		leg("X1-S1-B", "X1-S1", sell("G1", 40, 200)),
	}}
}

// The core writer lifecycle: a plan RESERVES its tranches (buy-side at A, sell-side at B),
// the executed sale CONVERTS the sink's reservation into an EXECUTED recovery shadow, and
// on exit the container's remaining PLANNED rows (the buy-side hold) are RELEASED. The
// single surviving row is the sink shadow — proof all three fired (no shadow without a
// prior reservation to convert; no leaked PLANNED without the release).
func TestTourAbsorption_ReservesConvertsAndReleases(t *testing.T) {
	fx := arbFixture(1000)
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{arbPlan()}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	ledger, db := setupTourLedger(t)
	h.SetAbsorptionLedger(ledger, false, 0)

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1", ModelArtifactPath: writeTourArtifact(t),
	})
	require.NoError(t, err)
	require.True(t, tourResponse(t, resp).Completed)

	rows := tourLedgerRows(t, db, "ctr-1")
	require.Len(t, rows, 1, "only the sink recovery shadow survives; the buy-side hold was released")
	require.Equal(t, "EXECUTED", rows[0].State)
	require.Equal(t, "X1-S1-B", rows[0].Waypoint)
	require.Equal(t, absorption.SideSell, rows[0].Side)
	require.Equal(t, 40, rows[0].Units, "shadow carries the realized sold units")
	require.Equal(t, "STRONG", rows[0].TierAtWrite, "converted with the live re-verify tier")
}

// The netting READ path: outstanding depth another container holds is assembled into the
// tour's plan request so the solver plans around it. The fake planner does not itself net
// — this asserts the coordinator ASSEMBLED and PASSED the ledger's outstanding, which is
// the Go-side contract (the actual availability math is the Python solver's, tested there).
func TestTourAbsorption_NetsOutstandingIntoPlanRequest(t *testing.T) {
	ledger, _ := setupTourLedger(t)
	ctx := context.Background()
	// A rival container holds an in-flight PLANNED reservation on sink B.
	_, ok, err := ledger.Reserve(ctx, 1, "rival", "idle-arb", []absorption.ReserveEntry{{
		Waypoint: "X1-S1-B", Good: "G1", Side: absorption.SideSell,
		Units: 40, CapUnits: 4000, TTL: time.Hour,
	}})
	require.NoError(t, err)
	require.True(t, ok)

	fx := arbFixture(1000)
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{arbPlan()}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	h.SetAbsorptionLedger(ledger, false, 0)

	_, err = h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1", ModelArtifactPath: writeTourArtifact(t),
	})
	require.NoError(t, err)

	require.NotEmpty(t, planner.absorptions, "the coordinator must call the planner at least once")
	var found bool
	for _, a := range planner.absorptions[0] {
		if a.Waypoint == "X1-S1-B" && a.Good == "G1" && a.Side == absorption.SideSell && a.PlannedUnits == 40 {
			found = true
		}
	}
	require.True(t, found, "the rival's outstanding PLANNED depth must be netted into the tour's plan request: %+v", planner.absorptions[0])
}

// A reservation breach is a NORMAL re-plan, not a failure: with sink B's fleet-wide cap
// already saturated by a rival, the tour's Reserve rolls back all-or-nothing and re-plans
// against fresh ledger state. When the (fake) planner keeps returning the same contended
// sink, the bounded retry exhausts and the tour exits infeasible — never a phantom trade,
// never a leaked row.
func TestTourAbsorption_ReserveBreachRePlansThenInfeasible(t *testing.T) {
	ledger, db := setupTourLedger(t)
	ctx := context.Background()
	// Saturate sink B at the fleet-wide A-cap (2 tranches × tv 40 = 80 units).
	_, ok, err := ledger.Reserve(ctx, 1, "rival", "idle-arb", []absorption.ReserveEntry{{
		Waypoint: "X1-S1-B", Good: "G1", Side: absorption.SideSell,
		Units: 80, CapUnits: 80, TTL: time.Hour,
	}})
	require.NoError(t, err)
	require.True(t, ok)

	fx := arbFixture(40) // tv 40 → tour CapUnits = 2*40 = 80, already saturated by the rival
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{arbPlan()}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	h.SetAbsorptionLedger(ledger, false, 0)

	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1", ModelArtifactPath: writeTourArtifact(t),
	})
	require.NoError(t, err)
	r := tourResponse(t, resp)

	require.True(t, r.TourUnavailable, "a persistently-contended sink exits infeasible (fail-open no-op)")
	require.Contains(t, r.TourUnavailableReason, "reserve")
	require.Equal(t, tourReserveMaxRetries+1, planner.calls, "one plan per bounded reserve retry, then give up")
	require.Empty(t, tourLedgerRows(t, db, "ctr-1"), "every breached reservation rolled back; nothing leaked")
	require.False(t, r.Completed)
}

// Restart de-dup: a container re-adopted after a daemon restart still holds its pre-restart
// PLANNED rows (L1 liveness keeps them). The release-before-(re)plan invariant drops those
// stale holds before reserving fresh, so the resumed plan does NOT double-reserve — exactly
// one reservation lifecycle runs, yielding one shadow, not the two a double-convert produces.
// (This same release-before-plan is what a mid-tour re-plan uses to release the prior plan's
// PLANNED rows, so it guards the no-leak-on-replan path too.)
func TestTourAbsorption_RestartDoesNotDoubleReserve(t *testing.T) {
	ledger, db := setupTourLedger(t)
	ctx := context.Background()
	// Simulate a pre-restart in-flight reservation this container still owns.
	_, ok, err := ledger.Reserve(ctx, 1, "ctr-1", "tour", []absorption.ReserveEntry{{
		Waypoint: "X1-S1-B", Good: "G1", Side: absorption.SideSell,
		Units: 40, CapUnits: 4000, TTL: time.Hour,
	}})
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, tourLedgerRows(t, db, "ctr-1"), 1, "the pre-restart PLANNED row exists")

	fx := arbFixture(1000)
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{arbPlan()}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	h.SetAbsorptionLedger(ledger, false, 0)

	_, err = h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1", ModelArtifactPath: writeTourArtifact(t),
	})
	require.NoError(t, err)

	rows := tourLedgerRows(t, db, "ctr-1")
	require.Len(t, rows, 1, "one shadow, not two — the stale row was released before re-reserving")
	require.Equal(t, "EXECUTED", rows[0].State)
	require.Equal(t, 40, rows[0].Units, "a single crush recorded, not a doubled one")
}

// A sink sold across TWO price-tiered tranches (the solver emits them as separate trades)
// converts to ONE shadow carrying the FULL realized crush (80u), not just the first
// tranche (40u). This is the D39 multi-tranche co-dump the ledger exists to shadow — a
// per-tranche convert would under-state exactly the case that matters most.
func TestTourAbsorption_MultiTrancheSinkShadowsFullCrush(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"G1": 1000}},
		ask:     map[string]map[string]int{"X1-S1-A": {"G1": 100}, "X1-S1-B": {"G1": 1000}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G1": 40}, "X1-S1-B": {"G1": 40}},
	}
	// Two buy tranches fill 80u; two price-tiered sell tranches (1000, 900 — both within
	// the 15% live tolerance of the 1000 bid) dump all 80u into sink B across two trades.
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("G1", 40, 100), buy("G1", 40, 110)),
			leg("X1-S1-B", "X1-S1", sell("G1", 40, 1000), sell("G1", 40, 900)),
		},
	}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	ledger, db := setupTourLedger(t)
	h.SetAbsorptionLedger(ledger, false, 0)

	_, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1", ModelArtifactPath: writeTourArtifact(t),
	})
	require.NoError(t, err)

	rows := tourLedgerRows(t, db, "ctr-1")
	require.Len(t, rows, 1)
	require.Equal(t, "EXECUTED", rows[0].State)
	require.Equal(t, 80, rows[0].Units, "the shadow records the FULL 80u crush across both tranches, not just the first 40u")
}

// Q3 (REPORT-ONLY): every accepted plan logs projected_recovery_burden — the sum over its
// SELL sinks of units × the fitted recovery half-life (minutes) of the sink's tier. It is
// the analyst's experiment-bar metric; it must be greppable and structured, and it must
// NOT steer selection (the plan flies unchanged). The fixture sinks at STRONG (120 min),
// selling 40 units → 40 × 120 = 4800 unit-minutes; the buy leg is excluded (recovery is a
// sink-crush externality only).
func TestTourAbsorption_LogsRecoveryBurden_ReportOnly(t *testing.T) {
	fx := arbFixture(1000)
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{arbPlan()}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	// The recovery half-lives come from the artifact the handler is configured with; this
	// one carries a `recovery` section (STRONG = 120 min).
	artifact := writeTourRecoveryArtifact(t)
	h.SetModelArtifactPath(artifact)
	ledger, _ := setupTourLedger(t)
	h.SetAbsorptionLedger(ledger, false, 0)

	logger := &laneLogCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)
	_, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1", ModelArtifactPath: artifact,
	})
	require.NoError(t, err)

	var burden *laneLogEntry
	for i := range logger.entries {
		if strings.HasPrefix(logger.entries[i].message, "Tour projected_recovery_burden") {
			burden = &logger.entries[i]
			break
		}
	}
	require.NotNil(t, burden, "an accepted plan must log projected_recovery_burden: %+v", logger.entries)
	require.Contains(t, burden.message, "report-only", "the metric must announce itself as non-steering")
	require.Equal(t, 4800.0, burden.metadata["projected_recovery_burden"], "40 units × STRONG 120-min half-life")
}
