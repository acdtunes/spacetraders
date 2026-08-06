package database

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"gorm.io/gorm"
)

// The two eras. Waypoint symbols are regenerated on a universe reset, so the SAME symbol can
// belong to a dead era and a live one — that recurrence is the whole precondition of the bug.
const (
	deadEraPlayer = 1
	livePlayer    = 7
	sharedPoint   = "X1-BG40-D40"
	sharedGood    = "ALUMINUM"
)

func collisionTestGoods(t *testing.T, purchase, sell int) []market.TradeGood {
	t.Helper()
	supply := "MODERATE"
	good, err := market.NewTradeGood(sharedGood, &supply, nil, purchase, sell, 60, market.TradeType("EXPORT"))
	if err != nil {
		t.Fatalf("NewTradeGood: %v", err)
	}
	return []market.TradeGood{*good}
}

// seedEraPlayers creates the two player rows market_data's foreign key requires: one per era,
// sharing an agent symbol. That sharing is faithful rather than incidental — PlayerModel
// documents agent_symbol as deliberately NOT unique because the same agent is re-registered
// after a universe reset, which is the same reset that regenerates the waypoint symbols this
// test collides.
func seedEraPlayers(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, id := range []int{deadEraPlayer, livePlayer} {
		p := persistence.PlayerModel{
			ID:          id,
			AgentSymbol: "TORWIND",
			Token:       "test-token",
			CreatedAt:   time.Now().UTC(),
		}
		if err := db.Create(&p).Error; err != nil {
			t.Fatalf("seed player %d: %v", id, err)
		}
	}
}

// seedDeadEraRow writes the row a closed era left behind at the shared key. It is inserted
// directly rather than through the repository so the test states the precondition — a row at
// this key owned by someone else — without depending on the write path under test.
func seedDeadEraRow(t *testing.T, db *gorm.DB) {
	t.Helper()
	supply := "SCARCE"
	row := persistence.MarketData{
		PlayerID:       deadEraPlayer,
		WaypointSymbol: sharedPoint,
		GoodSymbol:     sharedGood,
		Supply:         &supply,
		PurchasePrice:  9999,
		SellPrice:      1,
		TradeVolume:    10,
		LastUpdated:    time.Now().UTC().Add(-90 * 24 * time.Hour),
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed dead-era row: %v", err)
	}
}

// THE COLLISION (sp-hdr4p acceptance 2). A dead era's row sits at (waypoint, good). The live
// player scans that market. UpsertMarketData deletes scoped `player_id = ? AND waypoint_symbol
// = ?`, which cannot touch the dead era's row, and then inserts.
//
// While the PRIMARY KEY was (waypoint_symbol, good_symbol), that insert violated it, the whole
// transaction rolled back, and the market could never be cached for the live era — permanently,
// and invisibly, because a market with no rows is indistinguishable from one nobody scouted.
//
// Against unmodified code this fails with a UNIQUE/duplicate-key violation on the upsert.
func TestUpsertMarketData_DeadEraRowAtSameWaypointDoesNotBlockTheLiveScan(t *testing.T) {
	db, err := NewTestConnection()
	if err != nil {
		t.Fatalf("test connection: %v", err)
	}
	seedEraPlayers(t, db)
	seedDeadEraRow(t, db)

	repo := persistence.NewMarketRepository(db)
	upsertErr := repo.UpsertMarketData(context.Background(), livePlayer, sharedPoint, collisionTestGoods(t, 375, 300), time.Now().UTC())
	if upsertErr != nil {
		t.Fatalf("live scan of %s was rejected because a dead era still holds that key: %v\n"+
			"This is the defect: the DELETE is scoped player_id+waypoint_symbol but the PRIMARY KEY is not, "+
			"so the market can never be cached again for as long as the stale row exists.", sharedPoint, upsertErr)
	}

	live, err := repo.GetMarketData(context.Background(), sharedPoint, livePlayer)
	if err != nil {
		t.Fatalf("GetMarketData(live): %v", err)
	}
	if live == nil {
		t.Fatal("the live player's market is unreadable after a successful scan — a nil market is exactly " +
			"what an unscouted waypoint looks like, which is why this failure mode stayed invisible")
	}
	if got := len(live.TradeGoods()); got != 1 {
		t.Fatalf("live market holds %d goods, want 1", got)
	}
	if got := live.TradeGoods()[0].PurchasePrice(); got != 375 {
		t.Fatalf("live purchase price = %d, want 375 (the freshly scanned value, not the dead era's)", got)
	}
}

