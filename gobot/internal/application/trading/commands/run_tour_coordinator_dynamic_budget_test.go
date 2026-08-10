package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
)

// --max-spend 0 resolves to TRADE'S SHARE OF DEPLOYABLE CAPITAL — the per-operation capital
// budget — and to nothing else. The flat fraction of treasury that used to sit in front of it
// bound BELOW the budget and throttled the engine the budget had already sized.
//
// Measured live 2026-08-10 02:37Z: treasury 350,682 over the 150,000 non-contract floor leaves
// 200,682 deployable, of which trade's 40% is 80,273 — against a flat-fraction cap of 87,670
// that was the binding constraint.
func TestDefaultMaxSpend_ZeroResolvesToTheCapitalBudget(t *testing.T) {
	const (
		liveTreasury   = int64(350_682)
		wantTradeShare = int64(80_273) // 40% of (350,682 - 150,000)
		flatFraction   = int64(87_670) // 25% of 350,682 — the cap that used to bind
	)

	sensor := &tourFakeCapitalWorkSensor{constructionWork: true}
	h := &RunTourCoordinatorHandler{
		apiClient:  &tourSeqAPIClient{balances: []int{int(liveTreasury)}},
		workSensor: sensor,
	}
	ctx := common.WithLogger(auth.WithPlayerToken(context.Background(), "TOUR-ZUZJ9"), &laneLogCapturingLogger{})

	got, unreadable := h.defaultMaxSpend(ctx, 1, common.NonContractWorkingCapitalFloor)
	if unreadable {
		t.Fatal("a readable treasury must not report unreadable")
	}
	if got == flatFraction {
		t.Fatalf("defaultMaxSpend = %d — still a flat %d%% of treasury, not the capital budget", got, 25)
	}
	if got != wantTradeShare {
		t.Fatalf("defaultMaxSpend = %d, want %d — trade's %d%% share of the %d deployable over the %d floor",
			got, wantTradeShare, common.TradeCapitalSharePct,
			common.CapitalDeployable(liveTreasury, common.NonContractWorkingCapitalFloor), int64(common.NonContractWorkingCapitalFloor))
	}
	if sensor.calls == 0 {
		t.Fatal("the construction-side hasWork sensor was never consulted — the capital budget is not in the resolution path")
	}
}

// Graceful degradation survives: with the construction drain idle the whole deployable pool
// trades, so no capital sits idle behind a stopped gate.
func TestDefaultMaxSpend_ConstructionIdleYieldsTheWholeDeployablePool(t *testing.T) {
	h := &RunTourCoordinatorHandler{
		apiClient:  &tourSeqAPIClient{balances: []int{350_682}},
		workSensor: &tourFakeCapitalWorkSensor{constructionWork: false},
	}
	ctx := common.WithLogger(auth.WithPlayerToken(context.Background(), "TOUR-ZUZJ9-IDLE"), &laneLogCapturingLogger{})

	got, unreadable := h.defaultMaxSpend(ctx, 1, common.NonContractWorkingCapitalFloor)
	if unreadable {
		t.Fatal("a readable treasury must not report unreadable")
	}
	if want := int64(200_682); got != want {
		t.Fatalf("defaultMaxSpend = %d, want the whole %d deployable pool (construction idle)", got, want)
	}
}

