package persistence

import (
	"time"
)

// ScoutPostModel is one desired-state scout post: a per-system
// market-freshness assignment the scout_post_coordinator keeps manned, the way
// the contract fleet coordinator keeps its dedicated fleet working. AssignedHull
// (nullable) is the satellite currently manning the post and TourContainerID the
// worker container scanning it — both persisted so a daemon restart re-adopts the
// same hull onto the same post (RULINGS #2). Kind is "standing" (infinite tour)
// or "sweep_once" (single tour, then auto-removed).
//
// EraID mirrors WaypointModel/GateEdgeModel exactly: reads are era-scoped so a
// universe reset never resurrects dead-era posts. The unique index on
// (player_id, system_symbol) enforces one post per system per player; a re-add in
// a new era reuses the row (Upsert restamps era_id). No players foreign key —
// like the other operational-state rows (spend reservations, tour telemetry),
// player_id is a plain indexed column the reads scope to, and a hard FK would only
// add fixture friction to the coordinator tests that write these rows.
type ScoutPostModel struct {
	ID                     int     `gorm:"column:id;primaryKey;autoIncrement"`
	PlayerID               int     `gorm:"column:player_id;not null;uniqueIndex:idx_scout_posts_player_system,priority:1;index:idx_scout_posts_player"`
	SystemSymbol           string  `gorm:"column:system_symbol;not null;uniqueIndex:idx_scout_posts_player_system,priority:2"`
	FreshnessTargetSeconds int     `gorm:"column:freshness_target_seconds;not null"`
	Kind                   string  `gorm:"column:kind;not null"`
	AssignedHull           *string `gorm:"column:assigned_hull"`
	TourContainerID        *string `gorm:"column:tour_container_id"`
	// RepositionContainerID is the in-flight cross-gate relay jump-routing
	// a satellite toward this post. Nullable — set only while a relay is airborne,
	// cleared when it lands (the next tick mans the post in-system) or dies. GORM
	// AutoMigrate adds the column in place; existing rows read it as NULL → "".
	RepositionContainerID *string `gorm:"column:reposition_container_id"`

	// Hulls is the probe budget N for a multi-probe post: the system is
	// toured by N probes over N disjoint market partitions. Defaults to 1 (single
	// hull, the pre-enry behavior). AutoMigrate adds the column with default 1, so
	// every existing post reads as single-hull. RULINGS #5: a DB value, not a const.
	Hulls int `gorm:"column:hulls;not null;default:1"`

	// MinHulls is the PERMANENT manning FLOOR the freshsizer never sizes the post below
	// (sp-2ci9y). Defaults to 0 (no floor); AutoMigrate adds the column with default 0,
	// so every existing/non-home row reads as unfloored and stays byte-identical.
	// Bootstrap stamps the home post's floor to probe_target. RULINGS #5: a DB value.
	MinHulls int `gorm:"column:min_hulls;not null;default:0"`

	// Dormant is the sensing coordinator's pressure-rotation bit: a dormant
	// post's probe parks in place (its tour sleeps instead of scanning), so
	// shedding under API pressure costs zero API and moves no hull. Defaults to
	// false — every existing post keeps scanning.
	Dormant bool `gorm:"column:dormant;not null;default:false"`

	// HotWaypoints is the JSON-encoded stage-2 circuit of a STANDING post: the
	// system's market waypoints dealing in ≥1 whitelisted good, stamped sorted
	// by the sensing coordinator. NULL/empty ⇒ stage 1 — the tour flies its
	// full circuit — so every pre-existing row keeps full-circuit scanning.
	HotWaypoints *string `gorm:"column:hot_waypoints"`

	// PrimaryPartition is the JSON-encoded frozen market tour of the PRIMARY slot
	// when Hulls>1. NULL/empty ⇒ the primary tours ALL markets (single-hull
	// behavior), so a single-hull row never carries one and stays byte-identical.
	// ExtraSlots is the JSON-encoded slots 1..N-1 (hull, tour/relay container, and
	// each slot's frozen partition). Persisting the partitions is what makes a daemon
	// restart re-adopt each probe onto the SAME partition without a mass re-tour
	// (RULINGS #2) — the reconciler re-partitions ONLY on a hull-budget change.
	PrimaryPartition *string `gorm:"column:primary_partition"`
	ExtraSlots       *string `gorm:"column:extra_slots"`

	// RespawnAttempts and RespawnParkedUntil back the general per-post respawn-loop cap:
	// the consecutive dead-tour respawn count and the backoff-window deadline
	// the reconciler parks a persistently-crashing post under. AutoMigrate adds both in
	// place — respawn_attempts defaults 0 and reposition_parked_until is nullable, so
	// every existing row reads as "never capped, not parked". Persisting them is what
	// makes the cap survive a daemon restart rather than the crash-loop resuming at tick
	// cadence (RULINGS #2).
	RespawnAttempts    int        `gorm:"column:respawn_attempts;not null;default:0"`
	RespawnParkedUntil *time.Time `gorm:"column:respawn_parked_until"`

	EraID     *int      `gorm:"column:era_id;index:idx_scout_posts_era"`
	CreatedAt time.Time `gorm:"column:created_at;not null"`
}

