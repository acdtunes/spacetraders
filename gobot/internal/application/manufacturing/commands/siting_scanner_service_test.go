package commands

import (
	"context"
	"errors"
	"testing"
)

// --- Fakes for the SCAN ports ---

type fakeMarketSource struct {
	systems       []string
	systemsErr    error
	goodsBySystem map[string][]MarketGood
	goodsErr      map[string]error
}

func (f *fakeMarketSource) Systems(ctx context.Context, playerID int) ([]string, error) {
	return f.systems, f.systemsErr
}

func (f *fakeMarketSource) GoodsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]MarketGood, error) {
	if e := f.goodsErr[systemSymbol]; e != nil {
		return nil, e
	}
	return f.goodsBySystem[systemSymbol], nil
}

type fakeRecipeResolver struct {
	feeds       map[string][]string // "good@system" -> feed goods
	notInSystem map[string]bool
	err         map[string]error
}

func (f *fakeRecipeResolver) Feeds(ctx context.Context, targetGood, systemSymbol string, playerID int) ([]string, bool, error) {
	key := targetGood + "@" + systemSymbol
	if e := f.err[key]; e != nil {
		return nil, false, e
	}
	if f.notInSystem[key] {
		return nil, false, nil
	}
	return f.feeds[key], true, nil
}

type fakeInputSource struct {
	wp         map[string]string // "good@system" -> source waypoint
	ineligible map[string]bool
	err        map[string]error
}

func (f *fakeInputSource) Source(ctx context.Context, good, systemSymbol string, playerID int) (string, bool, error) {
	key := good + "@" + systemSymbol
	if e := f.err[key]; e != nil {
		return "", false, e
	}
	if f.ineligible[key] {
		return "", false, nil
	}
	wp := f.wp[key]
	if wp == "" {
		wp = "WP-" + good
	}
	return wp, true, nil
}

// exportGood / importGood are MarketGood fixture helpers.
func exportGood(good string, age float64) MarketGood {
	return MarketGood{Good: good, TradeType: "EXPORT", AgeSecs: age}
}
func importGood(good string, age float64) MarketGood {
	return MarketGood{Good: good, TradeType: "IMPORT", AgeSecs: age}
}

func newScanner(ms *fakeMarketSource, rr *fakeRecipeResolver, is *fakeInputSource) *SitingScannerService {
	if rr == nil {
		rr = &fakeRecipeResolver{}
	}
	if is == nil {
		is = &fakeInputSource{}
	}
	return NewSitingScannerService(ms, rr, is)
}

// A candidate passing every gate is returned with its data age and feed source markets.
func TestScan_HappyCandidatePasses(t *testing.T) {
	ms := &fakeMarketSource{
		systems: []string{"X1-AA"},
		goodsBySystem: map[string][]MarketGood{
			"X1-AA": {exportGood("ELECTRONICS", 100), importGood("COPPER", 50)},
		},
	}
	rr := &fakeRecipeResolver{feeds: map[string][]string{"ELECTRONICS@X1-AA": {"COPPER", "SILICON"}}}
	is := &fakeInputSource{wp: map[string]string{"COPPER@X1-AA": "X1-AA-C1", "SILICON@X1-AA": "X1-AA-S1"}}

	got, err := newScanner(ms, rr, is).ScanCandidates(context.Background(), 1, SitingScanParams{FreshnessMaxSecs: 7200})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 candidate, got %d: %+v", len(got), got)
	}
	c := got[0]
	if c.Good != "ELECTRONICS" || c.System != "X1-AA" || c.DataAgeSecs != 100 {
		t.Errorf("candidate = %+v, want ELECTRONICS@X1-AA age 100", c)
	}
	if len(c.InputMarkets) != 2 || c.InputMarkets[0] != "X1-AA-C1" || c.InputMarkets[1] != "X1-AA-S1" {
		t.Errorf("InputMarkets = %v, want [X1-AA-C1 X1-AA-S1]", c.InputMarkets)
	}
}

// HARD GATE: a good with no EXPORT market in-system (only IMPORT/EXCHANGE) is excluded.
func TestScan_NoExportMarketExcluded(t *testing.T) {
	ms := &fakeMarketSource{
		systems: []string{"X1-AA"},
		goodsBySystem: map[string][]MarketGood{
			"X1-AA": {importGood("ELECTRONICS", 100), {Good: "FUEL", TradeType: "EXCHANGE", AgeSecs: 100}},
		},
	}
	// Even if a recipe/eligibility would pass, the good never reaches those gates.
	rr := &fakeRecipeResolver{feeds: map[string][]string{"ELECTRONICS@X1-AA": {"COPPER"}}}
	got, err := newScanner(ms, rr, nil).ScanCandidates(context.Background(), 1, SitingScanParams{FreshnessMaxSecs: 7200})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("no EXPORT market → no candidate; got %+v", got)
	}
}

// SOFT GATE: a feed input with no eligible in-system source excludes the candidate (a5j7).
func TestScan_IneligibleInputExcluded(t *testing.T) {
	ms := &fakeMarketSource{
		systems:       []string{"X1-AA"},
		goodsBySystem: map[string][]MarketGood{"X1-AA": {exportGood("ELECTRONICS", 100)}},
	}
	rr := &fakeRecipeResolver{feeds: map[string][]string{"ELECTRONICS@X1-AA": {"COPPER", "SILICON"}}}
	is := &fakeInputSource{ineligible: map[string]bool{"SILICON@X1-AA": true}} // one feed has no MODERATE+ source

	got, err := newScanner(ms, rr, is).ScanCandidates(context.Background(), 1, SitingScanParams{FreshnessMaxSecs: 7200})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ineligible feed → no candidate; got %+v", got)
	}
}

