package services

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

func TestCalculateMarketScore_PinsActivityAndSupplyTables(t *testing.T) {
	cases := []struct {
		activity string
		supply   string
		expected int
	}{
		{"STRONG", "", 65}, {"GROWING", "", 45}, {"WEAK", "", 25}, {"RESTRICTED", "", 20}, {"", "", 35},
		{"", "ABUNDANT", 70}, {"", "HIGH", 60}, {"", "MODERATE", 50}, {"", "LIMITED", 40}, {"", "SCARCE", 30},
		{"STRONG", "ABUNDANT", 100}, {"RESTRICTED", "SCARCE", 15}, {"GROWING", "MODERATE", 60},
	}
	for _, tc := range cases {
		if got := calculateMarketScore(tc.activity, tc.supply); got != tc.expected {
			t.Errorf("calculateMarketScore(%q, %q) = %d, want %d", tc.activity, tc.supply, got, tc.expected)
		}
	}
}

func TestExportActivityScore_PinsBuyerSideOrdering(t *testing.T) {
	cases := map[string]int{"WEAK": 4, "GROWING": 3, "STRONG": 2, "RESTRICTED": 1, "": 2, "UNKNOWN_VALUE": 2}
	for activity, expected := range cases {
		if got := ExportActivityScore(activity); got != expected {
			t.Errorf("ExportActivityScore(%q) = %d, want %d", activity, got, expected)
		}
	}
}

func TestImportActivityScore_PinsSellerSideOrdering(t *testing.T) {
	cases := map[string]int{"STRONG": 4, "GROWING": 3, "WEAK": 2, "RESTRICTED": 1, "": 2, "UNKNOWN_VALUE": 2}
	for activity, expected := range cases {
		if got := ImportActivityScore(activity); got != expected {
			t.Errorf("ImportActivityScore(%q) = %d, want %d", activity, got, expected)
		}
	}
}

func TestCollectionOpportunityScore_PinsBonusTables(t *testing.T) {
	cases := []struct {
		name            string
		factorySupply   string
		sellActivity    string
		factoryActivity string
		expected        int
	}{
		{"abundant strong weak", "ABUNDANT", "STRONG", "WEAK", 1800},
		{"high growing growing", "HIGH", "GROWING", "GROWING", 1400},
		{"abundant weak strong", "ABUNDANT", "WEAK", "STRONG", 1250},
		{"high restricted restricted", "HIGH", "RESTRICTED", "RESTRICTED", 1000},
		{"unknown levels", "", "", "", 1000},
	}
	for _, tc := range cases {
		opp := &CollectionOpportunity{
			ExpectedProfit:     1000,
			FactorySupply:      tc.factorySupply,
			SellMarketActivity: tc.sellActivity,
			FactoryActivity:    tc.factoryActivity,
		}
		if got := opp.Score(); got != tc.expected {
			t.Errorf("%s: Score() = %d, want %d", tc.name, got, tc.expected)
		}
	}
}

func TestApplySourceSupplyPriority_PinsPriorityMapping(t *testing.T) {
	cases := map[string]int{
		"ABUNDANT": 40, "HIGH": 30, "MODERATE": 10, "LIMITED": 10, "SCARCE": 10, "": 10,
	}
	for supply, expected := range cases {
		task := manufacturing.NewAcquireDeliverTask("pipe-1", 1, "IRON", "X1-A-B1", "X1-A-C2", nil)
		applySourceSupplyPriority(task, supply)
		if got := task.Priority(); got != expected {
			t.Errorf("applySourceSupplyPriority(%q): priority = %d, want %d", supply, got, expected)
		}
	}
}

