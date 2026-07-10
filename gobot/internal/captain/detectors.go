package watchkeeper

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

type DetectorConfig struct {
	PlayerID          int
	ShipIdle          time.Duration
	StaleHeartbeat    time.Duration
	CreditsThresholds []int
	LastCredits       int // credits at the previous poll; 0 disables crossing detection
	// CurrentCreditsValue is this poll's credits, supplied by the supervisor
	// (sp-sk68 D4) so the crossing detector and the wake gate evaluate the
	// SAME number and the detector has no independent DB failure mode.
	CurrentCreditsValue int

	IncomeStall     time.Duration // 0 disables income-stall detection
	StreamDown      time.Duration // 0 disables stream-down detection
	ExpectedStreams []string      // container-type prefixes expected to be RUNNING; empty disables

	// FactoryIncomeStall (sp-7vos) is the per-goods-factory zero-income window,
	// kept SEPARATE from IncomeStall on purpose (RULINGS #5). The aggregate/
	// per-engine IncomeStall window (2h) is bounded below by lumpy CONTRACT
	// payouts; goods factories are a different earner, so their window is its
	// own knob and can be tuned without loosening contract detection. <= 0
	// disables the per-factory detector. Because a factory's sale cadence is
	// itself lumpy — it sells a batch, then spends an hour-plus reacquiring and
	// re-manufacturing inputs before the next batch (observed healthy gaps reach
	// ~2h) — this must stay comfortably above the normal inter-batch gap or it
	// cries wolf; hence a conservative default (see defaultFactoryIncomeStall).
	FactoryIncomeStall time.Duration

	// Crash-loop detection (sp-no9i): a single container.crashed is self-healing
	// (auto-restart+resume) and deferred; a container that dies CrashLoopThreshold
	// times within CrashLoopWindow is a genuine loop worth an interrupt. Either
	// field <= 0 disables the detector.
	CrashLoopWindow    time.Duration
	CrashLoopThreshold int

	// RegimeTripwires (sp-zlfv): captain-declared price tripwires, loaded
	// fresh each tick from RegimePolicy. Empty disables the price-regime
	// detector entirely — no config means no scan.
	RegimeTripwires []RegimeTripwire

	// PinnedHullContainerless (sp-v63s watchdog): how long a fleet-dedicated hull
	// may sit with no running container before it fires an interrupt-class
	// hull.containerless event. <= 0 disables the detector.
	PinnedHullContainerless time.Duration

	// StandingCoordinatorFleets (sp-jetm) parametrizes which DedicatedFleet tags
	// are pool-managed by a standing coordinator container rather than pinned
	// 1:1 to a single always-running container (RULINGS #5). A hull pinned to
	// one of these fleets legitimately sits containerless BETWEEN claims — the
	// coordinator owns the pool and its own loss modes are covered by
	// income-stall detection — so detectContainerlessPinnedHulls exempts it
	// while that fleet's coordinator has a RUNNING container. Empty disables
	// the exemption entirely (every dedicated hull is treated as 1:1-pinned,
	// the original sp-v63s behavior). This cannot be derived from the
	// containers table: the coordinator's launch config carries no fleet-name
	// field (see run_fleet_coordinator.go's dedicatedFleetContract, an
	// unexported Go constant, not persisted data) — so the association is
	// asserted here as the well-known standing-coordinator fleet(s), the same
	// shape as incomeEngines below.
	StandingCoordinatorFleets []StandingCoordinatorFleet
}

// StandingCoordinatorFleet pairs a Ship.DedicatedFleet() tag with the
// container_type of the standing coordinator that pools it (sp-jetm).
type StandingCoordinatorFleet struct {
	Fleet         string // ship.DedicatedFleet() value this exemption covers
	ContainerType string // container_type of the fleet's pool-managing coordinator
}

// Crash-loop defaults wired by the supervisor until CaptainConfig grows tunable
// fields (follow-up bead). Conservative: three unrecoverable deaths of one
// container inside 30 minutes is a genuine loop, not restart noise (sp-no9i).
const (
	defaultCrashLoopWindow    = 30 * time.Minute
	defaultCrashLoopThreshold = 3
)

// defaultPinnedHullContainerless is the sp-v63s watchdog threshold, wired by the
// supervisor until CaptainConfig grows a tunable field (follow-up bead, mirrors the
// crash-loop defaults above). Conservative: a normal daemon redeploy re-adopts a
// dedicated hull's container within seconds, so five containerless minutes is well
// past churn and squarely an anomaly worth an interrupt.
const defaultPinnedHullContainerless = 5 * time.Minute

// defaultStandingCoordinatorFleets is the sp-jetm exemption list, wired by the
// supervisor until CaptainConfig grows a tunable field (follow-up bead, mirrors
// the crash-loop/pinned-hull defaults above). "contract" is the one fleet with a
// pooling standing coordinator today — CONTRACT_FLEET_COORDINATOR, matching
// dedicatedFleetContract in run_fleet_coordinator.go. Tour/trade pins are
// deliberately absent: those hulls run one dedicated container each with no
// pool, so a containerless tour/trade hull stays exactly the anomaly the
// watchdog was built to catch.
var defaultStandingCoordinatorFleets = []StandingCoordinatorFleet{
	{Fleet: "contract", ContainerType: "CONTRACT_FLEET_COORDINATOR"},
}

