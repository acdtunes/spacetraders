package common

import (
	"context"
	"errors"
	"testing"
)

// TestCapitalSplit is the table the per-operation capital budget stands on (sp-ftqgp). The three
// properties it pins are the ones the shared-pool-plus-floors model lacked: PROPORTIONAL (neither
// side can crowd the other out), CLAMPED (no share can starve a side entirely), and GRACEFULLY
// DEGRADING (an idle side yields its whole budget rather than leaving capital unspent).
func TestCapitalSplit(t *testing.T) {
	cases := []struct {
		name             string
		share            int
		deployable       int64
		tradeWork        bool
		constructionWork bool
		wantTrade        int64
		wantConstruction int64
	}{
		// PROPORTIONAL: the Admiral-approved 60/40 on a representative deployable pool.
		// 40% of ~290k still puts ~116k per cycle into gate materials.
		{"both working splits 60/40", TradeCapitalSharePct, 290_000, true, true, 174_000, 116_000},

		// GRACEFUL DEGRADATION — the property this bead exists for. Idle capital is the
		// failure mode; a stopped gate must not leave 40% of the treasury unspent.
		{"construction idle hands trade the whole pool", TradeCapitalSharePct, 290_000, true, false, 290_000, 0},
		{"trade idle hands construction the whole pool", TradeCapitalSharePct, 290_000, false, true, 0, 290_000},
		{"neither side working allocates nothing", TradeCapitalSharePct, 290_000, false, false, 0, 0},

		// Degradation beats the share: even a 0 share gives trade everything when it is the
		// only side working, and a 100 share gives construction everything when it is.
		{"zero share still yields the whole pool to a lone trade side", 0, 1_000, true, false, 1_000, 0},
		{"full share still yields the whole pool to a lone construction side", 100, 1_000, false, true, 0, 1_000},

		// CLAMPING at both bounds: an out-of-range share is pulled back into [0,100] rather
		// than inverting the split or over-allocating.
		{"share above the max clamps to pure trade", 175, 1_000, true, true, 1_000, 0},
		{"share below the min clamps to pure construction", -40, 1_000, true, true, 0, 1_000},
		{"exactly the max is pure trade", 100, 1_000, true, true, 1_000, 0},
		{"exactly the min is pure construction", 0, 1_000, true, true, 0, 1_000},

		// Degenerate pools: nothing to divide, and a negative deployable (a treasury under
		// the floor, defended twice) can never produce a negative budget.
		{"zero deployable allocates nothing", TradeCapitalSharePct, 0, true, true, 0, 0},
		{"negative deployable floors at zero", TradeCapitalSharePct, -500_000, true, true, 0, 0},
		{"negative deployable floors at zero under degradation too", TradeCapitalSharePct, -500_000, true, false, 0, 0},

		// Rounding: 60% of 5 is 3.0 and of 7 is 4.2 -> 4 (round to nearest), with
		// construction taking the exact remainder so the two always sum to deployable.
		{"rounds to nearest and conserves the total", TradeCapitalSharePct, 7, true, true, 4, 3},
		{"one credit is not lost to rounding", TradeCapitalSharePct, 1, true, true, 1, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			trade, construction := CapitalSplit(tc.share, tc.deployable, tc.tradeWork, tc.constructionWork)
			if trade != tc.wantTrade || construction != tc.wantConstruction {
				t.Fatalf("CapitalSplit(%d, %d, trade=%v, construction=%v) = (%d, %d), want (%d, %d)",
					tc.share, tc.deployable, tc.tradeWork, tc.constructionWork, trade, construction, tc.wantTrade, tc.wantConstruction)
			}
			// TOTAL-CONSERVING whenever any side has work: the invariant both callers rely
			// on to never over- or under-allocate the pool.
			if tc.tradeWork || tc.constructionWork {
				want := tc.deployable
				if want < 0 {
					want = 0
				}
				if trade+construction != want {
					t.Fatalf("budgets sum to %d, want the whole %d deployable", trade+construction, want)
				}
			}
			if trade < 0 || construction < 0 {
				t.Fatalf("a budget may never be negative, got (%d, %d)", trade, construction)
			}
		})
	}
}

