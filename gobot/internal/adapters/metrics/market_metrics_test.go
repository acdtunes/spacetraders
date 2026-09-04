package metrics

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// TestMarketMetrics_RegistersExactlySurvivingMetrics pins sp-3hdsk.4's deletion: this collector
// must register the 5 metrics that remain (4 scanner-performance + market_data_age_seconds) and
// none of the 10 poll-fed metrics it deleted (market_coverage_total, market_coverage_fresh,
// market_price_spread, market_best_spread, market_efficiency_percent, market_supply_distribution,
// market_activity_distribution, market_liquidity, trade_opportunities_total, market_best_price) —
// a consumer audit found zero dashboards, Prometheus rules or code reading any of them, while the
// per-(player,system) scope loop that fed them cost ~1-2 Postgres cores every 60s across 340
// systems.
func TestMarketMetrics_RegistersExactlySurvivingMetrics(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewMarketMetricsCollector(nil)
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	// Force one child per vector: an unobserved MetricVec emits no sample and so would not show
	// on Gather() even if Register() had wired it up, making this test pass by accident.
	c.marketScansTotal.WithLabelValues("1", "X1-AA-A1", "success").Inc()
	c.marketScanDurationSeconds.WithLabelValues("1", "X1-AA-A1").Observe(1.0)
	c.marketScanRate.WithLabelValues("1", "X1-AA").Set(1)
	c.marketScannerErrorsTotal.WithLabelValues("1", "unknown").Inc()
	c.marketDataAge.WithLabelValues("1", "X1-AA").Observe(60)

	families, err := Registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error: %v", err)
	}
	var got []string
	for _, f := range families {
		got = append(got, f.GetName())
	}
	sort.Strings(got)

	want := []string{
		"spacetraders_daemon_market_data_age_seconds",
		"spacetraders_daemon_market_scan_duration_seconds",
		"spacetraders_daemon_market_scan_rate",
		"spacetraders_daemon_market_scanner_errors_total",
		"spacetraders_daemon_market_scans_total",
	}
	if len(got) != len(want) {
		t.Fatalf("registered metrics = %v, want exactly %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("registered metrics = %v, want exactly %v", got, want)
		}
	}
}

// ageTestMarketRow / ageTestEraRow are declared locally for the same reason
// market_scope_test.go's fixture is: persistence imports this package back, so a test here
// cannot import persistence's models. These carry the exact columns observeMarketAges and
// openEraPlayerID select and filter on, including last_updated, which market_scope_test.go's
// fixture does not need.
type ageTestMarketRow struct {
	PlayerID       int       `gorm:"column:player_id;primaryKey"`
	WaypointSymbol string    `gorm:"column:waypoint_symbol;primaryKey"`
	GoodSymbol     string    `gorm:"column:good_symbol;primaryKey"`
	LastUpdated    time.Time `gorm:"column:last_updated"`
}

func (ageTestMarketRow) TableName() string { return "market_data" }

type ageTestEraRow struct {
	EraID    int        `gorm:"column:era_id;primaryKey"`
	PlayerID int        `gorm:"column:player_id"`
	ClosedAt *time.Time `gorm:"column:closed_at"`
}

func (ageTestEraRow) TableName() string { return "eras" }

// capturingGormLogger records every SQL statement GORM actually issues, so the query-count
// assertion below counts real round trips rather than trusting the code to have said so.
type capturingGormLogger struct {
	gormlogger.Interface
	statements []string
}

func (l *capturingGormLogger) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	l.statements = append(l.statements, sql)
}

// TestObserveMarketAges_OneQueryPerPlayer_PinnedLabels is the regression pin for sp-3hdsk.4's
// core change: market_data_age_seconds now costs ONE query per open-era player, not the old
// per-(player,system) scope loop's three queries per pair. Two systems are seeded — one with a
// multi-waypoint, multi-good market, to exercise the per-waypoint MAX-collapse the old subquery
// also did — to prove the query count does not scale with system or waypoint count, while the
// per-label observation counts pin that the histogram still reports exactly what the old
// per-scope query did: one observation per distinct waypoint, labelled (player_id,
// system_symbol).
func TestObserveMarketAges_OneQueryPerPlayer_PinnedLabels(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&ageTestEraRow{}, &ageTestMarketRow{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	if err := db.Create(&ageTestEraRow{EraID: 1, PlayerID: 7}).Error; err != nil {
		t.Fatalf("seed open era: %v", err)
	}

	now := time.Now().UTC()
	rows := []ageTestMarketRow{
		// X1-AA-D40: two goods sharing one scan; MAX must collapse them to ONE observation.
		{PlayerID: 7, WaypointSymbol: "X1-AA-D40", GoodSymbol: "ALUMINUM", LastUpdated: now.Add(-90 * time.Second)},
		{PlayerID: 7, WaypointSymbol: "X1-AA-D40", GoodSymbol: "COPPER", LastUpdated: now.Add(-90 * time.Second)},
		// X1-AA-K87: a second market in the same system.
		{PlayerID: 7, WaypointSymbol: "X1-AA-K87", GoodSymbol: "IRON_ORE", LastUpdated: now.Add(-200 * time.Second)},
		// X1-BB-M55: the other system.
		{PlayerID: 7, WaypointSymbol: "X1-BB-M55", GoodSymbol: "FUEL", LastUpdated: now.Add(-400 * time.Second)},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed market rows: %v", err)
	}

	capture := &capturingGormLogger{Interface: db.Logger}
	c := NewMarketMetricsCollector(db.Session(&gorm.Session{Logger: capture}))

	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.updateAllMetrics()

	if len(capture.statements) != 2 {
		t.Fatalf("issued %d queries (%v), want exactly 2 (resolve the open era's player, then one "+
			"age query for it) regardless of the 2 systems / 3 waypoints seeded — the old per-scope "+
			"path issued its own age query PER SYSTEM on top of its other per-scope queries",
			len(capture.statements), capture.statements)
	}

	const metricName = "spacetraders_daemon_market_data_age_seconds"
	if count, ok := gatherHistogramCount(t, Registry, metricName,
		map[string]string{"player_id": "7", "system_symbol": "X1-AA"}); !ok || count != 2 {
		t.Fatalf("X1-AA histogram count = %d, ok=%v, want 2 (one per distinct waypoint, goods collapsed)", count, ok)
	}
	if count, ok := gatherHistogramCount(t, Registry, metricName,
		map[string]string{"player_id": "7", "system_symbol": "X1-BB"}); !ok || count != 1 {
		t.Fatalf("X1-BB histogram count = %d, ok=%v, want 1", count, ok)
	}
}