// defaultFactoryIncomeStall is the sp-7vos per-factory window, wired by the
// supervisor until CaptainConfig grows a tunable field (follow-up bead, mirrors
// the crash-loop/pinned-hull defaults above). Calibrated against observed live
// cadence: productive goods factories go quiet for up to ~2h between
// manufacture-and-sell batches, so 3h leaves margin above the normal gap while
// still catching a factory that is RUNNING yet has genuinely died (zero income
// for three hours is not a slow cycle). Deliberately longer than IncomeStall's
// 2h — the per-factory signal trades detection latency for a low false-positive
// rate, the failure mode this detector must avoid.
const defaultFactoryIncomeStall = 3 * time.Hour

// RunDetectors writes synthetic strategic events for conditions that are
// state (not daemon events): stale heartbeats, idle ships, credit crossings.
// Dedup: an event is skipped while an unprocessed twin exists.
func RunDetectors(ctx context.Context, db *gorm.DB, store captain.EventStore, cfg DetectorConfig, now time.Time) error {
	if err := detectStaleHeartbeats(ctx, db, store, cfg, now); err != nil {
		return err
	}
	if err := detectIdleShips(ctx, db, store, cfg, now); err != nil {
		return err
	}
	if err := detectContainerlessPinnedHulls(ctx, db, store, cfg, now); err != nil {
		return err
	}
	if err := detectIncomeStall(ctx, db, store, cfg, now); err != nil {
		return err
	}
	if err := detectEngineIncomeStall(ctx, db, store, cfg, now); err != nil {
		return err
	}
	if err := detectFactoryIncomeStall(ctx, db, store, cfg, now); err != nil {
		return err
	}
	if err := detectStreamDown(ctx, db, store, cfg, now); err != nil {
		return err
	}
	if err := detectCrashLoops(ctx, db, store, cfg, now); err != nil {
		return err
	}
	if err := detectRegimeShift(ctx, db, store, cfg, now); err != nil {
		return err
	}
	return detectCreditsCrossing(ctx, store, cfg)
}

func detectStaleHeartbeats(ctx context.Context, db *gorm.DB, store captain.EventStore, cfg DetectorConfig, now time.Time) error {
	cutoff := now.Add(-cfg.StaleHeartbeat)
	var stale []persistence.ContainerModel
	if err := db.WithContext(ctx).
		Where("player_id = ? AND status = ? AND heartbeat_at IS NOT NULL AND heartbeat_at < ?",
			cfg.PlayerID, "RUNNING", cutoff).
		Find(&stale).Error; err != nil {
		return err
	}
	if len(stale) == 0 {
		return nil
	}
	// A slow scout mid-transit legitimately stops heart-beating while the leg
	// runs; its ADVANCING position (nav_status IN_TRANSIT) is proof it is alive.
	// Exempt any stale container whose ship is in transit — a FROZEN position
	// (not in transit) plus a stale heartbeat is the real death signal (sp-no9i).
	// Load the in-transit ship symbols once and match them against each
	// container's config (same quoted-symbol convention as detectIdleShips).
	inTransit, err := inTransitShipSymbols(ctx, db, cfg.PlayerID)
	if err != nil {
		return err
	}
	for _, c := range stale {
		if configReferencesAny(c.Config, inTransit) {
			continue
		}
		// Staleness is a persistent state, not an edge: cooldown on ANY recent
		// heartbeat_lost event (processed or not) prevents a session-burn loop
		// where each acked event is re-emitted — and, being interrupt-class,
		// re-wakes the captain — on the next poll (mirrors detectIdleShips).
		recent, err := store.HasSince(ctx, cfg.PlayerID, captain.EventHeartbeatLost, c.ID, now.Add(-cfg.StaleHeartbeat))
		if err != nil || recent {
			continue
		}
		_ = store.Record(ctx, &captain.Event{
			Type: captain.EventHeartbeatLost, Ship: c.ID, PlayerID: cfg.PlayerID,
			Payload: fmt.Sprintf(`{"container_id":%q,"command_type":%q,"last_heartbeat":%q}`,
				c.ID, c.CommandType, c.HeartbeatAt.UTC().Format(time.RFC3339)),
		})
	}
	return nil
}

// inTransitShipSymbols returns the symbols of the player's ships whose position
// is advancing (nav_status IN_TRANSIT). Used to exempt their worker containers
// from stale-heartbeat detection (sp-no9i).
func inTransitShipSymbols(ctx context.Context, db *gorm.DB, playerID int) ([]string, error) {
	var ships []persistence.ShipModel
	if err := db.WithContext(ctx).
		Select("ship_symbol").
		Where("player_id = ? AND nav_status = ?", playerID, "IN_TRANSIT").
		Find(&ships).Error; err != nil {
		return nil, err
	}
	symbols := make([]string, 0, len(ships))
	for _, s := range ships {
		symbols = append(symbols, s.ShipSymbol)
	}
	return symbols, nil
}

// configReferencesAny reports whether a container's config JSON references any
// of the given ship symbols, matching the quoted symbol the same way
// detectIdleShips joins containers to ships (config stores "...":"SYMBOL").
func configReferencesAny(config string, shipSymbols []string) bool {
	for _, sym := range shipSymbols {
		if sym != "" && strings.Contains(config, `"`+sym+`"`) {
			return true
		}
	}
	return false
}

