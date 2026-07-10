package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// depositCandidateMinerTopN is how many ranked rows the assembler pulls from the
// Lane A miner before applying its own allow/block/top-N filters. Generous so a
// blocklist or allowlist can't starve the final top-N (Lane A's live sample had
// ~37 rows at minRecurrence 1).
const depositCandidateMinerTopN = 50

// DepositDemandMiner is the narrow Lane A demand-miner port the deposit assembler
// ranks candidates from (satisfied by *persistence.DemandMiner). Kept local so the
// assembler couples only to the one method it uses.
type DepositDemandMiner interface {
	Mine(ctx context.Context, homeSystem string, playerID int, eraID *int, opts persistence.DemandMinerOptions) ([]persistence.DemandCandidate, error)
}

// WarehouseOperationFinder is the narrow storage-op port the assembler uses to
// locate a running warehouse in the tour graph (satisfied by
// *persistence.StorageOperationRepository).
type WarehouseOperationFinder interface {
	FindRunning(ctx context.Context, playerID int) ([]*storage.StorageOperation, error)
}

// WarehouseSpaceReader reads a warehouse operation's free space and per-good
// stocked units (satisfied by the shared StorageCoordinator).
type WarehouseSpaceReader interface {
	GetStorageShipsForOperation(operationID string) []*storage.StorageShip
	GetTotalCargoAvailable(operationID, goodSymbol string) int
}

// DepositCandidateConfig is the resolved pre-positioning tuning (sp-dchv Lane C).
// It mirrors config.PrePositioningSettings, decoupled from the config package so
// this service stays testable. Enabled=false yields no candidates.
type DepositCandidateConfig struct {
	Enabled           bool
	TopN              int
	MinRecurrence     int
	MinSavingsPerUnit int
	Allowlist         []string // nil => every stock-eligible good; else restrict to these
	Blocklist         []string // never deposited; wins over the allowlist
}

// depositVerdict accumulates the per-re-plan deposit-candidate funnel so the
// assembler emits exactly ONE structured verdict line per re-plan — success or a
// DISTINCT zero-reason (sp-dchv observability; nw9v verdict-line pattern). The
// counts live in the MESSAGE TEXT (not only structured metadata) because the
// text log drops metadata fields, which is precisely how a 3h run of zero
// deposits went undiagnosed: "assembled deposit candidates" logged the count in
// metadata that never reached the log, and the dominant zero-reason
// (no warehouse in the tour graph) logged NOTHING at all. Silent zeros become
// impossible: every return path sets a reason and the deferred emit renders it.
type depositVerdict struct {
	level               string // "" => INFO
	reason              string // "selected" on success; else the distinct zero-reason
	allowedSystems      []string
	warehouseID         string
	storageWaypoint     string
	homeSystem          string
	ceilingCredits      int64
	ceilingKnown        bool
	freeSpace           int
	minerRows           int
	stockEligible       int // rows passing eligibility (known asks, savings >= floor)
	afterWhitelist      int // of those, rows passing allow/block/SupportsGood
	final               int // candidates actually offered to the planner
	warehouseCandidates int // RUNNING ops matching the graph filter, pre tie-break; >1 means a
	// stale zombie row (sp-3lj5) is sitting alongside the live one
}

