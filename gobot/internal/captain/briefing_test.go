package watchkeeper

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"gorm.io/gorm"
)

// seedTx inserts one ledger row for the composer integration tests.
func seedTx(t *testing.T, db *gorm.DB, playerID, i, amount, balanceAfter int, category, opType string, at time.Time) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID:              fmt.Sprintf("tx-%d", i),
		PlayerID:        playerID,
		Timestamp:       at,
		CreatedAt:       at,
		TransactionType: "SELL",
		Category:        category,
		OperationType:   opType,
		Amount:          amount,
		BalanceBefore:   balanceAfter - amount,
		BalanceAfter:    balanceAfter,
	}).Error)
}

func TestBriefingComposeFailsOpenOnEmptyStateAndNoPrometheus(t *testing.T) {
	db, playerID, _ := setupDB(t)
	now := time.Now()

	// Empty ledger + promURL "" (prometheus disabled). Compose must still return
	// a well-formed block, never panic, never error — the fail-open contract.
	out := NewBriefing(db, playerID, "", nil).Compose(context.Background(), now, time.Hour)

	require.Contains(t, out, "WAKE BRIEFING")
	require.Contains(t, out, "TREASURY: n/a") // no transactions -> no live balance
	require.Contains(t, out, "GUARDS: n/a")   // prometheus disabled
	require.Contains(t, out, "API: n/a")
}

func TestBriefingComposeRendersLiveTreasuryTrendAndMix(t *testing.T) {
	db, playerID, _ := setupDB(t)
	now := time.Now()
	// Two ledger rows in the last hour: a tour sale and a ship-investment cost.
	seedTx(t, db, playerID, 1, 50_000, 1_050_000, "TRADING_REVENUE", "tour", now.Add(-30*time.Minute))
	seedTx(t, db, playerID, 2, -8_000, 1_042_000, "SHIP_INVESTMENTS", "", now.Add(-20*time.Minute))

	out := NewBriefing(db, playerID, "", []int{500_000}).Compose(context.Background(), now, 2*time.Hour)

	require.Contains(t, out, "TREASURY: 1.04M") // latest balance_after
	require.Contains(t, out, "1h +42k")         // net last hour = 50k - 8k
	require.Contains(t, out, "tours")           // profit mix carries the tour engine
}

func TestSupervisorComposeBriefingHonorsDisabledKnob(t *testing.T) {
	db, playerID, _ := setupDB(t)
	now := time.Now()

	// Disabled: the knob (live-by-default) suppresses the block entirely.
	off := &Supervisor{db: db, cfg: config.CaptainConfig{PlayerID: playerID, BriefingDisabled: true}}
	require.Empty(t, off.composeBriefing(context.Background(), now))

	// Enabled (default): a block is produced even with an empty ledger (fail-open).
	on := &Supervisor{db: db, cfg: config.CaptainConfig{PlayerID: playerID}}
	require.Contains(t, on.composeBriefing(context.Background(), now), "WAKE BRIEFING")
}

// orderedLabels is the captain-ruled (14dl1) strict ordering of the briefing
// block, money first, programs last. ENGINEERING is omit-if-empty and so is
// asserted separately.
var orderedLabels = []string{
	"TREASURY", "TREND", "RUNWAY", "GUARDS", "POSTURE", "EARNERS",
	"MIX", "BURN", "API", "ALERTS", "COVERAGE", "INVENTORY",
}

// requireOrder asserts every label in want appears in body, in that order.
func requireOrder(t *testing.T, body string, want []string) {
	t.Helper()
	last := -1
	for _, label := range want {
		idx := strings.Index(body, label)
		require.GreaterOrEqualf(t, idx, 0, "label %q missing from briefing:\n%s", label, body)
		require.Greaterf(t, idx, last, "label %q out of order in briefing:\n%s", label, body)
		last = idx
	}
}

func fullBriefingData() briefingData {
	d := func(v int) *int { return &v }
	f := func(v float64) *float64 { return &v }
	return briefingData{
		Treasury:  &treasuryData{Balance: 1_230_000, DeltaSinceWake: d(45_000)},
		Trend:     &trendData{NetLastHour: 12_000, NetPriorHour: 8_000, Net6hAvg: 5_000, ExCapexSlopePerHour: d(2_000)},
		Floors:    []int{500_000, 1_000_000},
		Guards:    &guardsData{CeilingParks: d(0), SupplyParks: d(1), InputPauses: d(2), Kills: 1, KilledChains: []killedChain{{Name: "FABRICS", RatePerHour: d(-12_000)}}},
		Posture:   &postureData{Laden: 4, Idle: 2, Stranded: 1, StrandedDetail: []strandedHull{{Waypoint: "X1-GQ92", Reason: "no_reachable_source"}}},
		Earners:   &earnersData{ActiveTours: 3, HeavySellsPerHour: d(12)},
		ProfitMix: &profitMixData{Shares: []mixShare{{Label: "tours", Pct: 60}, {Label: "contracts", Pct: 20}, {Label: "factory", Pct: 15}, {Label: "arb", Pct: 5}}},
		BurnWatch: &burnWatchData{CostCenter: "SHIP_INVESTMENTS", RatePerHour: -2_200_000, SharePct: 78},
		API:       &apiData{UtilPct: d(45), P95WaitSeconds: f(1.2)},
		Alerts:    &alertsData{Firing: []string{"EarnerDark", "BurstSaturation"}},
		Coverage:  &coverageData{Stale: 3, Total: 20},
		Inventory: &inventoryData{TotalValue: 1_500_000, StoredValue: d(800_000)},
	}
}