func detectIdleShips(ctx context.Context, db *gorm.DB, store captain.EventStore, cfg DetectorConfig, now time.Time) error {
	// A ship is idle if it is not IN_TRANSIT and no RUNNING container's config
	// references it. Container config is JSON text; a LIKE match on the quoted
	// symbol is the pragmatic join (config stores "ship_symbol":"X").
	var ships []persistence.ShipModel
	if err := db.WithContext(ctx).
		Where("player_id = ? AND nav_status != ?", cfg.PlayerID, "IN_TRANSIT").
		Find(&ships).Error; err != nil {
		return err
	}
	for _, s := range ships {
		var busy int64
		if err := db.WithContext(ctx).Model(&persistence.ContainerModel{}).
			Where("player_id = ? AND status = ? AND config LIKE ?",
				cfg.PlayerID, "RUNNING", "%\""+s.ShipSymbol+"\"%").
			Count(&busy).Error; err != nil {
			return err
		}
		if busy > 0 {
			continue
		}
		// Idle is a persistent state, not an edge: cooldown on ANY recent
		// idle event (processed or not) prevents a session-burn loop where
		// each processed event is re-emitted on the next poll.
		recent, err := store.HasSince(ctx, cfg.PlayerID, captain.EventShipIdle, s.ShipSymbol, now.Add(-cfg.ShipIdle))
		if err != nil || recent {
			continue
		}
		_ = store.Record(ctx, &captain.Event{
			Type: captain.EventShipIdle, Ship: s.ShipSymbol, PlayerID: cfg.PlayerID,
			Payload: fmt.Sprintf(`{"location":%q,"nav_status":%q}`, s.LocationSymbol, s.NavStatus),
		})
	}
	return nil
}

// detectContainerlessPinnedHulls (sp-v63s watchdog) is the belt-and-suspenders
// detector for EVERY silent-death class. A hull with a standing fleet dedication
// (dedicated_fleet != ”) is meant to ALWAYS have a running coordinator container:
// the continuous trade/tour engines run one container per hull across manifests, so
// a dedicated hull with NO running container is an anomaly, never normal churn. The
// root silent-death cause is fixed at the source (a live container carrying a stale
// FAILED row, dropped from recovery), but any FUTURE defect that strands a pinned
// hull — a dropped claim, an unlogged crash, a recovery miss — surfaces HERE as one
// interrupt-class event naming the hull, cargo, and how long it has been stranded.
//
// Edge-triggered like detectIdleShips/detectStaleHeartbeats: the age gate
// (containerless for >= threshold, anchored on the assignment's released_at)
// tolerates the brief containerless window of a normal redeploy+recovery, and the
// HasSince cooldown suppresses per-poll re-fire while the state persists (no o8wi
// spam). A hull WITH a running container, an UNDEDICATED hull, and a dedicated hull
// only briefly containerless all stay silent.
//
// A hull pinned to a StandingCoordinatorFleets entry (sp-jetm) is a further
// exemption, orthogonal to the age gate: a contract-fleet hull pooled-idle
// between claims is by design, for as long as the pool's coordinator stays up
// — not just briefly. It stays silent WHILE that fleet's coordinator container
// is RUNNING; the moment the coordinator itself dies, the hull loses its
// exemption and the watchdog fires exactly as it would for any other pin (the
// coordinator-died case SHOULD alarm — that loss mode is real).
func detectContainerlessPinnedHulls(ctx context.Context, db *gorm.DB, store captain.EventStore, cfg DetectorConfig, now time.Time) error {
	if cfg.PinnedHullContainerless <= 0 {
		return nil // disabled
	}
	cutoff := now.Add(-cfg.PinnedHullContainerless)

	// Precompute once per sweep (not per-ship) which configured fleets currently
	// have a RUNNING pool coordinator — one query per configured fleet, mirroring
	// inTransitShipSymbols's "compute the exemption set up front" shape.
	pooledFleets, err := runningStandingCoordinatorFleets(ctx, db, cfg.PlayerID, cfg.StandingCoordinatorFleets)
	if err != nil {
		return err
	}

	var ships []persistence.ShipModel
	if err := db.WithContext(ctx).
		Where("player_id = ? AND dedicated_fleet <> ''", cfg.PlayerID).
		Find(&ships).Error; err != nil {
		return err
	}
	for _, s := range ships {
		// A RUNNING container referencing this hull → healthy, skip. Same quoted-
		// symbol LIKE join detectIdleShips uses (config stores "ship_symbol":"X").
		var busy int64
		if err := db.WithContext(ctx).Model(&persistence.ContainerModel{}).
			Where("player_id = ? AND status = ? AND config LIKE ?",
				cfg.PlayerID, "RUNNING", "%\""+s.ShipSymbol+"\"%").
			Count(&busy).Error; err != nil {
			return err
		}
		if busy > 0 {
			continue
		}
		// Pool-managed fleet with its coordinator up → containerless is by design,
		// regardless of how long. Skip before the age gate so a pool hull idle for
		// hours between claims never fires (see doc comment above).
		if pooledFleets[s.DedicatedFleet] {
			continue
		}
		// Age gate: only a hull containerless for >= threshold is anomalous. Anchor
		// on released_at (when its last assignment ended). A dedicated hull that has
		// never held an assignment (released_at NULL) is a launch/config concern, not
		// a silent death — leave it to detectIdleShips rather than false-alarm here.
		if s.ReleasedAt == nil || s.ReleasedAt.After(cutoff) {
			continue
		}
		// Containerless is a persistent state, not an edge: cooldown on ANY recent
		// twin (processed or not) so an interrupt-class event does not re-wake the
		// captain every poll while the hull stays stranded (mirrors detectIdleShips).
		recent, err := store.HasSince(ctx, cfg.PlayerID, captain.EventPinnedHullContainerless, s.ShipSymbol, cutoff)
		if err != nil || recent {
			continue
		}
		containerlessMinutes := int(now.Sub(*s.ReleasedAt).Minutes())
		_ = store.Record(ctx, &captain.Event{
			Type: captain.EventPinnedHullContainerless, Ship: s.ShipSymbol, PlayerID: cfg.PlayerID,
			Payload: fmt.Sprintf(`{"ship_symbol":%q,"dedicated_fleet":%q,"location":%q,"cargo_units":%d,"cargo_capacity":%d,"containerless_minutes":%d}`,
				s.ShipSymbol, s.DedicatedFleet, s.LocationSymbol, s.CargoUnits, s.CargoCapacity, containerlessMinutes),
		})
	}
	return nil
}

