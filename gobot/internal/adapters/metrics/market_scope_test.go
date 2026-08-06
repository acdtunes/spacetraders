package metrics

import (
	"sort"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The two tables are declared locally rather than imported from persistence: persistence imports
// this package (ledger_treasury.go), so a test here cannot import it back. What is under test is
// the QUERY, and these carry the exact column names it selects and filters on — market_data's
// player_id / waypoint_symbol and eras' player_id / closed_at / era_id. A rename on either side
// would break the production query and this test together, which is the coupling that matters.

type scopeTestMarketRow struct {
	PlayerID       int    `gorm:"column:player_id;primaryKey"`
	WaypointSymbol string `gorm:"column:waypoint_symbol;primaryKey"`
	GoodSymbol     string `gorm:"column:good_symbol;primaryKey"`
}

func (scopeTestMarketRow) TableName() string { return "market_data" }

type scopeTestEraRow struct {
	EraID    int        `gorm:"column:era_id;primaryKey"`
	Name     string     `gorm:"column:name"`
	PlayerID int        `gorm:"column:player_id"`
	ClosedAt *time.Time `gorm:"column:closed_at"`
}

func (scopeTestEraRow) TableName() string { return "eras" }

// twoEraMarketDB is the shape market_data is actually left in by a universe rollover:
// `universe transition` preserves the prior era's rows, so a dead era's player and its waypoints
// are still present alongside the live era's.
func twoEraMarketDB(t *testing.T, openEra bool) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&scopeTestEraRow{}, &scopeTestMarketRow{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}

	closed := time.Now().UTC().Add(-30 * 24 * time.Hour)
	eras := []scopeTestEraRow{{EraID: 1, Name: "era-1", PlayerID: 1, ClosedAt: &closed}}
	if openEra {
		eras = append(eras, scopeTestEraRow{EraID: 2, Name: "era-2", PlayerID: 7})
	}
	for i := range eras {
		if err := db.Create(&eras[i]).Error; err != nil {
			t.Fatalf("seed era %d: %v", eras[i].EraID, err)
		}
	}

	// The dead era priced X1-AA; the live era prices X1-BB. Two waypoints in the live system so
	// the per-(player, system) dedupe is exercised rather than assumed.
	rows := []scopeTestMarketRow{
		{PlayerID: 1, WaypointSymbol: "X1-AA-D40", GoodSymbol: "ALUMINUM"},
		{PlayerID: 7, WaypointSymbol: "X1-BB-D40", GoodSymbol: "ALUMINUM"},
		{PlayerID: 7, WaypointSymbol: "X1-BB-K87", GoodSymbol: "IRON_ORE"},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed market row %d: %v", i, err)
		}
	}
	return db
}

func scopeKeys(scopes []marketScope) []string {
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		out = append(out, s.playerIDLabel+"|"+s.system)
	}
	sort.Strings(out)
	return out
}

func assertScopes(t *testing.T, got, want []string, msg string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("activeScopes = %v, want %v — %s", got, want, msg)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("activeScopes = %v, want %v — %s", got, want, msg)
		}
	}
}

// Two defects in one assertion (sp-hrko6).
//
// THE CROSS PRODUCT: the old code returned a set of players and a set of systems and the caller
// multiplied them, so {7, X1-AA} — a pair that has never held a single row — was polled with four
// SQL queries per tick and registered its own Prometheus label series.
//
// THE DEAD ERA: market_data is not pruned on a rollover, so player 1 stayed in the label set
// forever. That is not only cost — the market dashboards aggregate with an unqualified
// `sum(market_coverage_fresh) / sum(market_coverage_total)`, so a closed era contributes a
// permanent, never-fresh denominator and drags the coverage panel down for good.
func TestActiveScopes_ExcludesDeadErasAndNeverInventsAPair(t *testing.T) {
	c := NewMarketMetricsCollector(twoEraMarketDB(t, true))

	assertScopes(t, scopeKeys(c.activeScopes()), []string{"7|X1-BB"},
		`a "1|..." entry means a closed era is still emitting series and diluting the coverage panel; `+
			`a "7|X1-AA" entry means the cross product is back, a pair with no rows polled four times per tick`)
}

// FAIL-OPEN: with no open era — a database predating the eras table, or one caught mid-rollover —
// the collector must report a superset rather than go dark. Silent zero series are harder to
// diagnose than noisy ones, and this is observability, not a money guard.
func TestActiveScopes_FallsBackToEveryPlayerWhenNoEraIsOpen(t *testing.T) {
	c := NewMarketMetricsCollector(twoEraMarketDB(t, false))

	assertScopes(t, scopeKeys(c.activeScopes()), []string{"1|X1-AA", "7|X1-BB"},
		"with no open era the collector must report every player rather than emit nothing")
}