// The WORKING-CAPITAL RESERVE FLOOR still binds, and binds HARDEST exactly where a flat fraction
// of treasury was loosest: a treasury at the floor deploys nothing, and one just above it
// deploys only what sits above the floor. The resolved cap is a budget derived from the floor,
// so it can never authorise a spend the floor would refuse.
func TestDefaultMaxSpend_ReserveFloorStillBindsAtLowTreasury(t *testing.T) {
	const reserve = int64(common.NonContractWorkingCapitalFloor)

	cases := []struct {
		name     string
		treasury int64
		want     int64
	}{
		// Nothing above the floor is deployable, so the budget is zero and every plan
		// priced under it is refused before a market is read (fail closed).
		{name: "treasury at the floor deploys nothing", treasury: reserve, want: 0},
		// Below the floor is not negative capital — it is still nothing.
		{name: "treasury under the floor deploys nothing", treasury: reserve - 40_000, want: 0},
		// Only the 10,000 above the floor is deployable; trade's share of it is 4,000.
		// A flat 25% of this treasury would have authorised 40,000 — four times the
		// capital that exists above the floor.
		{name: "only the band above the floor is deployable", treasury: reserve + 10_000, want: 4_000},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &RunTourCoordinatorHandler{
				apiClient:  &tourSeqAPIClient{balances: []int{int(tc.treasury)}},
				workSensor: &tourFakeCapitalWorkSensor{constructionWork: true},
			}
			ctx := common.WithLogger(auth.WithPlayerToken(context.Background(), "TOUR-ZUZJ9-LOW"), &laneLogCapturingLogger{})

			got, unreadable := h.defaultMaxSpend(ctx, 1, reserve)
			if unreadable {
				t.Fatal("a readable treasury must not report unreadable")
			}
			if got != tc.want {
				t.Fatalf("defaultMaxSpend(treasury=%d, reserve=%d) = %d, want %d", tc.treasury, reserve, got, tc.want)
			}
			if got > common.CapitalDeployable(tc.treasury, reserve) {
				t.Fatalf("the resolved cap %d exceeds the %d deployable above the reserve floor — the floor was weakened (RULINGS #4)",
					got, common.CapitalDeployable(tc.treasury, reserve))
			}
		})
	}
}

// An empty deployable pool must still be read as a SOLVENCY refusal, so the loop pauses on a
// capital-denied verdict rather than planning a tour no buy can clear.
func TestDefaultMaxSpend_EmptyDeployablePoolDeniesEverySpend(t *testing.T) {
	h := &RunTourCoordinatorHandler{
		apiClient:  &tourSeqAPIClient{balances: []int{common.NonContractWorkingCapitalFloor}},
		workSensor: &tourFakeCapitalWorkSensor{constructionWork: true},
	}
	ctx := common.WithLogger(auth.WithPlayerToken(context.Background(), "TOUR-ZUZJ9-DENY"), &laneLogCapturingLogger{})

	cap, _ := h.defaultMaxSpend(ctx, 1, common.NonContractWorkingCapitalFloor)
	cmd := &RunTourCoordinatorCommand{MaxSpend: 0}
	budget := tourPlanBudget{maxSpend: cap, reserve: common.NonContractWorkingCapitalFloor}
	if !h.budgetDeniesEverySpend(cmd, budget) {
		t.Fatalf("a %d budget over an empty deployable pool must deny every spend (fail closed)", cap)
	}
}

// An EXPLICIT --max-spend is a captain override and is returned untouched: no treasury read, no
// capital budget, no sensor. Only the 0/default path resolves.
func TestResolveTourSpendCap_ExplicitMaxSpendIsUnchanged(t *testing.T) {
	api := &tourSeqAPIClient{balances: []int{350_682}}
	sensor := &tourFakeCapitalWorkSensor{constructionWork: true}
	h := &RunTourCoordinatorHandler{apiClient: api, workSensor: sensor}
	logger := &laneLogCapturingLogger{}
	ctx := common.WithLogger(auth.WithPlayerToken(context.Background(), "TOUR-ZUZJ9-FIXED"), logger)

	cmd := &RunTourCoordinatorCommand{ShipSymbol: "TOUR-FIX", PlayerID: 1, MaxSpend: 500_000}
	got, unresolved, err := h.resolveTourSpendCap(ctx, cmd, &RunTourCoordinatorResponse{},
		health.NewMonitor(health.DefaultStreakThreshold), common.NonContractWorkingCapitalFloor, logger)
	if err != nil || unresolved {
		t.Fatalf("an explicit --max-spend never resolves: err=%v unresolved=%v", err, unresolved)
	}
	if got != 500_000 {
		t.Fatalf("explicit --max-spend resolved to %d, want the captain's constant 500,000", got)
	}
	if api.calls != 0 {
		t.Fatalf("an explicit --max-spend read live treasury %d time(s) — that path has never had a live read", api.calls)
	}
	if sensor.calls != 0 {
		t.Fatalf("an explicit --max-spend consulted the capital-budget sensor %d time(s) — the override must bypass the budget", sensor.calls)
	}
}