// runningStandingCoordinatorFleets returns the set of DedicatedFleet tags
// (sp-jetm) whose configured pool coordinator currently has a RUNNING
// container. Empty fleets means the exemption is off entirely — the query
// loop below simply does not run — so a DetectorConfig that never sets
// StandingCoordinatorFleets reproduces the original sp-v63s behavior exactly.
func runningStandingCoordinatorFleets(ctx context.Context, db *gorm.DB, playerID int, fleets []StandingCoordinatorFleet) (map[string]bool, error) {
	if len(fleets) == 0 {
		return nil, nil
	}
	pooled := make(map[string]bool, len(fleets))
	for _, f := range fleets {
		var running int64
		if err := db.WithContext(ctx).Model(&persistence.ContainerModel{}).
			Where("player_id = ? AND status = ? AND container_type = ?", playerID, "RUNNING", f.ContainerType).
			Count(&running).Error; err != nil {
			return nil, err
		}
		if running > 0 {
			pooled[f.Fleet] = true
		}
	}
	return pooled, nil
}

const incomeStallStreamKey = "income"

func detectIncomeStall(ctx context.Context, db *gorm.DB, store captain.EventStore, cfg DetectorConfig, now time.Time) error {
	if cfg.IncomeStall <= 0 {
		return nil
	}
	cutoff := now.Add(-cfg.IncomeStall)
	var runningCoordinators int64
	if err := db.WithContext(ctx).Model(&persistence.ContainerModel{}).
		Where("player_id = ? AND status = ? AND container_type LIKE ? AND started_at IS NOT NULL AND started_at <= ?",
			cfg.PlayerID, "RUNNING", "%coordinator%", cutoff).
		Count(&runningCoordinators).Error; err != nil {
		return err
	}
	if runningCoordinators == 0 {
		return nil
	}
	var incoming int64
	if err := db.WithContext(ctx).Model(&persistence.TransactionModel{}).
		Where("player_id = ? AND amount > 0 AND timestamp >= ?", cfg.PlayerID, cutoff).
		Count(&incoming).Error; err != nil {
		return err
	}
	if incoming > 0 {
		return nil
	}
	recent, err := store.HasSince(ctx, cfg.PlayerID, captain.EventIncomeStalled, incomeStallStreamKey, now.Add(-cfg.IncomeStall))
	if err != nil || recent {
		return err
	}
	return store.Record(ctx, &captain.Event{
		Type: captain.EventIncomeStalled, Ship: incomeStallStreamKey, PlayerID: cfg.PlayerID,
		Payload: fmt.Sprintf(`{"stall_hours":%.1f,"running_coordinators":%d}`,
			cfg.IncomeStall.Hours(), runningCoordinators),
	})
}

// incomeEngine names one earning line for per-engine stall detection
// (sp-2cdu): its coordinator's container_type (the "is this engine even
// active" gate, scoped to ONE engine instead of detectIncomeStall's any-
// container '%coordinator%' match) and the ledger category/operation_type
// combination that identifies its income transactions.
//
// category alone identifies contract income unambiguously: CONTRACT_REVENUE
// is only ever produced by contract fulfillment (see
// ledger.TypeToCategoryMap). It does NOT distinguish trading from
// manufacturing - both post SELL_CARGO transactions under the same
// TRADING_REVENUE category, which is exactly how the real 2026-07-09
// incident's healthy aggregate TRADING_REVENUE flow hid a fully dead
// contract line: the missing signal was never visible in Category, so
// operationTypes disambiguates within it.
//
// operationTypes hold the REAL values that land in the operation_type column
// today - cargo_transaction.go/refuel_ship.go persist
// opCtx.NormalizedOperationType(), the NORMALIZED value, not the raw
// OperationContext string a coordinator/worker sets on ctx. The two only
// coincide when the raw string has no case in that switch: "trade_route"
// (run_trade_route_coordinator.go) and "factory_workflow"
// (run_factory_coordinator.go) fall through its default case and persist
// unnormalized. "manufacturing_worker" (run_manufacturing_task_worker.go) is
// NOT one of those - the switch has an explicit
// case "manufacturing_worker": return "manufacturing", so every sale a
// manufacturing task makes (e.g. ManufacturingSeller.SellCargo from the
// COLLECT_SELL task type) persists as operation_type="manufacturing". This
// detector bucket on the real persisted values, not the pre-normalization
// context strings, so it is grounded in what actually lands in the column
// (a separate follow-up tracks reconciling the mapping's dead
// "goods_factory_coordinator"/"arbitrage_worker" cases - no caller passes
// those - fleet-wide; that's well beyond this detector's blast radius).
type incomeEngine struct {
	name           string   // dedup-key suffix ("income:<name>") and payload "engine" field
	containerType  string   // container_type of this engine's top-level coordinator
	commandType    string   // "" = gate on container_type alone; set to disambiguate engines that SHARE a container_type
	category       string   // ledger category of this engine's income transactions
	operationTypes []string // empty = category alone is unambiguous (contract)
}