func (ScoutPostModel) TableName() string {
	return "scout_posts"
}

// SensingSystemModel is the per-system screening verdict of the parked-probe
// sensing ledger: has this system been judged worth placing probes
// in, and what did the charting seed cost. Verdict is PENDING (not yet
// screened), IN_SCOPE (deals in ≥1 whitelisted good — place slots here), or
// NO_WHITELIST (screened and rejected). SeedShip/SeedState track the one-off
// charting run that resolved UnchartedCount>0 (DISPATCHED → CHARTING → DONE);
// both nullable because a fully-charted system never needs a seed.
//
// Composite primary key (player_id, system_symbol) makes duplicate screenings
// structurally impossible — a re-screen updates the row in place. EraID mirrors
// ScoutPostModel/ShipyardInventoryModel: planning reads are era-scoped so a
// universe reset never resurrects a dead era's verdicts. No players foreign key
// — like the other operational-state rows, player_id is a plain scoped column.
type SensingSystemModel struct {
	PlayerID       int        `gorm:"primaryKey;column:player_id"`
	SystemSymbol   string     `gorm:"primaryKey;column:system_symbol;size:50"`
	Verdict        string     `gorm:"column:verdict;size:20;not null;default:'PENDING'"` // PENDING|IN_SCOPE|NO_WHITELIST
	ScreenedAt     *time.Time `gorm:"column:screened_at"`
	UnchartedCount int        `gorm:"column:uncharted_count;not null;default:0"`
	// CatalogSyncedAt records that the system's waypoint LIST has actually been
	// swept. NULL means unswept, and that is the guard on the NO_WHITELIST
	// verdict: a system nobody has visited has no waypoint rows, so it screens
	// exactly like one that is charted through and deals in nothing — and that
	// verdict is durable AND makes the system a frontier propagation origin, so
	// a wrong one spreads. Screening records PENDING instead, and expansion
	// sends a seed.
	CatalogSyncedAt *time.Time `gorm:"column:catalog_synced_at"`
	SeedShip        *string    `gorm:"column:seed_ship;size:50"`
	SeedState       *string    `gorm:"column:seed_state;size:20"` // DISPATCHED|CHARTING|DONE
	DepthCredits    int64      `gorm:"column:depth_credits;not null;default:0"`
	EraID           *int       `gorm:"column:era_id;index"`
	UpdatedAt       time.Time  `gorm:"column:updated_at"`
}

func (SensingSystemModel) TableName() string {
	return "sensing_systems"
}

