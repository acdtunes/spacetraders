package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestScanBudgetMetrics_RegisterAndExport proves all four families REGISTER on
// the daemon's registry AND actually appear by name once recorded — the trap
// where a family is "registered" yet never shows on /metrics because no label
// combination was ever touched. That trap is the whole failure this bead exists
// to close: a budget that publishes nothing is indistinguishable from one that
// is never consulted.
func TestScanBudgetMetrics_RegisterAndExport(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewScanBudgetMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.RecordDecision(1, BudgetShipyard, "earning", "spend")
	c.RecordOverdraft(1, BudgetShipyard, "earning")
	c.RecordAllowance(1, BudgetShipyard, 0.12, 84)

	families, err := Registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error: %v", err)
	}
	got := map[string]bool{}
	for _, f := range families {
		got[f.GetName()] = true
	}
	for _, name := range []string{
		"spacetraders_daemon_scan_budget_decisions_total",
		"spacetraders_daemon_scan_budget_overdrafts_total",
		"spacetraders_daemon_scan_budget_rate_req_per_sec",
		"spacetraders_daemon_scan_budget_coverage",
	} {
		if !got[name] {
			t.Errorf("%s registered but not exported on the registry", name)
		}
	}
}

// TestScanBudgetMetrics_DecisionsSplitByBudgetClassAndOutcome pins the label set
// the acceptance queries are written against: an operator must be able to ask
// "what fraction of SHIPYARD reads were Earning-class" without the market
// budget's traffic contaminating the answer, and without the two budgets needing
// separate families.
func TestScanBudgetMetrics_DecisionsSplitByBudgetClassAndOutcome(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewScanBudgetMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.RecordDecision(7, BudgetShipyard, "earning", "spend")
	c.RecordDecision(7, BudgetShipyard, "earning", "spend")
	c.RecordDecision(7, BudgetShipyard, "discretionary", "serve_from_store")
	c.RecordDecision(7, BudgetMarket, "earning", "spend")

	cases := []struct {
		name   string
		labels map[string]string
		want   float64
	}{
		{
			"shipyard earning spends accumulate",
			map[string]string{"player_id": "7", "budget": "shipyard", "class": "earning", "decision": "spend"},
			2,
		},
		{
			"shipyard discretionary declines are a separate series",
			map[string]string{"player_id": "7", "budget": "shipyard", "class": "discretionary", "decision": "serve_from_store"},
			1,
		},
		{
			"the market budget does not contaminate the shipyard series",
			map[string]string{"player_id": "7", "budget": "market", "class": "earning", "decision": "spend"},
			1,
		},
	}
	for _, tc := range cases {
		got, ok := gatherCounter(t, Registry, "spacetraders_daemon_scan_budget_decisions_total", tc.labels)
		if !ok {
			t.Errorf("%s: series not found", tc.name)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestScanBudgetMetrics_AllowanceGaugesAreSetNotAccumulated is the sp-fpgl2
// guard expressed as a test. The rate and the coverage denominator both move in
// BOTH directions — a map shrinks when an era rolls, a rate is retuned down —
// so they must be Gauges that SET. Were either an accumulating observation, a
// budget re-publishing 0.12 on every admission would climb without bound and an
// operator would read a phantom rising allowance.
func TestScanBudgetMetrics_AllowanceGaugesAreSetNotAccumulated(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewScanBudgetMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	// Three admissions at the same allowance, then the map SHRINKS and the rate
	// is retuned DOWN — the direction a counter could never express.
	c.RecordAllowance(3, BudgetShipyard, 0.12, 84)
	c.RecordAllowance(3, BudgetShipyard, 0.12, 84)
	c.RecordAllowance(3, BudgetShipyard, 0.12, 84)
	c.RecordAllowance(3, BudgetShipyard, 0.05, 40)

	labels := map[string]string{"player_id": "3", "budget": "shipyard"}

	gotRate, ok := gatherGauge(t, Registry, "spacetraders_daemon_scan_budget_rate_req_per_sec", labels)
	if !ok {
		t.Fatal("rate gauge series not found")
	}
	if gotRate != 0.05 {
		t.Errorf("rate: got %v, want 0.05 (the LAST value, not a sum of every republish)", gotRate)
	}

	gotCoverage, ok := gatherGauge(t, Registry, "spacetraders_daemon_scan_budget_coverage", labels)
	if !ok {
		t.Fatal("coverage gauge series not found")
	}
	if gotCoverage != 40 {
		t.Errorf("coverage: got %v, want 40 (a shrinking map must read DOWN)", gotCoverage)
	}
}

// TestScanBudgetMetrics_NilCollectorIsSafe — recording is pure observation
// (RULINGS #4). Metrics are disabled in every unit test and in any daemon run
// without the flag, so the budgets call through a nil collector on the hot
// admission path; a panic here would take down an admission decision, which
// observation must never do.
func TestScanBudgetMetrics_NilCollectorIsSafe(t *testing.T) {
	var c *ScanBudgetMetricsCollector
	c.RecordDecision(1, BudgetMarket, "discretionary", "spend")
	c.RecordOverdraft(1, BudgetMarket, "discretionary")
	c.RecordAllowance(1, BudgetMarket, 0.35, 4389)
}

// TestScanBudgetMetrics_GlobalIsNilUntilInstalled pins the lazy-resolution
// contract the budgets depend on: they are constructed during handler wiring,
// BEFORE the daemon installs this collector, so an unset global must read nil
// (record nothing) rather than panic — and must start recording the moment it is
// installed.
func TestScanBudgetMetrics_GlobalIsNilUntilInstalled(t *testing.T) {
	prev := globalScanBudgetCollector
	t.Cleanup(func() { globalScanBudgetCollector = prev })

	globalScanBudgetCollector = nil
	if GetGlobalScanBudgetCollector() != nil {
		t.Fatal("an uninstalled global must read nil so recording is a no-op")
	}

	c := NewScanBudgetMetricsCollector()
	SetGlobalScanBudgetCollector(c)
	if GetGlobalScanBudgetCollector() != c {
		t.Error("the installed collector must be the one the budgets resolve")
	}
}