var incomeEngines = []incomeEngine{
	{name: "contract", containerType: "CONTRACT_FLEET_COORDINATOR", category: "CONTRACT_REVENUE"},
	// trade_route and tour_run containers both persist container_type="TRADING"
	// (container.ContainerTypeTrading) - only command_type tells them apart.
	// Before sp-lyc3 this line gated on container_type alone, so once
	// trade_fleet_coordinator (sp-1278) made tour_run containers the fleet's
	// permanent steady state, the activity gate below was perpetually satisfied
	// by tour traffic while the ledger check only ever accepts
	// operation_type='trade_route' - a healthy, churning tour fleet with zero
	// real trade-route activity read as "trading engine active, income
	// stalled" and false-fired every IncomeStallHours window even though the
	// fleet was earning. commandType pins the gate to genuine trade_route
	// containers, mirroring the 'tour' line's own disambiguation below.
	{name: "trading", containerType: "TRADING", commandType: "trade_route", category: "TRADING_REVENUE",
		operationTypes: []string{"trade_route"}},
	{name: "manufacturing", containerType: "MANUFACTURING_COORDINATOR", category: "TRADING_REVENUE",
		operationTypes: []string{"factory_workflow", "manufacturing"}},
	// Tour sales are TRADING_REVENUE with operation_type="tour" (tour_run's buy/
	// sell legs, sp-lgnh) — a stream the 'trading' line above deliberately EXCLUDES
	// (it filters operation_type='trade_route'), so a tour-fleet collapse was
	// invisible to every income detector (sp-7vos / v63s cross-check). The gate
	// needs commandType: tour_run and trade_route containers share
	// container_type="TRADING" (both are container.ContainerTypeTrading), so
	// container_type alone would fire this line whenever ANY trade route is up.
	{name: "tour", containerType: "TRADING", commandType: "tour_run", category: "TRADING_REVENUE",
		operationTypes: []string{"tour"}},
}

// detectEngineIncomeStall runs detectIncomeStall's same "coordinator running
// but nothing came in" test per earning line instead of in aggregate
// (sp-2cdu): a single engine can flatline for hours while a DIFFERENT
// engine's healthy income keeps detectIncomeStall's system-wide amount>0
// check satisfied, exactly the failure mode that let a real 4h contract-
// engine collapse ride through undetected while manufacturing/trading kept
// the aggregate ledger flowing (contract: 42k/4h vs ~1.6M expected, while
// TRADING_REVENUE posted +1.13M/4h).
//
// Reuses cfg.IncomeStall (no new tunable) and the existing EventIncomeStalled
// type (already interrupt-class - see events.go DefaultInterruptTypes) so
// current consumers wake on it unchanged; a per-engine dedup key and a
// payload "engine" field are the only additions. detectIncomeStall itself is
// untouched - this runs alongside it, not instead of it.
//
// Deliberately a zero-income-in-window threshold (matching
// detectIncomeStall's own model), not a trailing-rate/percentage-drop
// comparison: it fully covers the acceptance criterion (killing a
// coordinator produces exactly zero income, not a partial reduction), and
// contract payouts in particular are lumpy/infrequent even when healthy - a
// trailing-rate ratio would likely raise the false-positive rate this
// detector must avoid, not lower it.
func detectEngineIncomeStall(ctx context.Context, db *gorm.DB, store captain.EventStore, cfg DetectorConfig, now time.Time) error {
	if cfg.IncomeStall <= 0 {
		return nil
	}
	cutoff := now.Add(-cfg.IncomeStall)

	for _, engine := range incomeEngines {
		gate := db.WithContext(ctx).Model(&persistence.ContainerModel{}).
			Where("player_id = ? AND status = ? AND container_type = ? AND started_at IS NOT NULL AND started_at <= ?",
				cfg.PlayerID, "RUNNING", engine.containerType, cutoff)
		if engine.commandType != "" {
			// Engines sharing a container_type (tour_run and trade_route both
			// persist container_type="TRADING") are separated by command_type.
			gate = gate.Where("command_type = ?", engine.commandType)
		}
		var runningCount int64
		if err := gate.Count(&runningCount).Error; err != nil {
			return err
		}
		if runningCount == 0 {
			// Engine isn't active - silence is correct, not a stall. Mirrors
			// detectIncomeStall's own "no coordinators -> nil" gate and
			// detectStreamDown's never-run exemption: an engine that was
			// never started cannot have collapsed.
			continue
		}

		query := db.WithContext(ctx).Model(&persistence.TransactionModel{}).
			Where("player_id = ? AND amount > 0 AND category = ? AND timestamp >= ?",
				cfg.PlayerID, engine.category, cutoff)
		if len(engine.operationTypes) > 0 {
			query = query.Where("operation_type IN ?", engine.operationTypes)
		}
		var incoming int64
		if err := query.Count(&incoming).Error; err != nil {
			return err
		}
		if incoming > 0 {
			continue
		}

		dedupKey := "income:" + engine.name
		recent, err := store.HasSince(ctx, cfg.PlayerID, captain.EventIncomeStalled, dedupKey, now.Add(-cfg.IncomeStall))
		if err != nil {
			return err
		}
		if recent {
			continue
		}
		if err := store.Record(ctx, &captain.Event{
			Type: captain.EventIncomeStalled, Ship: dedupKey, PlayerID: cfg.PlayerID,
			Payload: fmt.Sprintf(`{"engine":%q,"stall_hours":%.1f,"running_coordinators":%d}`,
				engine.name, cfg.IncomeStall.Hours(), runningCount),
		}); err != nil {
			return err
		}
	}
	return nil
}