// SensingSlotModel is one probe PLACEMENT in the parked-probe sensing ledger
// (sp-k6v8z) — the durable spine that makes the whole model re-derivable from
// the database after a daemon restart (RULINGS #2). One row per (player,
// waypoint, KIND): the slot we want a probe parked at, and how far along we are.
//
// State is the slot's lifecycle: WANTED (a placement we want) → QUEUED (chosen
// for purchase) → BOUGHT (hull paid for) → IN_TRANSIT (flying to the waypoint)
// → PARKED (on station, scanning). AssignedShip is the hull filling the slot,
// nullable because WANTED/QUEUED slots have no hull yet — that distinction is
// exactly what the probe_cap read counts, so a state alone never implies a
// purchase. SlotKind is MARKET (a market to watch), YARD (a shipyard), or SPARE
// (a parked reserve hull, still a probe we paid for).
//
// WhitelistGoods is the JSON-encoded set of whitelisted goods the waypoint
// deals in (defaulted '[]' so a row is never NULL-parsed), SpreadEWMA the
// smoothed price spread the scans feed, LastScanAt the freshness stamp.
// EraID mirrors SensingSystemModel.
//
// SLOT_KIND IS IN THE PRIMARY KEY, and that is load-bearing rather
// than incidental. A waypoint can be two things at once: a market a probe is
// parked at scanning (MARKET), and a probe-selling yard where a seed is staged
// for purchase (SPARE). Keyed on the waypoint alone those two claims collided,
// and the collision froze expansion outright — the fleet's only two probe yards
// both held MARKET placements, so no SPARE want could ever be written and no
// charting seed could ever be bought.
//
// The consequence for every WRITER: a kind is part of a row's identity, so it is
// IMMUTABLE. Re-declaring a placement under a different kind inserts a second
// row rather than converting the first, and any write that names a waypoint must
// name a kind too or it addresses an ambiguous set. That is not a convention —
// DeleteSlot, TransitionSlot and MarkScanned all take a kind for exactly this
// reason, and it is what replaces the "one row per waypoint" guarantee this key
// gave away.
type SensingSlotModel struct {
	PlayerID       int        `gorm:"primaryKey;column:player_id"`
	WaypointSymbol string     `gorm:"primaryKey;column:waypoint_symbol;size:50"`
	SystemSymbol   string     `gorm:"column:system_symbol;size:50;index;not null"`
	SlotKind       string     `gorm:"primaryKey;column:slot_kind;size:10;not null"` // MARKET|YARD|SPARE
	State          string     `gorm:"column:state;size:12;not null;index"`          // WANTED|QUEUED|BOUGHT|IN_TRANSIT|PARKED
	AssignedShip   *string    `gorm:"column:assigned_ship;size:50;index"`
	PurchaseYard   *string    `gorm:"column:purchase_yard;size:50"`
	WhitelistGoods string     `gorm:"column:whitelist_goods;type:text;not null;default:'[]'"` // JSON array
	SpreadEWMA     float64    `gorm:"column:spread_ewma;not null;default:0"`
	LastScanAt     *time.Time `gorm:"column:last_scan_at"`
	// LastScanAttemptAt is when the scan rotation last took this slot's TURN,
	// whether or not that turn produced any market data. It is the rotation's
	// PACING clock, and it exists because last_scan_at cannot be both that and an
	// honest freshness stamp.
	//
	// The fleet's market-scan budget declines the overwhelming majority of turns
	// — measured at 92% (3,551 declines to 310 scans) — and a decline writes
	// nothing to market_data. Stamping last_scan_at anyway made 78.5% of slots
	// claim data they did not have. But the stamp is ALSO what the rotation paces
	// against: the reconcile rebuilds the whole heap from this table every 30s, so
	// a clock that stops advancing on a decline makes every declined slot read as
	// permanently due and spins the entire rotation at full speed producing
	// nothing. The two answers are needed at once and they disagree 92% of the
	// time, so they are two columns.
	//
	// This one advances on EVERY turn (scanned, declined, or failed); last_scan_at
	// advances only when market data was actually written. Same split, same
	// reasoning, as LastAttemptAt vs updated_at below.
	//
	// NULL MEANS "NEVER ATTEMPTED", and the reader coalesces it to last_scan_at
	// rather than to the zero time — see ParkedSlotViews. AutoMigrate adds the
	// column in place, so every pre-existing row reads NULL on the first tick
	// after deploy, and the coalesce is what makes that tick pace identically to
	// the one before it instead of declaring the whole rotation due at once.
	LastScanAttemptAt *time.Time `gorm:"column:last_scan_attempt_at"`
	DepthCredits      int64      `gorm:"column:depth_credits;not null;default:0"`
	EraID             *int       `gorm:"column:era_id;index"`
	UpdatedAt         time.Time  `gorm:"column:updated_at"`
	// LastAttemptAt is when the placement machine last spent one of a tick's
	// budgets on this slot, or NULL for a slot it has never tried. It is what lets
	// the placement worklist rotate least-recently-attempted first instead of
	// working a fixed alphabetical head forever. AutoMigrate adds the
	// column in place; every existing row reads it as NULL, i.e. never attempted.
	//
	// IT IS DELIBERATELY NOT updated_at, and the difference is the whole point.
	// updated_at moves only on a SUCCESSFUL transition, so a slot whose move fails
	// every tick carries the oldest one — ordering by it would have entrenched a
	// failing head rather than breaking it. Restamping updated_at on failures
	// instead would have been worse: it is the only record of how long a slot has
	// sat in one state, and a stuck placement that reads as freshly updated is
	// invisible. This column answers "when was it last tried", that one answers
	// "when did it last move", and a starving slot is exactly the row where those
	// two must be allowed to disagree.
	//
	// NULL IS MEANINGFUL AND MUST NOT COLLAPSE TO THE ZERO TIME, for the reason
	// ScreenedAt is a pointer too: a never-attempted slot is the case the rotation
	// most needs to reach first, and the zero time would merely sort it there by
	// accident while leaving any reader that dereferences the pointer to panic.
	LastAttemptAt *time.Time `gorm:"column:last_attempt_at"`
}

func (SensingSlotModel) TableName() string {
	return "sensing_slots"
}