// FRESHNESS GATE: market data older than the threshold excludes the candidate.
func TestScan_StaleExcluded(t *testing.T) {
	ms := &fakeMarketSource{
		systems:       []string{"X1-AA"},
		goodsBySystem: map[string][]MarketGood{"X1-AA": {exportGood("ELECTRONICS", 9000)}}, // > 7200
	}
	rr := &fakeRecipeResolver{feeds: map[string][]string{"ELECTRONICS@X1-AA": {"COPPER"}}}
	got, err := newScanner(ms, rr, nil).ScanCandidates(context.Background(), 1, SitingScanParams{FreshnessMaxSecs: 7200})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("stale data → no candidate; got %+v", got)
	}
}

// SOFT GATE: a recipe that does not resolve entirely in-system is excluded (in-system rule).
func TestScan_RecipeNotInSystemExcluded(t *testing.T) {
	ms := &fakeMarketSource{
		systems:       []string{"X1-AA"},
		goodsBySystem: map[string][]MarketGood{"X1-AA": {exportGood("ELECTRONICS", 100)}},
	}
	rr := &fakeRecipeResolver{notInSystem: map[string]bool{"ELECTRONICS@X1-AA": true}}
	got, err := newScanner(ms, rr, nil).ScanCandidates(context.Background(), 1, SitingScanParams{FreshnessMaxSecs: 7200})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("recipe not in-system → no candidate; got %+v", got)
	}
}

// A good that exports but has no fabrication recipe (no feeds) is not a chain site → excluded.
func TestScan_NoFeedsExcluded(t *testing.T) {
	ms := &fakeMarketSource{
		systems:       []string{"X1-AA"},
		goodsBySystem: map[string][]MarketGood{"X1-AA": {exportGood("IRON_ORE", 100)}},
	}
	rr := &fakeRecipeResolver{feeds: map[string][]string{"IRON_ORE@X1-AA": {}}} // resolves, but no BUY-leaf feeds
	got, err := newScanner(ms, rr, nil).ScanCandidates(context.Background(), 1, SitingScanParams{FreshnessMaxSecs: 7200})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("no feeds → no candidate; got %+v", got)
	}
}

// The same export good in two markets in one system yields ONE candidate at the freshest age.
func TestScan_DedupKeepsFreshest(t *testing.T) {
	ms := &fakeMarketSource{
		systems: []string{"X1-AA"},
		goodsBySystem: map[string][]MarketGood{
			"X1-AA": {exportGood("ELECTRONICS", 500), exportGood("ELECTRONICS", 120)},
		},
	}
	rr := &fakeRecipeResolver{feeds: map[string][]string{"ELECTRONICS@X1-AA": {"COPPER"}}}
	got, err := newScanner(ms, rr, nil).ScanCandidates(context.Background(), 1, SitingScanParams{FreshnessMaxSecs: 7200})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].DataAgeSecs != 120 {
		t.Errorf("want 1 candidate at freshest age 120, got %+v", got)
	}
}

// A system whose good-listing read errors is skipped; other systems still scan.
func TestScan_SystemErrorSkipsSystemNotScan(t *testing.T) {
	ms := &fakeMarketSource{
		systems:  []string{"X1-BAD", "X1-OK"},
		goodsErr: map[string]error{"X1-BAD": errors.New("db down for this system")},
		goodsBySystem: map[string][]MarketGood{
			"X1-OK": {exportGood("ELECTRONICS", 100)},
		},
	}
	rr := &fakeRecipeResolver{feeds: map[string][]string{"ELECTRONICS@X1-OK": {"COPPER"}}}
	got, err := newScanner(ms, rr, nil).ScanCandidates(context.Background(), 1, SitingScanParams{FreshnessMaxSecs: 7200})
	if err != nil {
		t.Fatalf("system-level error must not abort scan: %v", err)
	}
	if len(got) != 1 || got[0].System != "X1-OK" {
		t.Errorf("bad system skipped, good system scanned; got %+v", got)
	}
}

// FAIL-CLOSED: a per-candidate recipe/eligibility read error excludes that candidate.
func TestScan_PerCandidateReadErrorExcludes(t *testing.T) {
	ms := &fakeMarketSource{
		systems: []string{"X1-AA"},
		goodsBySystem: map[string][]MarketGood{
			"X1-AA": {exportGood("ELECTRONICS", 100), exportGood("MACHINERY", 100)},
		},
	}
	rr := &fakeRecipeResolver{
		feeds: map[string][]string{"ELECTRONICS@X1-AA": {"COPPER"}, "MACHINERY@X1-AA": {"IRON"}},
		err:   map[string]error{"MACHINERY@X1-AA": errors.New("resolver blip")},
	}
	is := &fakeInputSource{err: map[string]error{"COPPER@X1-AA": errors.New("supply read blip")}}

	got, err := newScanner(ms, rr, is).ScanCandidates(context.Background(), 1, SitingScanParams{FreshnessMaxSecs: 7200})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("both candidates hit read errors → excluded (fail-closed); got %+v", got)
	}
}

// Systems() error aborts the scan (cannot enumerate the universe).
func TestScan_SystemsErrorAborts(t *testing.T) {
	ms := &fakeMarketSource{systemsErr: errors.New("cannot list systems")}
	if _, err := newScanner(ms, nil, nil).ScanCandidates(context.Background(), 1, SitingScanParams{}); err == nil {
		t.Fatal("expected Systems() error to abort the scan")
	}
}