// detectFactoryIncomeStall closes the aggregation gap the per-engine
// 'manufacturing' line above cannot: that line gates on a single
// MANUFACTURING_COORDINATOR and buckets ALL factory income together, so one dead
// goods factory is masked by any sibling's sales (the real MEDICINE 100-min
// outage rode through invisibly while other factories kept selling — sp-7vos /
// sp-tit8). This detector treats EACH running goods_factory_coordinator as its
// own earner and fires per factory.
//
// Attribution is by container identity, NOT by good. Every sale a factory makes
// routes through the single ledger writer
// (CargoTransactionHandler.recordCargoTransaction, cargo_transaction.go) under
// the factory coordinator's operation context — run_factory_coordinator.go sets
// NewOperationContext(cmd.ContainerID, "factory_workflow") — so the row's
// related_entity_id IS the factory's container ID: an exact, dialect-portable
// join needing no JSON or description parsing. A good-based join was rejected
// after checking live data: a factory sells its intermediates too (the FOOD
// factory posts FERTILIZERS sales, the LAB_INSTRUMENTS factory posts
// ELECTRONICS), and two factories for the same good run concurrently, so a good
// filter would both miss real income and let one factory mask another. The good
// (config "target_good") is used only to NAME the event.
//
// Edge-triggered and windowed exactly like the sibling detectors: the factory
// must have been RUNNING for the full window (started_at <= cutoff) so a
// just-launched or just-restarted factory mid-first-cycle is exempt (RULINGS #2
// resilience), and a per-container HasSince cooldown suppresses per-poll re-fire
// while the stall persists. Dedup is per CONTAINER (not per good) because
// same-good factories coexist — deduping on the good would silence a second
// dead FOOD factory behind the first.
func detectFactoryIncomeStall(ctx context.Context, db *gorm.DB, store captain.EventStore, cfg DetectorConfig, now time.Time) error {
	if cfg.FactoryIncomeStall <= 0 {
		return nil // disabled
	}
	cutoff := now.Add(-cfg.FactoryIncomeStall)

	var factories []persistence.ContainerModel
	if err := db.WithContext(ctx).
		Where("player_id = ? AND status = ? AND container_type = ? AND started_at IS NOT NULL AND started_at <= ?",
			cfg.PlayerID, "RUNNING", "goods_factory_coordinator", cutoff).
		Find(&factories).Error; err != nil {
		return err
	}
	for _, f := range factories {
		var incoming int64
		if err := db.WithContext(ctx).Model(&persistence.TransactionModel{}).
			Where("player_id = ? AND amount > 0 AND related_entity_id = ? AND timestamp >= ?",
				cfg.PlayerID, f.ID, cutoff).
			Count(&incoming).Error; err != nil {
			return err
		}
		if incoming > 0 {
			continue
		}
		// Dedup per factory container: one interrupt while the stall persists,
		// not one per poll (mirrors the sibling income detectors).
		dedupKey := "income:factory:" + f.ID
		recent, err := store.HasSince(ctx, cfg.PlayerID, captain.EventIncomeStalled, dedupKey, now.Add(-cfg.FactoryIncomeStall))
		if err != nil {
			return err
		}
		if recent {
			continue
		}
		good := factoryTargetGood(f.Config)
		if good == "" {
			good = f.ID // malformed config: still surface the stall, named by container
		}
		if err := store.Record(ctx, &captain.Event{
			Type: captain.EventIncomeStalled, Ship: dedupKey, PlayerID: cfg.PlayerID,
			Payload: fmt.Sprintf(`{"engine":"factory","good":%q,"container_id":%q,"stall_hours":%.1f}`,
				good, f.ID, cfg.FactoryIncomeStall.Hours()),
		}); err != nil {
			return err
		}
	}
	return nil
}

// factoryTargetGood extracts a goods_factory_coordinator container's target good
// from its config JSON (StartGoodsFactory persists metadata["target_good"], see
// container_ops_goods.go). Returns "" when the config is absent or unparseable.
func factoryTargetGood(config string) string {
	var fc struct {
		TargetGood string `json:"target_good"`
	}
	if err := json.Unmarshal([]byte(config), &fc); err != nil {
		return ""
	}
	return fc.TargetGood
}

func detectStreamDown(ctx context.Context, db *gorm.DB, store captain.EventStore, cfg DetectorConfig, now time.Time) error {
	if cfg.StreamDown <= 0 || len(cfg.ExpectedStreams) == 0 {
		return nil
	}
	var runningTotal int64
	if err := db.WithContext(ctx).Model(&persistence.ContainerModel{}).
		Where("player_id = ? AND status = ?", cfg.PlayerID, "RUNNING").
		Count(&runningTotal).Error; err != nil {
		return err
	}
	if runningTotal == 0 {
		return nil
	}
	cutoff := now.Add(-cfg.StreamDown)
	for _, stream := range cfg.ExpectedStreams {
		like := stream + "%"
		var running int64
		if err := db.WithContext(ctx).Model(&persistence.ContainerModel{}).
			Where("player_id = ? AND status = ? AND container_type LIKE ?", cfg.PlayerID, "RUNNING", like).
			Count(&running).Error; err != nil {
			return err
		}
		if running > 0 {
			continue
		}
		var lastStopped persistence.ContainerModel
		if err := db.WithContext(ctx).
			Where("player_id = ? AND container_type LIKE ? AND stopped_at IS NOT NULL", cfg.PlayerID, like).
			Order("stopped_at DESC").
			Limit(1).
			Find(&lastStopped).Error; err != nil {
			return err
		}
		if lastStopped.ID == "" || lastStopped.StoppedAt == nil || lastStopped.StoppedAt.After(cutoff) {
			continue
		}
		recent, err := store.HasSince(ctx, cfg.PlayerID, captain.EventStreamDown, stream, now.Add(-cfg.StreamDown))
		if err != nil {
			return err
		}
		if recent {
			continue
		}
		_ = store.Record(ctx, &captain.Event{
			Type: captain.EventStreamDown, Ship: stream, PlayerID: cfg.PlayerID,
			Payload: fmt.Sprintf(`{"stream":%q,"down_minutes":%d,"stopped_at":%q}`,
				stream, int(cfg.StreamDown.Minutes()), lastStopped.StoppedAt.UTC().Format(time.RFC3339)),
		})
	}
	return nil
}