// Acceptance 4: the fix widens the KEY, and must not widen what a READ can see. The dead era's
// row still exists at the same waypoint and good — it must remain invisible to the live player,
// and the live rows invisible to the dead one. A stale price must be selectable by neither name
// nor ranking.
func TestUpsertMarketData_EraRowsStayInvisibleToEachOtherAfterTheCollisionIsResolved(t *testing.T) {
	db, err := NewTestConnection()
	if err != nil {
		t.Fatalf("test connection: %v", err)
	}
	seedEraPlayers(t, db)
	seedDeadEraRow(t, db)

	repo := persistence.NewMarketRepository(db)
	if err := repo.UpsertMarketData(context.Background(), livePlayer, sharedPoint, collisionTestGoods(t, 375, 300), time.Now().UTC()); err != nil {
		t.Fatalf("live upsert: %v", err)
	}

	live, err := repo.GetMarketData(context.Background(), sharedPoint, livePlayer)
	if err != nil || live == nil {
		t.Fatalf("GetMarketData(live) = %v, %v", live, err)
	}
	if got := live.TradeGoods()[0].PurchasePrice(); got == 9999 {
		t.Fatal("the live player was served the DEAD era's price — read scoping was weakened while the key was widened")
	}

	dead, err := repo.GetMarketData(context.Background(), sharedPoint, deadEraPlayer)
	if err != nil {
		t.Fatalf("GetMarketData(dead): %v", err)
	}
	if dead == nil {
		t.Fatal("the dead era's row was destroyed by the live player's scan — the fix must widen the key, not " +
			"delete another era's history (TransitionEra deliberately retains it)")
	}
	if got := dead.TradeGoods()[0].PurchasePrice(); got != 9999 {
		t.Fatalf("dead-era purchase price = %d, want its own 9999 — the two eras' rows must not have merged", got)
	}
}

// Acceptance 3: with no stale row present, an upsert behaves exactly as before — a re-scan of
// the same waypoint REPLACES that player's rows rather than accumulating them. This is the
// calibration test: a "fix" that made the key so wide that a re-scan appended instead of
// replacing would pass the collision test above and break the cache.
func TestUpsertMarketData_RescanReplacesThePlayersOwnRows(t *testing.T) {
	db, err := NewTestConnection()
	if err != nil {
		t.Fatalf("test connection: %v", err)
	}
	seedEraPlayers(t, db)
	repo := persistence.NewMarketRepository(db)
	ctx := context.Background()

	if err := repo.UpsertMarketData(ctx, livePlayer, sharedPoint, collisionTestGoods(t, 300, 250), time.Now().UTC()); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if err := repo.UpsertMarketData(ctx, livePlayer, sharedPoint, collisionTestGoods(t, 375, 300), time.Now().UTC()); err != nil {
		t.Fatalf("re-scan: %v", err)
	}

	var rows int64
	if err := db.Model(&persistence.MarketData{}).
		Where("player_id = ? AND waypoint_symbol = ? AND good_symbol = ?", livePlayer, sharedPoint, sharedGood).
		Count(&rows).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Fatalf("re-scanning the same waypoint left %d rows for one (player, waypoint, good), want 1 — "+
			"market_data is a cache and the upsert must replace, not accumulate", rows)
	}

	live, err := repo.GetMarketData(ctx, sharedPoint, livePlayer)
	if err != nil || live == nil {
		t.Fatalf("GetMarketData: %v, %v", live, err)
	}
	if got := live.TradeGoods()[0].PurchasePrice(); got != 375 {
		t.Fatalf("purchase price = %d, want the re-scanned 375", got)
	}
}

// The key itself, asserted directly rather than only through behaviour. AutoMigrate builds the
// schema from the struct tags, so this pins that player_id is actually IN the primary key — the
// property every test above depends on, and the one a future tag edit could silently drop.
func TestMarketDataPrimaryKeyIncludesPlayerID(t *testing.T) {
	db, err := NewTestConnection()
	if err != nil {
		t.Fatalf("test connection: %v", err)
	}

	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(&persistence.MarketData{}); err != nil {
		t.Fatalf("parse model: %v", err)
	}

	var keyed []string
	for _, f := range stmt.Schema.PrimaryFields {
		keyed = append(keyed, f.DBName)
	}
	joined := strings.Join(keyed, ",")

	for _, want := range []string{"player_id", "waypoint_symbol", "good_symbol"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("market_data primary key is (%s); it must include %q. The DELETE in UpsertMarketData is "+
				"scoped by player_id, and a key that does not match that scope is the sp-hdr4p bug.", joined, want)
		}
	}
	if len(keyed) != 3 {
		t.Fatalf("market_data primary key is (%s), want exactly the three partition+natural key columns", joined)
	}
}

// The repair must be inert on a database whose key is already correct — it runs on EVERY boot,
// so a non-idempotent repair would re-issue DDL forever. SQLite cannot ALTER a primary key at
// all, which is precisely why the repair is Postgres-only; this pins that it is a clean no-op
// here rather than an error.
func TestRepairMarketDataPrimaryKey_IsInertOnACorrectSchema(t *testing.T) {
	db, err := NewTestConnection()
	if err != nil {
		t.Fatalf("test connection: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := repairMarketDataPrimaryKey(db); err != nil {
			t.Fatalf("repair run %d returned an error on an already-correct schema: %v", i+1, err)
		}
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate must stay clean with the repair wired in: %v", err)
	}
}