// BuildDepositCandidates assembles the haul-to-storage deposit sinks the tour
// planner may price against arb sells (sp-dchv Lane C). It finds a RUNNING
// warehouse whose system is inside the tour graph (allowedSystems), mines the
// Lane A demand for that home system, keeps the STOCK-ELIGIBLE goods (both asks
// known AND home_ask > foreign_ask — never speculative, RULINGS #6), applies the
// allow/block/top-N filters, and caps each candidate's units at the MIN of:
//   - remaining contract demand (miner demand units − units already stocked),
//   - remaining warehouse free space (shared, consumed in rank order),
//   - the pre-positioning capital ceiling (shared credit budget / foreign ask).
//
// ceilingCredits is the resolved capital ceiling (10% of live treasury by
// default, junior to the reserve — computed by the caller). ceilingKnown=false
// means the live balance was UNREADABLE: the assembler returns NO candidates
// (fail closed, RULINGS #4 — money guards never spend on an unreadable balance).
// Any discovery/mining error degrades to no candidates (the tour falls back to
// pure arb), never an error the caller must handle.
//
// Every non-disabled exit emits ONE verdict line (see depositVerdict): the
// escalation on sp-dchv (0 deposit legs in 3h) was a diagnosis blind spot, not a
// candidate bug — the dominant path (hull re-planning >1 gate hop from home, so
// no warehouse in its tour graph) returned nil silently. The verdict makes that,
// and every other zero-reason, LOUD and countable.
func BuildDepositCandidates(
	ctx context.Context,
	miner DepositDemandMiner,
	warehouses WarehouseOperationFinder,
	space WarehouseSpaceReader,
	allowedSystems []string,
	playerID int,
	ceilingCredits int64,
	ceilingKnown bool,
	cfg DepositCandidateConfig,
) []routing.TourDepositCandidate {
	logger := common.LoggerFromContext(ctx)

	if !cfg.Enabled {
		// Deliberate off-switch, not a silent zero: no verdict (the feature is off
		// on purpose; the caller gates on Enabled before ever reaching here).
		return nil
	}

	v := &depositVerdict{
		allowedSystems: allowedSystems,
		ceilingCredits: ceilingCredits,
		ceilingKnown:   ceilingKnown,
	}
	var out []routing.TourDepositCandidate
	defer func() {
		v.final = len(out)
		if v.reason == "" {
			v.reason = "selected"
		}
		level := v.level
		if level == "" {
			level = "INFO"
		}
		wh := v.warehouseID
		if wh == "" {
			wh = "none"
		}
		logger.Log(level, fmt.Sprintf(
			"Pre-positioning verdict: %d deposit candidate(s) — %s "+
				"[warehouse=%s wh_candidates=%d reachable=%t systems=%v ceiling=%d(known=%t) free=%d "+
				"funnel: miner_rows=%d stock_eligible=%d after_whitelist=%d]",
			v.final, v.reason, wh, v.warehouseCandidates, v.warehouseID != "", v.allowedSystems,
			v.ceilingCredits, v.ceilingKnown, v.freeSpace,
			v.minerRows, v.stockEligible, v.afterWhitelist),
			map[string]interface{}{
				"final": v.final, "reason": v.reason,
				"warehouse": v.warehouseID, "storage_waypoint": v.storageWaypoint,
				"warehouse_candidates": v.warehouseCandidates,
				"home_system":          v.homeSystem, "reachable_warehouse": v.warehouseID != "",
				"allowed_systems": v.allowedSystems, "ceiling_credits": v.ceilingCredits,
				"ceiling_known": v.ceilingKnown, "free_space": v.freeSpace,
				"miner_rows": v.minerRows, "stock_eligible": v.stockEligible,
				"after_whitelist": v.afterWhitelist,
			})
	}()

	// Fail CLOSED: an unreadable balance (or a non-positive ceiling) buys nothing
	// (RULINGS #4). The 50k reserve + w3he cap are enforced separately at execution;
	// this ceiling is the pre-positioning-specific budget on top.
	if !ceilingKnown || ceilingCredits <= 0 {
		v.level = "WARNING"
		v.reason = "capital ceiling unreadable or zero (fail closed, RULINGS #4)"
		return nil
	}
	if miner == nil || warehouses == nil || space == nil {
		v.level = "WARNING"
		v.reason = "pre-positioning subsystem unwired (miner/warehouses/space nil)"
		return nil
	}

	warehouse, group, whCandidates, err := findWarehouseInGraph(ctx, warehouses, allowedSystems, playerID)
	if err != nil {
		v.level = "WARNING"
		v.reason = "warehouse lookup failed: " + err.Error()
		return nil
	}
	v.warehouseCandidates = whCandidates
	if warehouse == nil {
		// The dominant live zero-reason: the hull re-planned >1 gate hop from home,
		// so the warehouse's system is outside its 2-system tour graph. Correct to
		// fail closed (an unreachable sink cannot be deposited into) — but LOUD now.
		v.reason = "no running warehouse in tour graph"
		return nil
	}
	v.warehouseID = warehouse.ID()
	v.storageWaypoint = warehouse.WaypointSymbol()
	v.homeSystem = shared.ExtractSystemSymbol(v.storageWaypoint)

	// Multi-warehouse (sp-5q2c): the deposit sink is the CO-LOCATED group at the
	// anchor's waypoint (the anchor + any additive-capacity siblings, e.g. light-12
	// beside heavy-4B). Free space and on-hand stock are the SUM across the group, so
	// a second warehouse's slots are priced as real capacity rather than orphaned. A
	// >1 count is now a normal topology (or a harmless zombie contributing 0), not an
	// alarm — the count stays in the verdict for observability but no longer escalates.
	freeSpace := TotalFreeSpace(space, group)
	v.freeSpace = freeSpace
	if freeSpace <= 0 {
		v.reason = "warehouse full (0 free space)"
		return nil
	}

	rows, err := miner.Mine(ctx, v.homeSystem, playerID, nil, persistence.DemandMinerOptions{
		MinRecurrence: cfg.MinRecurrence, TopN: depositCandidateMinerTopN,
	})
	if err != nil {
		v.level = "WARNING"
		v.reason = "demand mining failed: " + err.Error()
		return nil
	}
	v.minerRows = len(rows)

	allow := toSet(cfg.Allowlist)
	block := toSet(cfg.Blocklist)
	minSavings := cfg.MinSavingsPerUnit
	if minSavings <= 0 {
		minSavings = 1
	}
	topN := cfg.TopN
	if topN <= 0 {
		topN = 5
	}

	// Rows arrive stock-eligible-first, ranked by total projected savings (Lane A).
	remainingSpace := freeSpace
	remainingCredits := ceilingCredits
	for _, r := range rows {
		if len(out) >= topN || remainingSpace <= 0 || remainingCredits <= 0 {
			break
		}
		if !r.StockEligible { // eligible only: known both asks AND savings>0 (no speculative stocking)
			continue
		}
		if r.ProjectedSavingsPerUnit < minSavings || r.ForeignAsk <= 0 || r.HomeAsk <= 0 {
			continue
		}
		v.stockEligible++
		if len(allow) > 0 && !allow[r.Good] {
			continue
		}
		if block[r.Good] {
			continue
		}
		// Only offer goods the group actually BUFFERS: withdrawal discovery (Lane D,
		// StorageSourceFinder.FindByGood) keys on the warehouse's supported goods, so
		// depositing a good no co-located member supports would strand it (paid-for
		// inventory that no contract worker can source). Fail closed. AnySupportsGood
		// spans the group so a good buffered by only one of the co-located hulls still
		// qualifies — the deposit executor lands it on a member that supports it.
		if !AnySupportsGood(group, r.Good) {
			continue
		}
		v.afterWhitelist++
		// Remaining contract demand: total historical demand minus what the group
		// already holds for the good (unreserved), SUMMED across every co-located
		// warehouse so a sibling's stock is netted out too. Reserved-but-not-yet-
		// withdrawn units read as still-needed, which is conservative (never over-stocks).
		remainingDemand := r.DemandUnits - TotalCargoAvailable(space, group, r.Good)
		if remainingDemand <= 0 {
			continue
		}
		byCeiling := int(remainingCredits / int64(r.ForeignAsk))
		units := minInt(remainingDemand, minInt(remainingSpace, byCeiling))
		if units <= 0 {
			continue
		}
		out = append(out, routing.TourDepositCandidate{
			Good:            r.Good,
			UnitsWanted:     units,
			SyntheticBid:    r.HomeAsk, // the contract-savings value the sink is priced at
			StorageWaypoint: v.storageWaypoint,
			StorageSystem:   v.homeSystem,
		})
		remainingSpace -= units
		remainingCredits -= int64(units) * int64(r.ForeignAsk)
	}

	if len(out) == 0 {
		// Warehouse reachable with free space, but nothing survived the eligibility/
		// whitelist/space/ceiling funnel — distinct from an absent warehouse.
		v.reason = "no candidates survived filters (eligibility/whitelist/space/ceiling)"
	}
	return out
}