// detectCrashLoops turns a burst of true container deaths into a single
// interrupt. sp-okwk made container.crashed count true (unrecoverable) deaths;
// a lone death is self-healing (auto-restart+resume) and stays deferred. When
// the SAME container dies CrashLoopThreshold times within CrashLoopWindow it is
// a genuine loop, so emit one interrupt-class container.crashloop for it — one
// per loop, not per death (sp-no9i).
func detectCrashLoops(ctx context.Context, db *gorm.DB, store captain.EventStore, cfg DetectorConfig, now time.Time) error {
	if cfg.CrashLoopWindow <= 0 || cfg.CrashLoopThreshold <= 0 {
		return nil
	}
	windowStart := now.Add(-cfg.CrashLoopWindow)
	var crashes []persistence.CaptainEventModel
	if err := db.WithContext(ctx).
		Where("player_id = ? AND type = ? AND created_at > ?",
			cfg.PlayerID, string(captain.EventContainerCrashed), windowStart).
		Find(&crashes).Error; err != nil {
		return err
	}
	deaths := make(map[string]int)
	for i := range crashes {
		if id := crashContainerID(crashes[i].Payload); id != "" {
			deaths[id]++
		}
	}
	for id, n := range deaths {
		if n < cfg.CrashLoopThreshold {
			continue
		}
		// One interrupt per loop, not per death: cooldown on any recent
		// crashloop for this container (mirrors the other detectors' idiom).
		recent, err := store.HasSince(ctx, cfg.PlayerID, captain.EventContainerCrashLoop, id, windowStart)
		if err != nil || recent {
			continue
		}
		_ = store.Record(ctx, &captain.Event{
			Type: captain.EventContainerCrashLoop, Ship: id, PlayerID: cfg.PlayerID,
			Payload: fmt.Sprintf(`{"container_id":%q,"deaths":%d,"window_minutes":%d}`,
				id, n, int(cfg.CrashLoopWindow.Minutes())),
		})
	}
	return nil
}

// crashContainerID extracts the container_id recorded in a container.crashed
// event payload (see recordCrash); returns "" when absent or unparseable.
func crashContainerID(payload string) string {
	var p struct {
		ContainerID string `json:"container_id"`
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return ""
	}
	return p.ContainerID
}

// gasSymbols is the fixed set of SpaceTraders goods the captain's "GAS"
// price class covers (extracted via gas-siphon operations, not mining).
// There is no exported domain classification to reuse — internal/domain/goods
// keeps no ore/gas grouping — so sp-zlfv defines its own minimal, local
// classifier rather than reach into an unrelated package for three strings.
var gasSymbols = map[string]bool{
	"HYDROCARBON":     true,
	"LIQUID_HYDROGEN": true,
	"LIQUID_NITROGEN": true,
}

// matchesGoodClass reports whether goodSymbol belongs to a tripwire's
// configured good scope: the class keyword "ORE" (any *_ORE symbol), the
// class keyword "GAS" (gasSymbols), or a literal comma-separated symbol
// allowlist (exact match, case-insensitive) for anything else.
func matchesGoodClass(goodSymbol, class string) bool {
	switch strings.ToUpper(strings.TrimSpace(class)) {
	case "ORE":
		return strings.HasSuffix(goodSymbol, "_ORE")
	case "GAS":
		return gasSymbols[goodSymbol]
	default:
		for _, sym := range strings.Split(class, ",") {
			if strings.EqualFold(strings.TrimSpace(sym), goodSymbol) {
				return true
			}
		}
		return false
	}
}

// resolveRegimeThreshold returns the effective price threshold to compare
// against, plus the baseline it was derived from. Absolute mode (Threshold
// set) needs no lookup: baseline reports as 0. Multiplier mode looks up the
// OLDEST recorded price-history sample within tw.Window as the baseline and
// scales it; ok=false when a multiplier tripwire has no baseline recorded
// yet within the window (nothing to compare the current price against).
func resolveRegimeThreshold(ctx context.Context, db *gorm.DB, playerID int, waypoint, good string, tw RegimeTripwire, now time.Time) (threshold int, baseline int, ok bool, err error) {
	if tw.Threshold != nil {
		return *tw.Threshold, 0, true, nil
	}
	if tw.Multiplier == nil {
		return 0, 0, false, nil
	}
	var oldest persistence.MarketPriceHistoryModel
	err = db.WithContext(ctx).
		Where("player_id = ? AND waypoint_symbol = ? AND good_symbol = ? AND recorded_at >= ?",
			playerID, waypoint, good, now.Add(-tw.Window)).
		Order("recorded_at ASC").
		Limit(1).
		Find(&oldest).Error
	if err != nil {
		return 0, 0, false, err
	}
	if oldest.WaypointSymbol == "" {
		return 0, 0, false, nil
	}
	baseline = oldest.SellPrice
	return int(*tw.Multiplier * float64(baseline)), baseline, true, nil
}

// regimeDedupKey scopes the edge-trigger cooldown to (good, market,
// direction): the natural identity of a single crossing, not the tripwire
// config that detected it. Two tripwires that happen to overlap on the same
// good+market+direction are a degenerate config the captain would not
// realistically declare (there is no reason to set two tripwires for the
// same good, same direction, different thresholds instead of just one).
func regimeDedupKey(good, waypoint, direction string) string {
	return good + "@" + waypoint + ":" + direction
}

