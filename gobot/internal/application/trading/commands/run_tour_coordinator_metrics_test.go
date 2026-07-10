package commands

import (
	"reflect"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// cb* builders keep these cap-binding cases self-contained (the package's other test
// files own leg/buy/sell/deposit with different arities).
func cbSnap(wp, good string, tradeVolume int) routing.TourGoodSnapshot {
	return routing.TourGoodSnapshot{Waypoint: wp, Good: good, TradeVolume: tradeVolume}
}

func cbLeg(wp string, trades ...routing.TourTrade) routing.TourLeg {
	return routing.TourLeg{Waypoint: wp, Trades: trades}
}

func cbBuy(good string, units int) routing.TourTrade {
	return routing.TourTrade{Good: good, Units: units, IsBuy: true}
}

func cbSell(good string, units int) routing.TourTrade {
	return routing.TourTrade{Good: good, Units: units}
}

func cbDeposit(good string, units int) routing.TourTrade {
	return routing.TourTrade{Good: good, Units: units, IsDeposit: true}
}

func cbAbsorbed(wp, good, side string, planned int, recovering float64) routing.TourMarketAbsorption {
	return routing.TourMarketAbsorption{Waypoint: wp, Good: good, Side: side, PlannedUnits: planned, RecoveringUnits: recovering}
}

func TestClassifyCapBinding(t *testing.T) {
	// tourACapTranches == 2, so the fleet-wide cap is 2 x trade_volume; the netted
	// availability ceiling is that cap less the outstanding depth at the lane.
	tests := []struct {
		name       string
		plan       *routing.TourPlan
		snapshot   []routing.TourGoodSnapshot
		absorption []routing.TourMarketAbsorption
		want       []capBindingSample
	}{
		{
			name:       "bound: sell units reach the netted ceiling",
			plan:       &routing.TourPlan{Legs: []routing.TourLeg{cbLeg("W1", cbSell("IRON", 60))}},
			snapshot:   []routing.TourGoodSnapshot{cbSnap("W1", "IRON", 100)},                                 // cap 200
			absorption: []routing.TourMarketAbsorption{cbAbsorbed("W1", "IRON", absorption.SideSell, 150, 0)}, // ceiling 50
			want:       []capBindingSample{{side: absorption.SideSell, outcome: "bound"}},
		},
		{
			name:       "unbound: sell units stay below the netted ceiling",
			plan:       &routing.TourPlan{Legs: []routing.TourLeg{cbLeg("W1", cbSell("IRON", 30))}},
			snapshot:   []routing.TourGoodSnapshot{cbSnap("W1", "IRON", 100)},
			absorption: []routing.TourMarketAbsorption{cbAbsorbed("W1", "IRON", absorption.SideSell, 150, 0)}, // ceiling 50
			want:       []capBindingSample{{side: absorption.SideSell, outcome: "unbound"}},
		},
		{
			name:       "no absorption at the lane: not scored",
			plan:       &routing.TourPlan{Legs: []routing.TourLeg{cbLeg("W1", cbSell("IRON", 60))}},
			snapshot:   []routing.TourGoodSnapshot{cbSnap("W1", "IRON", 100)},
			absorption: nil,
			want:       []capBindingSample{},
		},
		{
			name:       "buy side classified on the ask lane",
			plan:       &routing.TourPlan{Legs: []routing.TourLeg{cbLeg("W2", cbBuy("COPPER", 25))}},
			snapshot:   []routing.TourGoodSnapshot{cbSnap("W2", "COPPER", 50)},                                // cap 100
			absorption: []routing.TourMarketAbsorption{cbAbsorbed("W2", "COPPER", absorption.SideBuy, 80, 0)}, // ceiling 20
			want:       []capBindingSample{{side: absorption.SideBuy, outcome: "bound"}},
		},
		{
			name:       "deposit tranche skipped even with absorption present",
			plan:       &routing.TourPlan{Legs: []routing.TourLeg{cbLeg("W1", cbDeposit("IRON", 60))}},
			snapshot:   []routing.TourGoodSnapshot{cbSnap("W1", "IRON", 100)},
			absorption: []routing.TourMarketAbsorption{cbAbsorbed("W1", "IRON", absorption.SideSell, 150, 0)},
			want:       []capBindingSample{},
		},
		{
			name:       "decayed EXECUTED residual rounds up and makes the lane count",
			plan:       &routing.TourPlan{Legs: []routing.TourLeg{cbLeg("W1", cbSell("IRON", 10))}},
			snapshot:   []routing.TourGoodSnapshot{cbSnap("W1", "IRON", 100)},                                 // cap 200
			absorption: []routing.TourMarketAbsorption{cbAbsorbed("W1", "IRON", absorption.SideSell, 0, 0.6)}, // outstanding 1, ceiling 199
			want:       []capBindingSample{{side: absorption.SideSell, outcome: "unbound"}},
		},
		{
			name: "multi-good plan scores each touched-and-absorbed lane in order",
			plan: &routing.TourPlan{Legs: []routing.TourLeg{
				cbLeg("W1", cbBuy("IRON", 40)),
				cbLeg("W2", cbSell("COPPER", 10)),
			}},
			snapshot: []routing.TourGoodSnapshot{cbSnap("W1", "IRON", 20), cbSnap("W2", "COPPER", 100)},
			absorption: []routing.TourMarketAbsorption{
				cbAbsorbed("W1", "IRON", absorption.SideBuy, 5, 0),    // cap 40, ceiling 35, buy 40 -> bound
				cbAbsorbed("W2", "COPPER", absorption.SideSell, 5, 0), // cap 200, ceiling 195, sell 10 -> unbound
			},
			want: []capBindingSample{
				{side: absorption.SideBuy, outcome: "bound"},
				{side: absorption.SideSell, outcome: "unbound"},
			},
		},
		{
			name:       "missing snapshot row (cap 0) defensively reads as bound for an absorbed lane",
			plan:       &routing.TourPlan{Legs: []routing.TourLeg{cbLeg("W1", cbSell("IRON", 5))}},
			snapshot:   nil, // no trade_volume -> cap 0 -> ceiling 0
			absorption: []routing.TourMarketAbsorption{cbAbsorbed("W1", "IRON", absorption.SideSell, 10, 0)},
			want:       []capBindingSample{{side: absorption.SideSell, outcome: "bound"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyCapBinding(tt.plan, tt.snapshot, tt.absorption)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("classifyCapBinding() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestShadowSinksFromAbsorption(t *testing.T) {
	view := []routing.TourMarketAbsorption{
		cbAbsorbed("W1", "IRON", absorption.SideSell, 0, 12.3), // executed shadow -> included
		cbAbsorbed("W2", "COPPER", absorption.SideSell, 40, 0), // PLANNED only, no residual -> excluded
		cbAbsorbed("W3", "GOLD", absorption.SideBuy, 0, 5.0),   // residual on BUY side -> excluded (shadows are sell-side)
	}
	sinks := shadowSinksFromAbsorption(view)

	if !sinks[shadowSinkKey{"W1", "IRON"}] {
		t.Error("expected (W1,IRON) sell-side executed shadow to be a ladder sink")
	}
	if sinks[shadowSinkKey{"W2", "COPPER"}] {
		t.Error("(W2,COPPER) has no recovering residual — must not be a ladder sink")
	}
	if sinks[shadowSinkKey{"W3", "GOLD"}] {
		t.Error("(W3,GOLD) residual is on the BUY side — recovery shadows are sell-side only")
	}
}

func TestShadowSinksFromAbsorption_EmptyIsNil(t *testing.T) {
	if got := shadowSinksFromAbsorption(nil); got != nil {
		t.Errorf("expected nil sink set for empty absorption, got %v", got)
	}
	// A nil sink set must read false, not panic, at the buy-time probe.
	var sinks map[shadowSinkKey]bool = shadowSinksFromAbsorption(nil)
	if sinks[shadowSinkKey{"W1", "IRON"}] {
		t.Error("nil sink set must read false")
	}
}