// TestCapitalDeployable pins that the pool is derived from the CALLER'S floor (RULINGS #5: no
// second floor constant to drift from reserve_floor.go) and can never go negative.
func TestCapitalDeployable(t *testing.T) {
	cases := []struct {
		name     string
		treasury int64
		floor    int64
		want     int64
	}{
		{"everything above the caller's floor is deployable", 440_000, 300_000, 140_000},
		{"a different caller floor yields a different pool from the same treasury", 440_000, NonContractWorkingCapitalFloor, 290_000},
		{"treasury exactly at the floor deploys nothing", 300_000, 300_000, 0},
		{"treasury below the floor deploys nothing, never negative", 193_000, 300_000, 0},
		{"a zero floor deploys the whole treasury", 1_000, 0, 1_000},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CapitalDeployable(tc.treasury, tc.floor); got != tc.want {
				t.Fatalf("CapitalDeployable(%d, %d) = %d, want %d", tc.treasury, tc.floor, got, tc.want)
			}
		})
	}
}

// TestBudgetedSpendFloorNeverWeakensTheFloor is the RULINGS #4 guard on the whole bead: the budget
// may only ever ADD a constraint. Whatever the inputs — including nonsense a caller could never
// produce — the enforced floor is >= the base floor, and it equals the base exactly when this side
// holds the whole deployable pool (so graceful degradation is byte-identical to the old behavior).
func TestBudgetedSpendFloorNeverWeakensTheFloor(t *testing.T) {
	cases := []struct {
		name       string
		floor      int64
		deployable int64
		ownBudget  int64
		want       int64
	}{
		{"holding the whole pool leaves the base floor untouched", 150_000, 290_000, 290_000, 150_000},
		{"holding 40% raises the floor by the 60% reserved for the other side", 150_000, 290_000, 116_000, 324_000},
		{"holding nothing reserves the whole pool", 150_000, 290_000, 0, 440_000},
		{"an empty pool leaves the base floor untouched", 300_000, 0, 0, 300_000},
		// Defensive: CapitalSplit can never emit these, but the floor must hold anyway.
		{"a budget larger than the pool never lowers the floor", 150_000, 290_000, 999_999, 150_000},
		{"a negative budget still only raises the floor", 150_000, 290_000, -1_000, 441_000},
		{"a negative pool never lowers the floor", 150_000, -50_000, 0, 150_000},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BudgetedSpendFloor(tc.floor, tc.deployable, tc.ownBudget)
			if got != tc.want {
				t.Fatalf("BudgetedSpendFloor(%d, %d, %d) = %d, want %d", tc.floor, tc.deployable, tc.ownBudget, got, tc.want)
			}
			if got < tc.floor {
				t.Fatalf("BudgetedSpendFloor returned %d, BELOW the %d base floor — a money guard was weakened (RULINGS #4)", got, tc.floor)
			}
		})
	}
}

// TestBudgetedSpendFloorRoundTripsThroughTheSplit ties the two halves together on the path the
// engines actually take: whatever the split hands a side, the floor it then enforces is never
// below the base, and the two sides' floors together always leave the whole deployable pool
// spendable — no credit is fenced off from both engines at once.
func TestBudgetedSpendFloorRoundTripsThroughTheSplit(t *testing.T) {
	const floor int64 = 150_000
	for _, deployable := range []int64{0, 1, 7, 250, 290_000, 1_180_000} {
		for _, constructionWork := range []bool{true, false} {
			trade, construction := CapitalSplit(TradeCapitalSharePct, deployable, true, constructionWork)

			tradeFloor := BudgetedSpendFloor(floor, deployable, trade)
			constructionFloor := BudgetedSpendFloor(floor, deployable, construction)
			if tradeFloor < floor || constructionFloor < floor {
				t.Fatalf("deployable %d: floors (%d, %d) fell below the %d base", deployable, tradeFloor, constructionFloor, floor)
			}
			// Each side's headroom is exactly its budget, so the two headrooms sum to the
			// whole pool: nothing is stranded between the engines.
			treasury := floor + deployable
			if got := (treasury - tradeFloor) + (treasury - constructionFloor); got != deployable {
				t.Fatalf("deployable %d (construction working=%v): combined headroom %d, want the whole %d — capital is stranded",
					deployable, constructionWork, got, deployable)
			}
		}
	}
}

