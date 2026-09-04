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

	// The dead era priced X1-AA; the live era prices X1-BB.
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

func assertPlayers(t *testing.T, got, want []int, msg string) {
	t.Helper()
	sort.Ints(got)
	if len(got) != len(want) {
		t.Fatalf("playersToPoll = %v, want %v — %s", got, want, msg)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("playersToPoll = %v, want %v — %s", got, want, msg)
		}
	}
}

// TestPlayersToPoll_ExcludesDeadErasAndReturnsOnlyTheOpenEraPlayer pins the era-scoping rule
// sp-hrko6 established for the old per-scope enumeration and sp-3hdsk.4 carried into the
// single-query redesign: player 1's era is CLOSED, so it must not be polled even though its
// market_data rows are still on disk (a universe rollover does not prune them) — only player 7,
// the OPEN era, comes back.
func TestPlayersToPoll_ExcludesDeadErasAndReturnsOnlyTheOpenEraPlayer(t *testing.T) {
	c := NewMarketMetricsCollector(twoEraMarketDB(t, true))

	assertPlayers(t, c.playersToPoll(), []int{7}, "a closed era's player must not be polled")
}

// TestPlayersToPoll_FallsBackToEveryPlayerWhenNoEraIsOpen pins the FAIL-OPEN rule: with no open
// era resolvable (a database predating the eras table, or one caught mid-rollover), the collector
// must report every player market_data holds rather than go dark. This is observability, not a
// money guard — silent zero series are harder to diagnose than noisy ones.
func TestPlayersToPoll_FallsBackToEveryPlayerWhenNoEraIsOpen(t *testing.T) {
	c := NewMarketMetricsCollector(twoEraMarketDB(t, false))

	assertPlayers(t, c.playersToPoll(), []int{1, 7},
		"with no open era the collector must report every player rather than emit nothing")
}