func TestBriefingRendersAllLinesInStrictOrderWhenPopulated(t *testing.T) {
	now := time.Date(2026, 7, 11, 18, 0, 0, 0, time.UTC)
	data := fullBriefingData()
	data.Engineering = &engineeringData{DeploysAwaitingAcceptance: 2}

	body := renderBriefing(data, now)

	requireOrder(t, body, append(append([]string{}, orderedLabels...), "ENGINEERING"))
	// A few load-bearing values must actually render (not be swallowed).
	require.Contains(t, body, "EarnerDark")
	require.Contains(t, body, "FABRICS") // guard-kill inline chain name
	require.Contains(t, body, "3/20")    // coverage stale/total
}

func TestBriefingNamesKilledChainsInlineOnlyAtOrBelowThreshold(t *testing.T) {
	now := time.Date(2026, 7, 11, 18, 0, 0, 0, time.UTC)
	d := func(v int) *int { return &v }

	// Operator requirement (a): at or below the threshold (2), name the killed
	// chain(s) inline — a bare number sends the captain to the logs anyway.
	named := renderBriefing(briefingData{Guards: &guardsData{
		Kills:        2,
		KilledChains: []killedChain{{Name: "FABRICS", RatePerHour: d(-12_000)}, {Name: "PLASTICS"}},
	}}, now)
	require.Contains(t, named, "kills 2")
	require.Contains(t, named, "FABRICS")
	require.Contains(t, named, "-12k/hr")
	require.Contains(t, named, "PLASTICS")

	// Above the threshold: only the count, no inline names (the list would be
	// noise, not a shortcut).
	bulk := renderBriefing(briefingData{Guards: &guardsData{
		Kills:        3,
		KilledChains: []killedChain{{Name: "FABRICS"}, {Name: "PLASTICS"}, {Name: "FUEL"}},
	}}, now)
	require.Contains(t, bulk, "kills 3")
	require.NotContains(t, bulk, "FABRICS")
}

func TestBriefingPromotesInventoryToPositionTwoWithinEraEndWindow(t *testing.T) {
	now := time.Date(2026, 7, 11, 18, 0, 0, 0, time.UTC)
	reset := now.Add(3 * time.Hour) // inside the T-6h window
	data := fullBriefingData()
	data.EraReset = &reset

	body := renderBriefing(data, now)

	// INVENTORY promotes to position 2 — immediately after TREASURY, ahead of
	// every program line — and carries the dump-deadline countdown.
	requireOrder(t, body, []string{"TREASURY", "INVENTORY", "TREND", "GUARDS"})
	require.Contains(t, body, "dump T-3")
	// It must appear exactly once (moved, not duplicated).
	require.Equal(t, 1, strings.Count(body, "INVENTORY"))
}

func TestBriefingKeepsInventoryLateOutsideEraEndWindow(t *testing.T) {
	now := time.Date(2026, 7, 11, 18, 0, 0, 0, time.UTC)
	farReset := now.Add(12 * time.Hour) // outside the T-6h window
	data := fullBriefingData()
	data.EraReset = &farReset

	body := renderBriefing(data, now)

	// No promotion: INVENTORY stays in its normal late slot (after COVERAGE),
	// and no countdown is appended.
	requireOrder(t, body, []string{"TREND", "COVERAGE", "INVENTORY"})
	require.NotContains(t, body, "dump T-")

	// A nil era reset (unreadable status) fails open to no promotion, same shape.
	data.EraReset = nil
	body = renderBriefing(data, now)
	requireOrder(t, body, []string{"COVERAGE", "INVENTORY"})
	require.NotContains(t, body, "dump T-")
}

func TestBriefingCapsPathologicalBlockAtRenderCap(t *testing.T) {
	now := time.Date(2026, 7, 11, 18, 0, 0, 0, time.UTC)
	data := fullBriefingData()
	// A pathological read: hundreds of stranded hulls named inline would blow up
	// the wake mail. The cap is a backstop.
	for i := 0; i < 500; i++ {
		data.Posture.StrandedDetail = append(data.Posture.StrandedDetail,
			strandedHull{Waypoint: "X1-STRANDED-999", Reason: "no_reachable_source"})
	}

	body := renderBriefing(data, now)

	require.LessOrEqual(t, len(body), briefingRenderCap+len("…(capped)\n"))
	require.Contains(t, body, "…(capped)")
	// The cap drops whole trailing lines, never a partial one.
	for _, ln := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		require.NotEmpty(t, ln)
	}
}

func TestBriefingDegradesEachLineToNAAndOmitsEmptyEngineering(t *testing.T) {
	now := time.Date(2026, 7, 11, 18, 0, 0, 0, time.UTC)

	// Every read failed: all sub-structs nil. The block must still render, one
	// "n/a" per line, in strict order — a briefing read failure never blocks a
	// wake (fail-open doctrine).
	body := renderBriefing(briefingData{}, now)

	requireOrder(t, body, orderedLabels)
	require.Equal(t, len(orderedLabels), strings.Count(body, "n/a"),
		"expected exactly one n/a per line:\n%s", body)
	// ENGINEERING is omit-if-empty: absent data must not render the label at all.
	require.NotContains(t, body, "ENGINEERING")
}
