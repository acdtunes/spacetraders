package persistence

import (
	"time"
)

// SystemGraphModel represents the system_graphs table
type SystemGraphModel struct {
	SystemSymbol string    `gorm:"column:system_symbol;primaryKey"`
	GraphData    string    `gorm:"column:graph_data;type:jsonb;not null"` // Use JSONB for PostgreSQL, falls back to TEXT for SQLite
	EraID        *int      `gorm:"column:era_id"`
	CreatedAt    time.Time `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt    time.Time `gorm:"column:updated_at;not null;autoUpdateTime"`
}

func (SystemGraphModel) TableName() string {
	return "system_graphs"
}

// GateEdgeModel is one directed cross-system jump-gate connection — the persisted
// substrate of the gate-graph adjacency store. travel()'s multi-jump BFS
// and the routability-check-before-spend guard both read this table instead of the
// broken single-edge assumption that crashed a laden frigate at the home gate
// (KA42→JP61 is 3 jumps: PA3→UQ16→JP61, not one). GateWaypoint carries the
// CONNECTED system's own gate waypoint (the raw API connection symbol), so an
// uncharted neighbor can be expanded without first charting its system graph.
//
// EraID + SyncedAt mirror WaypointModel exactly: reads are era-scoped
// (eraScopePredicate) so dead-era rows never leak into live routing, and
// SyncedAt (RFC3339) drives the lazy 24h refresh. The (system_symbol,
// connected_system) pair is the primary key — a system's whole edge set is
// REPLACED on each sync (delete-then-insert), so a since-severed connection cannot
// linger and a re-sync also purges any dead-era row for that system.
//
// MARKER ROWS: a row whose ConnectedSystem is "" is NOT an edge — it is the
// persisted negative-result backoff marker for an UNREADABLE system (a frontier gate
// whose live fetch 400s, "no ship present"). At most one per (system, era). Its
// UnreadableSince/AttemptCount carry the backoff state; its "" connected_system is the
// structural sentinel that distinguishes it from a real edge (ExtractSystemSymbol never
// yields "", so an edge's ConnectedSystem is always non-empty). Edges/Adjacency EXCLUDE
// marker rows (connected_system <> ”); UnreadableState/MarkUnreadable read/write them.
type GateEdgeModel struct {
	SystemSymbol    string `gorm:"column:system_symbol;primaryKey"`
	ConnectedSystem string `gorm:"column:connected_system;primaryKey"`
	GateWaypoint    string `gorm:"column:gate_waypoint;not null"`
	EraID           *int   `gorm:"column:era_id;index:idx_gate_edges_era"`
	SyncedAt        string `gorm:"column:synced_at"` // ISO timestamp string
	// UnderConstruction records whether the CONNECTED system's own jump gate was
	// still being built at sync time. The routing BFS never traverses an
	// under-construction edge, and such an edge refreshes on a SHORTER TTL than a
	// healthy one so a completed build is noticed within the same era.
	UnderConstruction bool `gorm:"column:under_construction;not null;default:false"`
	// UnreadableSince is the RFC3339 timestamp of the LAST failed live gate probe, set
	// only on a marker row (connected_system = ""). Empty on every real edge row. With
	// AttemptCount it is the persisted negative-result cache: an unreadable
	// gate is not re-probed every 30s tick — the service backs it off 5m→30m→2h.
	// Persisted, not in-memory, so a restart resumes the backoff (RULINGS #2).
	UnreadableSince string `gorm:"column:unreadable_since"`
	// AttemptCount is the consecutive-failed-probe count on a marker row; it drives the
	// backoff schedule. 0 on every real edge row.
	AttemptCount int `gorm:"column:attempt_count;not null;default:0"`
}

func (GateEdgeModel) TableName() string {
	return "gate_edges"
}

// SystemCoordModel is one galaxy-level system coordinate snapshot row. The
// daemon owns only the DDL (AutoMigrate); rows are written LAZILY by the
// visualizer server from the live GET /systems/{symbol} API while building
// /api/flows/topology, so the galaxy view draws REAL positions instead of a
// synthesized force layout. Era-scoped like GateEdgeModel: a universe reset
// regenerates symbols, and the (era_id, symbol) key keeps a dead era's
// coordinates from colliding with a recurring symbol.
type SystemCoordModel struct {
	EraID     int     `gorm:"column:era_id;primaryKey"`
	Symbol    string  `gorm:"column:symbol;primaryKey;size:32"`
	X         float64 `gorm:"column:x;not null"`
	Y         float64 `gorm:"column:y;not null"`
	FetchedAt string  `gorm:"column:fetched_at"` // RFC3339, mirrors GateEdgeModel.SyncedAt
}

func (SystemCoordModel) TableName() string {
	return "system_coords"
}

// TourLegTelemetryModel is one planned-vs-realized record for a single trade at a
// single leg of a multi-hop trade tour. The tour_run executor writes
// one row per executed (or explicitly skipped) trade: the planner's projection
// (PlannedUnits/PlannedUnitPrice) alongside what the market actually gave
// (RealizedUnits/RealizedUnitPrice), plus the two timestamps that bracket EXECUTION
// (PlannedAt/RealizedAt). These rows feed the graduation-gate report (median
// |planned−realized|/planned price error — the gate metric that proves the model, not
// just profit) and future model recalibration.
//
// PlannedAt IS NOT PART OF THE PROJECTION — this comment used to list it as such, and
// that is exactly the misreading it caused (sp-fpgl2). See the field's own doc below
// before using it for anything.
//
// Follows the SpendReservationModel idiom: NO players foreign key. player_id is a
// plain indexed column the report scopes its reads to; tour_id (the container id)
// groups a tour's legs. Rows are durable history (unlike the ephemeral spend
// reservations) but referential integrity to players buys nothing here and a hard FK
// would only add fixture friction to the executor tests that write these rows.
type TourLegTelemetryModel struct {
	ID         uint   `gorm:"column:id;primaryKey;autoIncrement"`
	TourID     string `gorm:"column:tour_id;not null;index:idx_tour_leg_telemetry_tour"`
	ShipSymbol string `gorm:"column:ship_symbol;not null"`

	// Engine is WHICH execution path wrote this row: solver, lookback or liquidation
	// (trading.LegEngine). It is the attribution column a SQL reader filters on —
	// `WHERE engine = 'solver'` for planner accuracy — and exists so that reader need not
	// recognise an engine from the shape of its data.
	//
	// It is NOT a second encoding of LegIndex. LegIndex stays the visualizer's ordering
	// sentinel and keeps its exact meaning; Engine is stamped independently by the call
	// site, so the two are cross-checked rather than derived from one another. Rows
	// written before this column existed were backfilled once from the LegIndex class
	// (see database.AutoMigrate), which the production data supports without exception.
	//
	// AutoMigrate adds the column with an empty default; the backfill fills it in the same
	// startup. Deliberately NOT `not null` — a NOT NULL add against a populated table is
	// the one AutoMigrate shape that can fail on production Postgres, and the repository
	// already refuses to write an empty engine.
	Engine string `gorm:"column:engine;index:idx_tour_leg_telemetry_engine"`

	// LegIndex is the leg's 0..N position in the solver's plan — EXCEPT for two sentinels
	// that encode which kind of leg this is, and it is NOT free to change.
	//
	// trading.LookbackLegIndex (-1) marks a look-back manifest buy: an opportunistic
	// pre-jump load at the reposition seam, whose plan basis is a CACHED SourceAsk rather
	// than the solver's projection. Indices at or above trading.LiquidationLegIndexBase
	// (1_000_000) mark a distress liquidation, which carries no plan basis at all.
	//
	// PREFER THE ENGINE COLUMN for "which path made this leg" — that is what it is for, and
	// it is the only form of the question a SQL reader can ask. These sentinels remain the
	// ORDERING contract (their sign decides which way the visualizer draws a hop) and are
	// still the classification the Go-side graduation gate uses, but as a way to identify
	// an engine they were a magic number half-hidden in an application package.
	//
	// This column WAS informational — the netting readers group by good and tour and never
	// by leg_index — and said so until sp-fpgl2 made it the PLAN-BASIS DISCRIMINATOR. It
	// now decides both the basis label on the drift metric and which population the sp-1ek0
	// graduation gate grades, because look-back legs are compared against the very ask their
	// buy was gated to and so converge on 0% error: pooled with solver legs they dragged the
	// gate's reported median from 0.543% to 0.309%, a figure describing neither. Read it
	// through trading.IsLookbackManifestLeg rather than comparing the literal.
	LegIndex          int    `gorm:"column:leg_index;not null"`
	Waypoint          string `gorm:"column:waypoint;not null"`
	Good              string `gorm:"column:good;not null"`
	IsBuy             bool   `gorm:"column:is_buy"`
	PlannedUnits      int    `gorm:"column:planned_units"`
	RealizedUnits     int    `gorm:"column:realized_units"`
	PlannedUnitPrice  int    `gorm:"column:planned_unit_price"`
	RealizedUnitPrice int    `gorm:"column:realized_unit_price"`

	// PlannedAt is when EXECUTION OF THIS LEG STARTED — not when the plan was made. The
	// name is a historical misnomer and the value is deliberate; do not "fix" it.
	//
	// The executor stamps it immediately before the trade, so it sits a median 2 seconds
	// before RealizedAt (p90 7s). PLAN-VS-REALIZED TIME IS THEREFORE NOT MEASURABLE FROM
	// THIS TABLE, and reading the two columns as a plan-to-execution latency yields a
	// tautology — sp-fpgl2 was raised on exactly that reading. Plan STALENESS would need a
	// separate solve timestamp, which nothing records today.
	//
	// WHY IT MUST KEEP MEANING EXECUTION-START. MedianTourRate takes min(PlannedAt) as a
	// tour's span START and divides realized net by that span (see tour_rate.go legGroup).
	// That rate is β, and run_tour_coordinator_rate_floor.senseRateFloor MAY RELOCATE a
	// hull earning below a fraction of it. Re-stamping this column with a true solve time
	// would move every span start earlier, deflate $/hr fleet-wide, and trip that trigger
	// on a measurement artifact; it would also collapse all of a plan's legs onto one
	// instant, destroying the incremental per-leg spread the window logic relies on (25 of
	// 27 live tours spread their planned_at by more than a minute). The freshness sizer and
	// ListByPlayer's window filter read it the same way.
	//
	// PlannedUnitPrice, by contrast, IS genuinely solve-time: it is the planner's
	// ExpectedUnitPrice, written once from the routing response and never re-priced. The
	// price comparison is honest even though the timestamps cannot support one.
	//
	// Mirrors trading.TourLegTelemetry.PlannedAt, the domain DTO this row maps to; keep the
	// two in step.
	PlannedAt time.Time `gorm:"column:planned_at"`

	// RealizedAt is when the trade completed. A planned-then-SKIPPED leg leaves it ZERO
	// alongside RealizedUnits=0 — the encoding for "this did not happen". Readers must
	// exclude those rows rather than average them: a zero timestamp is not an early one,
	// and /flows/lanes already filters on realized_at for exactly this reason.
	RealizedAt time.Time `gorm:"column:realized_at"`

	PlayerID int `gorm:"column:player_id;not null;index:idx_tour_leg_telemetry_player"`
}

func (TourLegTelemetryModel) TableName() string {
	return "tour_leg_telemetry"
}

// JumpTollSampleModel is one MEASURED gate hop: what the jump actually cost the hull that
// flew it. The cross-system travel path writes one row per hop, bracketing from the jump
// dispatch to the moment the hull is action-ready again.
//
// IT EXISTS BECAUSE NOTHING ELSE RECORDS THE DURATION. The ledger's JUMP row carries the
// gate FEE, and the ships row carries only the CURRENT cooldown expiry — overwritten by the
// next hop. Neither preserves how long a hop took, so the tour solver's per-hop travel term
// could only ever be a constant fitted offline (trading.EstimatePerHopTollSeconds recomputes
// it from these rows). wait_seconds is the ECONOMIC cost — the interval over which the hull
// earned nothing; cooldown_seconds is what the API charged, kept as the hop's distance
// signal. Follows the TourLegTelemetryModel idiom: no players foreign key, durable history,
// born from AutoMigrate.
type JumpTollSampleModel struct {
	ID              uint      `gorm:"column:id;primaryKey;autoIncrement"`
	ShipSymbol      string    `gorm:"column:ship_symbol;not null"`
	FromSystem      string    `gorm:"column:from_system;not null"`
	ToSystem        string    `gorm:"column:to_system;not null"`
	WaitSeconds     int       `gorm:"column:wait_seconds;not null"`
	CooldownSeconds int       `gorm:"column:cooldown_seconds;not null"`
	PlayerID        int       `gorm:"column:player_id;not null;index:idx_jump_toll_samples_player"`
	RecordedAt      time.Time `gorm:"column:recorded_at;not null;index:idx_jump_toll_samples_recorded"`
}

func (JumpTollSampleModel) TableName() string {
	return "jump_toll_samples"
}