func newExportMarket(t *testing.T, waypointSymbol, good, supply, activity string, sellPrice int) *market.Market {
	t.Helper()
	var supplyPtr, activityPtr *string
	if supply != "" {
		supplyPtr = &supply
	}
	if activity != "" {
		activityPtr = &activity
	}
	tradeGood, err := market.NewTradeGood(good, supplyPtr, activityPtr, sellPrice+10, sellPrice, 40, market.TradeTypeExport)
	if err != nil {
		t.Fatalf("NewTradeGood(%s): %v", good, err)
	}
	m, err := market.NewMarket(waypointSymbol, []market.TradeGood{*tradeGood}, time.Now())
	if err != nil {
		t.Fatalf("NewMarket(%s): %v", waypointSymbol, err)
	}
	return m
}

func TestFindExportMarketBySupplyPriority_PinsTieKeepsFirstListedMarket(t *testing.T) {
	repo := &plannerStubMarketRepo{
		marketWaypoints: []string{"X1-T-A1", "X1-T-B2"},
		markets: map[string]*market.Market{
			"X1-T-A1": newExportMarket(t, "X1-T-A1", "IRON", "HIGH", "WEAK", 90),
			"X1-T-B2": newExportMarket(t, "X1-T-B2", "IRON", "HIGH", "WEAK", 90),
		},
	}
	locator := NewMarketLocator(repo, nil, nil, nil)

	result, err := locator.FindExportMarketBySupplyPriority(context.Background(), "IRON", "X1-T", 1)
	if err != nil {
		t.Fatalf("FindExportMarketBySupplyPriority: %v", err)
	}
	if result.WaypointSymbol != "X1-T-A1" {
		t.Errorf("full tie: expected first listed market X1-T-A1, got %s", result.WaypointSymbol)
	}
}

func TestFindExportMarketBySupplyPriority_PinsTieAfterWorseCandidate(t *testing.T) {
	repo := &plannerStubMarketRepo{
		marketWaypoints: []string{"X1-T-A1", "X1-T-B2", "X1-T-C3"},
		markets: map[string]*market.Market{
			"X1-T-A1": newExportMarket(t, "X1-T-A1", "IRON", "MODERATE", "WEAK", 90),
			"X1-T-B2": newExportMarket(t, "X1-T-B2", "IRON", "ABUNDANT", "WEAK", 90),
			"X1-T-C3": newExportMarket(t, "X1-T-C3", "IRON", "ABUNDANT", "WEAK", 90),
		},
	}
	locator := NewMarketLocator(repo, nil, nil, nil)

	result, err := locator.FindExportMarketBySupplyPriority(context.Background(), "IRON", "X1-T", 1)
	if err != nil {
		t.Fatalf("FindExportMarketBySupplyPriority: %v", err)
	}
	if result.WaypointSymbol != "X1-T-B2" {
		t.Errorf("expected first of tied best markets X1-T-B2, got %s", result.WaypointSymbol)
	}
}

func TestFindExportMarketBySupplyPriority_PinsSupplyThenActivityThenPrice(t *testing.T) {
	repo := &plannerStubMarketRepo{
		marketWaypoints: []string{"X1-T-A1", "X1-T-B2", "X1-T-C3", "X1-T-D4"},
		markets: map[string]*market.Market{
			"X1-T-A1": newExportMarket(t, "X1-T-A1", "IRON", "HIGH", "WEAK", 10),
			"X1-T-B2": newExportMarket(t, "X1-T-B2", "IRON", "ABUNDANT", "GROWING", 50),
			"X1-T-C3": newExportMarket(t, "X1-T-C3", "IRON", "ABUNDANT", "WEAK", 95),
			"X1-T-D4": newExportMarket(t, "X1-T-D4", "IRON", "ABUNDANT", "WEAK", 90),
		},
	}
	locator := NewMarketLocator(repo, nil, nil, nil)

	result, err := locator.FindExportMarketBySupplyPriority(context.Background(), "IRON", "X1-T", 1)
	if err != nil {
		t.Fatalf("FindExportMarketBySupplyPriority: %v", err)
	}
	if result.WaypointSymbol != "X1-T-D4" {
		t.Errorf("expected ABUNDANT+WEAK+cheapest X1-T-D4, got %s", result.WaypointSymbol)
	}
}

