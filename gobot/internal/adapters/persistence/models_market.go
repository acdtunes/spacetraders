package persistence

import (
	"time"
)

// MarketData represents the market_data table: one row per
// (player, waypoint, good). It is a cache — UpsertMarketData deletes and re-inserts a whole
// waypoint on every scan, and a missing row fails closed as "not scanned yet".
//
// PRIMARY KEY is (player_id, waypoint_symbol, good_symbol), and player_id leads it
// deliberately (sp-hdr4p). The table is player-partitioned in every other respect — every read
// filters player_id = ?, and UpsertMarketData's DELETE is scoped
// `player_id = ? AND waypoint_symbol = ?` — but the key used to be (waypoint_symbol,
// good_symbol) alone. That disagreement between the DELETE's scope and the key's scope WAS a
// bug, not a detail: waypoint symbols are regenerated on a universe reset and can recur, so a
// dead era's row could occupy the key a live scan needed. The player-scoped DELETE could not
// remove it, the insert violated the key, the whole transaction rolled back, and that market
// could never be cached again — permanently, and invisibly, since a market with no rows is
// indistinguishable from one nobody has scouted.
//
// Same shape as ShipyardInventoryModel below, whose key is (player_id, waypoint_symbol,
// ship_type). Note that era_id is NOT the partition here: it is player_id that scopes the
// DELETE and every read, so it is player_id that must be in the key for the two to agree.
type MarketData struct {
	PlayerID       int          `gorm:"primaryKey;index;not null"`
	WaypointSymbol string       `gorm:"primaryKey;size:255;not null"`
	GoodSymbol     string       `gorm:"primaryKey;size:100;not null"`
	Supply         *string      `gorm:"size:50"`
	Activity       *string      `gorm:"size:50"`
	PurchasePrice  int          `gorm:"not null"`
	SellPrice      int          `gorm:"not null"`
	TradeVolume    int          `gorm:"not null"`
	TradeType      *string      `gorm:"size:32"` // EXPORT, IMPORT, or EXCHANGE
	LastUpdated    time.Time    `gorm:"index;not null"`
	Player         *PlayerModel `gorm:"foreignKey:PlayerID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
}

func (MarketData) TableName() string {
	return "market_data"
}

// TransactionModel represents the transactions table
type TransactionModel struct {
	ID                string       `gorm:"column:id;primaryKey;size:36;not null"`
	PlayerID          int          `gorm:"column:player_id;index:idx_player_timestamp;not null"`
	Player            *PlayerModel `gorm:"foreignKey:PlayerID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
	Timestamp         time.Time    `gorm:"column:timestamp;index:idx_player_timestamp;not null"`
	TransactionType   string       `gorm:"column:transaction_type;index:idx_type;size:50;not null"`
	Category          string       `gorm:"column:category;size:50;not null"`
	Amount            int          `gorm:"column:amount;not null"` // Positive for income, negative for expenses
	BalanceBefore     int          `gorm:"column:balance_before;not null"`
	BalanceAfter      int          `gorm:"column:balance_after;not null"`
	Description       string       `gorm:"column:description;type:text"`
	Metadata          string       `gorm:"column:metadata;type:jsonb"`                           // JSON metadata
	RelatedEntityType string       `gorm:"column:related_entity_type;index:idx_related;size:50"` // e.g., "contract", "factory"
	RelatedEntityID   string       `gorm:"column:related_entity_id;index:idx_related;size:100"`  // ID of related entity
	OperationType     string       `gorm:"column:operation_type;size:50"`                        // e.g., "contract", "arbitrage", "rebalancing", "factory"
	CreatedAt         time.Time    `gorm:"column:created_at;not null;autoCreateTime"`
}

func (TransactionModel) TableName() string {
	return "transactions"
}

