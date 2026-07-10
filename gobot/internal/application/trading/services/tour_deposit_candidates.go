package services

import (
	"context"

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
		return nil
	}
	// Fail CLOSED: an unreadable balance (or a non-positive ceiling) buys nothing
	// (RULINGS #4). The 50k reserve + w3he cap are enforced separately at execution;
	// this ceiling is the pre-positioning-specific budget on top.
	if !ceilingKnown || ceilingCredits <= 0 {
		logger.Log("INFO", "Pre-positioning: no deposit candidates (capital ceiling unreadable or zero — fail closed)", map[string]interface{}{
			"ceiling_known": ceilingKnown, "ceiling_credits": ceilingCredits,
		})
		return nil
	}
	if miner == nil || warehouses == nil || space == nil {
		return nil // dependencies not wired — pre-positioning disabled
	}

	warehouse := findWarehouseInGraph(ctx, warehouses, allowedSystems, playerID)
	if warehouse == nil {
		return nil // no running warehouse in the tour graph — nothing to deposit into
	}
	storageWaypoint := warehouse.WaypointSymbol()
	homeSystem := shared.ExtractSystemSymbol(storageWaypoint)

	freeSpace := 0
	for _, s := range space.GetStorageShipsForOperation(warehouse.ID()) {
		freeSpace += s.AvailableSpace()
	}
	if freeSpace <= 0 {
		logger.Log("INFO", "Pre-positioning: warehouse full — no deposit candidates this tour", map[string]interface{}{
			"warehouse": warehouse.ID(), "storage_waypoint": storageWaypoint,
		})
		return nil
	}

	rows, err := miner.Mine(ctx, homeSystem, playerID, nil, persistence.DemandMinerOptions{
		MinRecurrence: cfg.MinRecurrence, TopN: depositCandidateMinerTopN,
	})
	if err != nil {
		logger.Log("WARNING", "Pre-positioning: demand mining failed — no deposit candidates (tour falls back to pure arb)", map[string]interface{}{
			"home_system": homeSystem, "error": err.Error(),
		})
		return nil
	}

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
	var out []routing.TourDepositCandidate
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
		if len(allow) > 0 && !allow[r.Good] {
			continue
		}
		if block[r.Good] {
			continue
		}
		// Only offer goods the warehouse actually BUFFERS: withdrawal discovery
		// (Lane D, StorageSourceFinder.FindByGood) keys on the warehouse's supported
		// goods, so depositing a good the warehouse doesn't support would strand it
		// (paid-for inventory that no contract worker can source). Fail closed.
		if !warehouse.SupportsGood(r.Good) {
			continue
		}
		// Remaining contract demand: total historical demand minus what the
		// warehouse already holds for the good (unreserved). GetTotalCargoAvailable
		// counts unreserved stock; reserved-but-not-yet-withdrawn units read as
		// still-needed, which is conservative (never over-stocks).
		remainingDemand := r.DemandUnits - space.GetTotalCargoAvailable(warehouse.ID(), r.Good)
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
			StorageWaypoint: storageWaypoint,
			StorageSystem:   homeSystem,
		})
		remainingSpace -= units
		remainingCredits -= int64(units) * int64(r.ForeignAsk)
	}

	if len(out) > 0 {
		logger.Log("INFO", "Pre-positioning: assembled deposit candidates", map[string]interface{}{
			"home_system": homeSystem, "storage_waypoint": storageWaypoint,
			"candidates": len(out), "free_space": freeSpace, "ceiling_credits": ceilingCredits,
		})
	}
	return out
}

// findWarehouseInGraph returns the first RUNNING warehouse operation whose system
// is inside the tour graph (allowedSystems), or nil. v1 targets one warehouse;
// with several the lowest-ID one in the graph wins (deterministic).
func findWarehouseInGraph(
	ctx context.Context,
	warehouses WarehouseOperationFinder,
	allowedSystems []string,
	playerID int,
) *storage.StorageOperation {
	ops, err := warehouses.FindRunning(ctx, playerID)
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", "Pre-positioning: warehouse lookup failed — no deposit candidates", map[string]interface{}{
			"error": err.Error(),
		})
		return nil
	}
	allowed := toSet(allowedSystems)
	var found *storage.StorageOperation
	for _, op := range ops {
		if op.OperationType() != storage.OperationTypeWarehouse {
			continue
		}
		if !allowed[shared.ExtractSystemSymbol(op.WaypointSymbol())] {
			continue
		}
		if found == nil || op.ID() < found.ID() {
			found = op
		}
	}
	return found
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