// findWarehouseInGraph resolves the deposit sink for a tour graph. anchor is the
// RUNNING warehouse operation whose system is inside allowedSystems with the latest
// CreatedAt (nil if none matches). group is the co-located additive-capacity set
// (sp-5q2c): every RUNNING warehouse at the anchor's waypoint — the anchor plus any
// siblings sharing that waypoint, whose capacity and stock the caller sums. A non-nil
// error means the warehouse lookup itself failed (surfaced by the caller's verdict).
// matches is the number of RUNNING warehouse operations that matched the graph filter
// (across all in-graph waypoints) before the newest-wins anchor pick. It is >1 both
// for the legitimate multi-warehouse topology and for a stale sp-3lj5 "zombie" row
// (a container stopped without its storage_operations row terminalized) sitting
// alongside a live replacement; the zombie contributes 0 to every aggregate and is
// never chosen as a deposit target, so aggregation stays correct either way.
func findWarehouseInGraph(
	ctx context.Context,
	warehouses WarehouseOperationFinder,
	allowedSystems []string,
	playerID int,
) (anchor *storage.StorageOperation, group []*storage.StorageOperation, matches int, err error) {
	ops, err := warehouses.FindRunning(ctx, playerID)
	if err != nil {
		return nil, nil, 0, err
	}
	graph := RunningWarehousesInGraph(ops, allowedSystems)
	anchor = SelectNewestRunningWarehouse(graph)
	if anchor == nil {
		return nil, nil, len(graph), nil
	}
	// Aggregate only the CO-LOCATED siblings (same waypoint as the anchor): a
	// warehouse at a different in-graph waypoint is a distinct physical sink the
	// planner would route a different leg to, so it is never folded into this
	// candidate's capacity.
	group = RunningWarehousesAtWaypoint(ops, anchor.WaypointSymbol())
	return anchor, group, len(graph), nil
}

func toSet(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	s := make(map[string]bool, len(items))
	for _, it := range items {
		s[it] = true
	}
	return s
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
