// detectors_income.go — income-stall detectors: aggregate (detectIncomeStall),
// per-engine (detectEngineIncomeStall over incomeEngines), and per-factory
// (detectFactoryIncomeStall). Split out of detectors.go for navigability; behavior unchanged.
package watchkeeper

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

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