// MarketPriceHistoryModel represents the market_price_history table
type MarketPriceHistoryModel struct {
	ID             int          `gorm:"column:id;primaryKey;autoIncrement"`
	WaypointSymbol string       `gorm:"column:waypoint_symbol;size:50;not null;index:idx_market_history_waypoint_good_time"`
	GoodSymbol     string       `gorm:"column:good_symbol;size:100;not null;index:idx_market_history_waypoint_good_time,idx_market_history_good_time"`
	PlayerID       int          `gorm:"column:player_id;not null;index:idx_market_history_player"`
	Player         *PlayerModel `gorm:"foreignKey:PlayerID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
	PurchasePrice  int          `gorm:"column:purchase_price;not null"`
	SellPrice      int          `gorm:"column:sell_price;not null"`
	Supply         *string      `gorm:"column:supply;size:20"`
	Activity       *string      `gorm:"column:activity;size:20"`
	TradeVolume    int          `gorm:"column:trade_volume;not null"`
	RecordedAt     time.Time    `gorm:"column:recorded_at;not null;default:CURRENT_TIMESTAMP;index:idx_market_history_waypoint_good_time,idx_market_history_good_time,idx_market_history_recorded_at"`
}

func (MarketPriceHistoryModel) TableName() string {
	return "market_price_history"
}

// MarketAbsorptionLedgerModel is one outstanding claim on a market's depth — the
// shared-state substrate of the cross-engine absorption ledger. Five
// engines (tours, arb-run, idle-arb, trade-route circuits, pre-positioning) all
// absorb the SAME (waypoint, good, side) depth with no cross-container signal but
// the market cache, which only reflects EXECUTED trades seconds later. This table
// carries the two invisible windows: PLANNED rows (in-flight intent — a leg
// dispatched but not yet landed, so the cache still quotes pre-absorption prices)
// and EXECUTED rows (the recovery shadow — depth a completed dump still occupies
// while it regrows on the model's fitted per-tier half-life). A reader nets the
// decayed outstanding against a market's depth so nobody, including the absorber's
// own next plan, steps into a hole the model says has not regrown (the lane mutex
// + flat hold are the tactical patch this ledger generalizes cross-engine).
//
// Deliberately NO players foreign key and NO era_id (the SpendReservationModel
// idiom): these are ephemeral operational rows living minutes (a PLANNED
// leg) to hours (an EXECUTED shadow, hard-capped at 12h), so
// referential integrity buys nothing and an era reset kills the owning containers
// (PLANNED rows swept by dead-container reclaim) while EXECUTED rows age out on
// their hard cap and key on (waypoint, good) quotes that reset anyway. player_id +
// (waypoint_symbol, good_symbol, side) is the composite the outstanding query
// scopes to; container_id is indexed for dead-container reclaim and the arb
// container's convert-at-sale; expires_at is indexed for the read filter and sweep.
//
// TierAtWrite is the sink good's activity (WEAK/GROWING/STRONG/RESTRICTED) stamped
// at the EXECUTED write; readers resolve the recovery half-life from the fitted
// artifact. UNTAGGED sinks (empty activity) get NO EXECUTED shadow at all
// (the depth model cannot price what it has not fit — a shadow
// there is either wrong or effectively eternal). TrancheSize is the sink good's
// trade_volume at write, so a reader can size the 50%-of-a-tranche recovery floor
// without a live market lookup. QuotedPrice is telemetry only.
type MarketAbsorptionLedgerModel struct {
	ID          string `gorm:"column:id;primaryKey;not null"`
	PlayerID    int    `gorm:"column:player_id;not null;index:idx_absorption_player_key,priority:1"`
	ContainerID string `gorm:"column:container_id;not null;index:idx_absorption_container"`
	Engine      string `gorm:"column:engine;not null"` // tour | arb | idle-arb — telemetry + reclaim attribution
	Waypoint    string `gorm:"column:waypoint_symbol;not null;index:idx_absorption_player_key,priority:2"`
	Good        string `gorm:"column:good_symbol;not null;index:idx_absorption_player_key,priority:3"`
	Side        string `gorm:"column:side;not null;index:idx_absorption_player_key,priority:4"` // sell | buy
	State       string `gorm:"column:state;not null"`                                           // PLANNED | EXECUTED
	Units       int    `gorm:"column:units;not null"`                                           // planned absorption / realized absorbed units
	TrancheSize int    `gorm:"column:tranche_size;not null;default:0"`                          // sink trade_volume at write (recovery-floor sizing)
	TierAtWrite string `gorm:"column:tier_at_write;not null;default:''"`                        // activity tier; readers resolve half-life from the artifact
	QuotedPrice int    `gorm:"column:quoted_price;not null;default:0"`                          // telemetry only
	// CreatedAt is set on the PLANNED insert; ExecutedAt is stamped when a leg's
	// sale converts the row to EXECUTED (nil while PLANNED). ExpiresAt is the
	// lifecycle bound the sweep and the read filter both use: a PLANNED row's
	// per-plan TTL (2× projected flight + slack) or an EXECUTED row's 12h hard cap.
	CreatedAt  time.Time  `gorm:"column:created_at;not null"`
	ExecutedAt *time.Time `gorm:"column:executed_at"`
	ExpiresAt  time.Time  `gorm:"column:expires_at;not null;index:idx_absorption_expires"`
}

func (MarketAbsorptionLedgerModel) TableName() string {
	return "market_absorption_ledger"
}

// ShipyardInventoryModel is one scanned shipyard listing fact: at
// last_scanned, the (player, waypoint) shipyard offered ship_type at
// purchase_price with the listing's supply tier. Written by the scout tour's
// piggybacked shipyard scan (ReplaceScan swaps a waypoint's whole row set —
// the market_data delete-then-insert idiom — so re-scans refresh price and
// last_scanned without duplicate rows, and a delisted type disappears). Read
// by the reachable-yard ranking that feeds the fleet autosizer's heavy-hull
// yard-price signal.
//
// EraID mirrors GateEdgeModel/ScoutPostModel: reads are era-scoped
// (eraScopePredicate) so a universe reset never leaks dead-era yards into a
// live buy signal; ReplaceScan purges the waypoint's rows across ALL eras
// before inserting, so dead-era rows self-clean on re-scan. Composite primary
// key (player_id, waypoint_symbol, ship_type) makes duplicates structurally
// impossible. PurchasePrice 0 = type listed but unpriced at scan time (proves
// availability, never feeds a price guard). No players foreign key — like the
// other operational-state rows, player_id is a plain scoped column. Unlike
// most cache tables this one IS CREATE'd by migration 041, so the column-drift
// gate holds its model and migration in lockstep.
type ShipyardInventoryModel struct {
	PlayerID       int       `gorm:"column:player_id;primaryKey"`
	SystemSymbol   string    `gorm:"column:system_symbol;not null;index:idx_shipyard_inventory_system"`
	WaypointSymbol string    `gorm:"column:waypoint_symbol;primaryKey"`
	ShipType       string    `gorm:"column:ship_type;primaryKey"`
	PurchasePrice  int       `gorm:"column:purchase_price;not null;default:0"`
	Supply         string    `gorm:"column:supply;not null;default:''"`
	LastScanned    time.Time `gorm:"column:last_scanned;not null"`
	EraID          *int      `gorm:"column:era_id;index:idx_shipyard_inventory_era"`
}

func (ShipyardInventoryModel) TableName() string {
	return "shipyard_inventory"
}