// detectRegimeShift scans MarketData for prices crossing a captain-declared
// tripwire (sp-zlfv): mechanizes the per-wake price sweep the captain used to
// hand-roll ("any ore bid >=200 or gas bid >=150 (~3x baseline) triggers an
// immediate extraction re-consult"). Edge-triggered with cooldown via
// HasSince (sp-1hak lesson): one event per crossing, not per poll — once
// acknowledged, the same crossing does not re-fire until Window elapses AND
// the price re-crosses. No tripwires configured means no query at all (zero
// overhead when unset).
func detectRegimeShift(ctx context.Context, db *gorm.DB, store captain.EventStore, cfg DetectorConfig, now time.Time) error {
	if len(cfg.RegimeTripwires) == 0 {
		return nil
	}
	var markets []persistence.MarketData
	if err := db.WithContext(ctx).Where("player_id = ?", cfg.PlayerID).Find(&markets).Error; err != nil {
		return err
	}
	for _, tw := range cfg.RegimeTripwires {
		for _, m := range markets {
			if !matchesGoodClass(m.GoodSymbol, tw.Good) {
				continue
			}
			threshold, baseline, ok, err := resolveRegimeThreshold(ctx, db, cfg.PlayerID, m.WaypointSymbol, m.GoodSymbol, tw, now)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			price := m.SellPrice
			var crossed bool
			switch tw.Direction {
			case "bid-above":
				crossed = price >= threshold
			case "bid-below":
				crossed = price <= threshold
			}
			if !crossed {
				continue
			}
			key := regimeDedupKey(m.GoodSymbol, m.WaypointSymbol, tw.Direction)
			recent, err := store.HasSince(ctx, cfg.PlayerID, captain.EventMarketRegimeShift, key, now.Add(-tw.Window))
			if err != nil {
				return err
			}
			if recent {
				continue
			}
			_ = store.Record(ctx, &captain.Event{
				Type: captain.EventMarketRegimeShift, Ship: key, PlayerID: cfg.PlayerID,
				Payload: fmt.Sprintf(`{"good":%q,"market":%q,"price":%d,"baseline":%d,"threshold":%d}`,
					m.GoodSymbol, m.WaypointSymbol, price, baseline, threshold),
			})
		}
	}
	return nil
}

func detectCreditsCrossing(ctx context.Context, store captain.EventStore, cfg DetectorConfig) error {
	if cfg.LastCredits == 0 || len(cfg.CreditsThresholds) == 0 {
		return nil
	}
	// Use the supervisor-supplied current credits (sp-sk68 D4): the detector no
	// longer re-derives its own value via CurrentCredits, so it evaluates the
	// SAME number as the wake gate and cannot fail independently on a DB error.
	current := cfg.CurrentCreditsValue
	for _, th := range cfg.CreditsThresholds {
		crossedUp := cfg.LastCredits < th && current >= th
		crossedDown := cfg.LastCredits >= th && current < th
		if !crossedUp && !crossedDown {
			continue
		}
		direction := "up"
		if crossedDown {
			direction = "down"
		}
		key := fmt.Sprintf("%d", th)
		dup, err := store.HasUnprocessed(ctx, cfg.PlayerID, captain.EventCreditsThreshold, key)
		if err != nil || dup {
			continue
		}
		_ = store.Record(ctx, &captain.Event{
			Type: captain.EventCreditsThreshold, Ship: key, PlayerID: cfg.PlayerID,
			Payload: fmt.Sprintf(`{"threshold":%d,"direction":%q,"credits":%d}`, th, direction, current),
		})
	}
	return nil
}

func CurrentCredits(ctx context.Context, db *gorm.DB, playerID int) (int, error) {
	anchor, anchored, err := latestContractAnchor(ctx, db, playerID)
	if err != nil {
		return 0, err
	}
	if !anchored {
		return creditsFromLatestBalance(ctx, db, playerID)
	}
	return creditsAnchoredToContract(ctx, db, playerID, anchor)
}

func latestContractAnchor(ctx context.Context, db *gorm.DB, playerID int) (persistence.TransactionModel, bool, error) {
	var tx persistence.TransactionModel
	err := db.WithContext(ctx).
		Where("player_id = ? AND transaction_type LIKE ?", playerID, "CONTRACT_%").
		Order("timestamp DESC, created_at DESC, id DESC").
		Limit(1).
		Find(&tx).Error
	if err != nil {
		return persistence.TransactionModel{}, false, err
	}
	return tx, tx.ID != "", nil
}

func creditsAnchoredToContract(ctx context.Context, db *gorm.DB, playerID int, anchor persistence.TransactionModel) (int, error) {
	var delta struct{ Sum int }
	err := db.WithContext(ctx).Model(&persistence.TransactionModel{}).
		Select("COALESCE(SUM(amount), 0) AS sum").
		Where("player_id = ? AND timestamp > ?", playerID, anchor.Timestamp).
		Scan(&delta).Error
	if err != nil {
		return 0, err
	}
	return anchor.BalanceAfter + delta.Sum, nil
}

func creditsFromLatestBalance(ctx context.Context, db *gorm.DB, playerID int) (int, error) {
	var tx persistence.TransactionModel
	err := db.WithContext(ctx).
		Where("player_id = ?", playerID).
		Order("timestamp DESC, created_at DESC, id DESC").
		Limit(1).
		Find(&tx).Error
	if err != nil {
		return 0, err
	}
	return tx.BalanceAfter, nil
}