// --- sensor ---

type fakeActiveContainerReader struct {
	active map[string]bool
	err    error
	asked  [][]string
}

func (f *fakeActiveContainerReader) HasActiveContainerOfType(_ context.Context, _ int, containerTypes ...string) (bool, error) {
	f.asked = append(f.asked, append([]string(nil), containerTypes...))
	if f.err != nil {
		return false, f.err
	}
	for _, t := range containerTypes {
		if f.active[t] {
			return true, nil
		}
	}
	return false, nil
}

// TestContainerCapitalWorkSensorAsksForTheRightContainerTypes pins the mapping from "engine" to
// the container types that actually spend that engine's capital. A wrong or stale type string
// here is invisible at compile time and would silently hand one side 100% forever.
func TestContainerCapitalWorkSensorAsksForTheRightContainerTypes(t *testing.T) {
	cases := []struct {
		name    string
		active  string
		wantErr bool
		// wantTrade / wantConstruction: what each predicate reports with `active` running.
		wantTrade        bool
		wantConstruction bool
	}{
		{name: "a trading worker makes trade live only", active: "TRADING", wantTrade: true},
		{name: "the trade fleet coordinator makes trade live only", active: "TRADE_FLEET_COORDINATOR", wantTrade: true},
		{name: "the construction drain makes construction live only", active: "CONSTRUCTION_COORDINATOR", wantConstruction: true},
		{name: "an unrelated container makes neither live", active: "CONTRACT_WORKFLOW"},
		{name: "nothing running makes neither live", active: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := &fakeActiveContainerReader{active: map[string]bool{tc.active: true}}
			sensor := NewContainerCapitalWorkSensor(reader)

			gotTrade, err := sensor.TradeHasWork(context.Background(), 1)
			if err != nil {
				t.Fatalf("TradeHasWork: unexpected error %v", err)
			}
			gotConstruction, err := sensor.ConstructionHasWork(context.Background(), 1)
			if err != nil {
				t.Fatalf("ConstructionHasWork: unexpected error %v", err)
			}
			if gotTrade != tc.wantTrade || gotConstruction != tc.wantConstruction {
				t.Fatalf("with %q running: (trade=%v, construction=%v), want (trade=%v, construction=%v); asked %v",
					tc.active, gotTrade, gotConstruction, tc.wantTrade, tc.wantConstruction, reader.asked)
			}
		})
	}
}

// TestContainerCapitalWorkSensorSurfacesReadErrors pins that a failed read is REPORTED rather than
// silently answered "idle" — the callers turn an error into the conservative "the other side is
// busy", which they can only do if the error reaches them.
func TestContainerCapitalWorkSensorSurfacesReadErrors(t *testing.T) {
	sensor := NewContainerCapitalWorkSensor(&fakeActiveContainerReader{err: errors.New("db down")})
	if _, err := sensor.TradeHasWork(context.Background(), 1); err == nil {
		t.Fatal("TradeHasWork swallowed a read error — the caller can no longer fail conservative")
	}
	if _, err := sensor.ConstructionHasWork(context.Background(), 1); err == nil {
		t.Fatal("ConstructionHasWork swallowed a read error — the caller can no longer fail conservative")
	}
}

// TestContainerCapitalWorkSensorWithoutReaderAnswersConservatively pins the wiring-bug path: with
// no reader the sensor claims the other side IS busy, so the asking engine takes its proportional
// share and never grabs the whole treasury on a blind read.
func TestContainerCapitalWorkSensorWithoutReaderAnswersConservatively(t *testing.T) {
	sensor := NewContainerCapitalWorkSensor(nil)
	for _, c := range []struct {
		name string
		call func() (bool, error)
	}{
		{"trade", func() (bool, error) { return sensor.TradeHasWork(context.Background(), 1) }},
		{"construction", func() (bool, error) { return sensor.ConstructionHasWork(context.Background(), 1) }},
	} {
		has, err := c.call()
		if err != nil {
			t.Fatalf("%s: unexpected error %v", c.name, err)
		}
		if !has {
			t.Fatalf("%s: an unwired reader must answer TRUE (the other side is busy), got false — the asking engine would take 100%%", c.name)
		}
	}
}
