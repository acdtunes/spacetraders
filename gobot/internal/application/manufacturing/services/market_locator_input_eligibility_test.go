package services

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// sp-r5a6: InputSourceEligibility is the input-poison anti-cycle's detector. Its two return bools
// distinguish the three states that must be treated differently — a healthy source, a DEPLETED
// (readable-but-SCARCE) source that arms the recovery pause, and an UNREADABLE/ABSENT source that
// must NOT (a transient miss must not idle a healthy chain; a sourceless input needs a re-site,
// not a wait). These pins nail that distinction directly.

// A MODERATE+ EXPORT source → eligible (produce).
func TestInputSourceEligibility_ModeratePlus_Eligible(t *testing.T) {
	repo := &plannerStubMarketRepo{
		marketWaypoints: []string{"X1-T-A1"},
		markets:         map[string]*market.Market{"X1-T-A1": newExportMarket(t, "X1-T-A1", "IRON", "MODERATE", "GROWING", 10)},
	}
	locator := NewMarketLocator(repo, nil, nil, nil)

	eligible, hasReadable, err := locator.InputSourceEligibility(context.Background(), "IRON", "X1-T", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !eligible || !hasReadable {
		t.Errorf("MODERATE source: eligible=%v hasReadable=%v, want true/true", eligible, hasReadable)
	}
}

// Only SCARCE/LIMITED EXPORT sources → NOT eligible but hasReadableSource=true: POSITIVE evidence
// of depletion, the only state that arms the recovery pause.
func TestInputSourceEligibility_OnlyScarce_DepletedReadable(t *testing.T) {
	repo := &plannerStubMarketRepo{
		marketWaypoints: []string{"X1-T-A1", "X1-T-B2"},
		markets: map[string]*market.Market{
			"X1-T-A1": newExportMarket(t, "X1-T-A1", "IRON", "SCARCE", "RESTRICTED", 10),
			"X1-T-B2": newExportMarket(t, "X1-T-B2", "IRON", "LIMITED", "WEAK", 12),
		},
	}
	locator := NewMarketLocator(repo, nil, nil, nil)

	eligible, hasReadable, err := locator.InputSourceEligibility(context.Background(), "IRON", "X1-T", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eligible || !hasReadable {
		t.Errorf("SCARCE/LIMITED only: eligible=%v hasReadable=%v, want false/true (depleted-readable)", eligible, hasReadable)
	}
}

// No EXPORT source for the good in-system (a per-waypoint read miss / cold cache / no local
// source) → NOT eligible AND hasReadableSource=false: must NOT arm the recovery pause.
func TestInputSourceEligibility_NoReadableSource(t *testing.T) {
	repo := &plannerStubMarketRepo{
		marketWaypoints: []string{"X1-T-A1"}, // listed, but its market data is absent (a read miss)
		markets:         map[string]*market.Market{},
	}
	locator := NewMarketLocator(repo, nil, nil, nil)

	eligible, hasReadable, err := locator.InputSourceEligibility(context.Background(), "IRON", "X1-T", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eligible || hasReadable {
		t.Errorf("no readable source: eligible=%v hasReadable=%v, want false/false", eligible, hasReadable)
	}
}

// A market-list read failure surfaces the error (the caller fails toward production).
func TestInputSourceEligibility_ListReadFailure_Errors(t *testing.T) {
	repo := &errFindAllRepo{err: errors.New("market list unreadable")}
	locator := NewMarketLocator(repo, nil, nil, nil)

	_, _, err := locator.InputSourceEligibility(context.Background(), "IRON", "X1-T", 1)
	if err == nil {
		t.Error("expected an error when the market-list read fails")
	}
}

// errFindAllRepo fails the system-market list read to exercise the error return.
type errFindAllRepo struct {
	market.MarketRepository
	err error
}

func (r *errFindAllRepo) FindAllMarketsInSystem(_ context.Context, _ string, _ int) ([]string, error) {
	return nil, r.err
}