func TestFindExportMarketBySupplyPriority_PinsLimitedAndScarceSkipped(t *testing.T) {
	repo := &plannerStubMarketRepo{
		marketWaypoints: []string{"X1-T-A1", "X1-T-B2"},
		markets: map[string]*market.Market{
			"X1-T-A1": newExportMarket(t, "X1-T-A1", "IRON", "LIMITED", "WEAK", 10),
			"X1-T-B2": newExportMarket(t, "X1-T-B2", "IRON", "SCARCE", "WEAK", 10),
		},
	}
	locator := NewMarketLocator(repo, nil, nil, nil)

	if _, err := locator.FindExportMarketBySupplyPriority(context.Background(), "IRON", "X1-T", 1); err == nil {
		t.Error("expected error when only LIMITED/SCARCE markets exist")
	}
}

func TestFindExportMarketWithGoodSupply_PinsTieKeepsFirstListedMarket(t *testing.T) {
	repo := &plannerStubMarketRepo{
		marketWaypoints: []string{"X1-T-A1", "X1-T-B2"},
		markets: map[string]*market.Market{
			"X1-T-A1": newExportMarket(t, "X1-T-A1", "IRON", "HIGH", "WEAK", 90),
			"X1-T-B2": newExportMarket(t, "X1-T-B2", "IRON", "HIGH", "WEAK", 90),
		},
	}
	locator := NewMarketLocator(repo, nil, nil, nil)

	result, err := locator.FindExportMarketWithGoodSupply(context.Background(), "IRON", "X1-T", 1)
	if err != nil {
		t.Fatalf("FindExportMarketWithGoodSupply: %v", err)
	}
	if result == nil || result.WaypointSymbol != "X1-T-A1" {
		t.Errorf("full tie: expected first listed market X1-T-A1, got %+v", result)
	}
}

func TestFindExportMarketWithGoodSupply_PinsAbundantBeatsHighThenPrice(t *testing.T) {
	repo := &plannerStubMarketRepo{
		marketWaypoints: []string{"X1-T-A1", "X1-T-B2", "X1-T-C3"},
		markets: map[string]*market.Market{
			"X1-T-A1": newExportMarket(t, "X1-T-A1", "IRON", "HIGH", "WEAK", 50),
			"X1-T-B2": newExportMarket(t, "X1-T-B2", "IRON", "ABUNDANT", "WEAK", 95),
			"X1-T-C3": newExportMarket(t, "X1-T-C3", "IRON", "ABUNDANT", "WEAK", 90),
		},
	}
	locator := NewMarketLocator(repo, nil, nil, nil)

	result, err := locator.FindExportMarketWithGoodSupply(context.Background(), "IRON", "X1-T", 1)
	if err != nil {
		t.Fatalf("FindExportMarketWithGoodSupply: %v", err)
	}
	if result == nil || result.WaypointSymbol != "X1-T-C3" {
		t.Errorf("expected cheapest ABUNDANT market X1-T-C3, got %+v", result)
	}
}

func TestFindExportMarketWithGoodSupply_PinsModerateReturnsNil(t *testing.T) {
	repo := &plannerStubMarketRepo{
		marketWaypoints: []string{"X1-T-A1"},
		markets: map[string]*market.Market{
			"X1-T-A1": newExportMarket(t, "X1-T-A1", "IRON", "MODERATE", "WEAK", 50),
		},
	}
	locator := NewMarketLocator(repo, nil, nil, nil)

	result, err := locator.FindExportMarketWithGoodSupply(context.Background(), "IRON", "X1-T", 1)
	if err != nil {
		t.Fatalf("FindExportMarketWithGoodSupply: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for MODERATE-only supply, got %+v", result)
	}
}
