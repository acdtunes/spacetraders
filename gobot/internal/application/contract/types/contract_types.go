package types

import (
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// This package contains all command and response types for the contract application layer.
//
// By extracting types to a separate package, we break the circular dependency between
// the commands and services packages:
//
//	commands package → imports types package
//	services package → imports types package
//	NO circular dependency!
//
// This is Phase 3.1 of the application layer refactoring plan.

// ============================================================================
// Contract Negotiation
// ============================================================================

// NegotiateContractCommand requests negotiation of a new contract.
type NegotiateContractCommand struct {
	ShipSymbol string
	PlayerID   shared.PlayerID
}

// NegotiateContractResponse contains the result of contract negotiation.
type NegotiateContractResponse struct {
	Contract      *contract.Contract
	WasNegotiated bool // false if existing contract returned (error 4511)
}

// ============================================================================
// Contract Acceptance
// ============================================================================

// AcceptContractCommand requests acceptance of a contract.
type AcceptContractCommand struct {
	ContractID string
	PlayerID   shared.PlayerID
}

// AcceptContractResponse contains the accepted contract.
type AcceptContractResponse struct {
	Contract *contract.Contract
}

// ============================================================================
// Contract Delivery
// ============================================================================

// DeliverContractCommand requests delivery of goods to fulfill a contract.
type DeliverContractCommand struct {
	ContractID  string
	ShipSymbol  string
	TradeSymbol string
	Units       int
	PlayerID    shared.PlayerID
}

// DeliverContractResponse contains the result of cargo delivery.
type DeliverContractResponse struct {
	Contract       *contract.Contract
	UnitsDelivered int
}

// ============================================================================
// Contract Fulfillment
// ============================================================================

// FulfillContractCommand requests marking a contract as fulfilled.
type FulfillContractCommand struct {
	ContractID string
	PlayerID   shared.PlayerID
}

// FulfillContractResponse contains the fulfilled contract.
type FulfillContractResponse struct {
	Contract *contract.Contract
}

// ============================================================================
// Contract Workflow
// ============================================================================

// RunWorkflowCommand orchestrates complete contract workflow execution.
type RunWorkflowCommand struct {
	ShipSymbol    string
	PlayerID      shared.PlayerID
	ContainerID   string // This worker's container ID (optional)
	CoordinatorID string // Parent coordinator container ID (optional)
}

// RunWorkflowResponse contains workflow execution results.
type RunWorkflowResponse struct {
	Negotiated  bool
	Accepted    bool
	Fulfilled   bool
	TotalProfit int
	TotalTrips  int
	Error       string
}

// ============================================================================
// Fleet Coordination
// ============================================================================

// RunFleetCoordinatorCommand orchestrates multiple ships executing contracts.
// Ships are discovered dynamically - no pre-assignment needed.
type RunFleetCoordinatorCommand struct {
	PlayerID    shared.PlayerID
	ShipSymbols []string // Deprecated: kept for backward compatibility, no longer used
	ContainerID string   // Coordinator's own container ID

	// DedicatedShips (sp-snmb): operator-supplied ship symbols permanently
	// reserved for this coordinator, parametrized via CLI/config - e.g.
	// --dedicated-ships. NOT a hardcoded default; an empty list means no
	// dedicated fleet and the coordinator behaves exactly as before.
	DedicatedShips []string

	// StandbyStations (sp-snmb): operator-supplied waypoint symbols an idle
	// dedicated ship homes to, parametrized via CLI/config - e.g.
	// --standby-stations. An empty list disables homing (dedicated ships
	// still get the claim-filter, they just don't relocate when idle).
	StandbyStations []string

	// DedicatedShipsSeeded (sp-86vb) is the first-boot marker for the
	// DedicatedShips launch seed. It is false on a genuine first boot (the seed
	// is applied into the dedication tag) and true on every later daemon-restart
	// rebuild (the seed is NOT replayed). Persisted into the coordinator's OWN
	// container config right after the first seed and reloaded here on recovery
	// (buildContractFleetCoordinatorCommand), so the immutable launch snapshot can
	// never re-stamp "contract" onto a hull that was deliberately `fleet remove`d
	// while the coordinator ran — the removal survives the restart (RULINGS #2).
	DedicatedShipsSeeded bool

	// Idle-gap arb (sp-1z2h): harvest the dedicated fleet's idle time with
	// hub-local one-shot arb legs (see contract.IdleArbDispatcher). All knobs
	// flow from the persisted launch config (RULINGS #5); zero values take the
	// contract package's documented defaults. IdleArbDisabled is the escape
	// hatch — the harvest is ON by default wherever a dedicated fleet exists.
	IdleArbDisabled     bool
	IdleArbReserveHulls int     // idle hulls never leased to arb (default 1)
	IdleArbHubRadius    float64 // outer hub-local filter distance (default 250)
	IdleArbMaxSpend     int     // per-leg spend cap (default 100k)
	IdleArbMinMargin    int     // absolute per-unit margin floor (default 1)
	IdleArbIntervalSecs int     // dispatch tick in seconds (default 90)
	// sp-uohe money guards (all parametrized, RULINGS #5):
	IdleArbLeashRadius     float64  // tight money-guard leash radius (default 80)
	IdleArbMaxLegSecs      int      // projected per-leg flight-time cap, seconds (default 480)
	IdleArbMarginVerifyPct int      // live-verify floor as % of quoted margin (default 80)
	IdleArbBlacklist       []string // excluded goods; nil → default [ELECTRONICS]
	// sp-lbbm lane mutex: post-termination recovery hold, seconds (default 1200 =
	// 20min). Keeps a (good, sink) lane closed after its leg terminates so
	// sequential passes never re-dump a sink the last leg just depressed.
	IdleArbRecoveryHoldSecs int
	// sp-u4tv per-trip live-profitability floor (all parametrized, RULINGS #5):
	IdleArbMinNetProfit    int // absolute after-fuel net floor per unit (default 100)
	IdleArbNetProfitPct    int // relative net floor as % of buy price (default 20)
	IdleArbFuelCostPerUnit int // per-cargo-unit fuel estimate subtracted from spread (default 35)

	// CommandCargoBaseline (sp-uj6a, RULINGS #5): minimum cargo capacity a
	// COMMAND-role hull must carry to remain a contract-selection candidate
	// once IncludeCommandShip has opted it into the pool (sp-4a4e). A stock
	// hull below this bar double-trips a load a light hauler single-trips,
	// spending its whole speed advantage on the extra leg for a net loss, so
	// it is filtered back out at selection time. Zero takes the contract
	// package's documented default (80, the light-hauler standard); era-2's
	// upgraded frigate (115 cargo) clears it.
	CommandCargoBaseline int

	// Auto-liquidation (sp-39oi): a hull FilterUnrelatedCargo parks for holding cargo
	// unrelated to the active contract self-clears via a one-shot cargo_liquidation
	// worker instead of sitting filtered out of candidacy forever — the fleet-scale
	// jam this closes (every dual-duty contract hull stranded 6-77u of leftovers and
	// the pool fell to zero fulfillments). AutoLiquidationDisabled is the escape hatch:
	// liquidation-by-SALE is ON by default (it only converts a stranded hold to
	// treasury), so an absent/false key keeps it on.
	AutoLiquidationDisabled bool
	// LiquidationMinJettisonValue is the value floor (bid * units) below which a leftover
	// lot may be jettisoned as a LAST resort. 0 (the default) disables jettison entirely —
	// nothing is destroyed without an explicit captain-set threshold; a lot with a bid is
	// always sold (value recovered, never dumped — RULINGS #5).
	LiquidationMinJettisonValue int
}

// RunFleetCoordinatorResponse contains fleet coordination results.
type RunFleetCoordinatorResponse struct {
	ContractsCompleted int
	Errors             []string
}

// ============================================================================
// Fleet Rebalancing
// ============================================================================

// RebalanceContractFleetCommand requests rebalancing of ships across markets.
type RebalanceContractFleetCommand struct {
	CoordinatorID string
	PlayerID      shared.PlayerID
	SystemSymbol  string
}

// RebalanceContractFleetResponse contains rebalancing results.
type RebalanceContractFleetResponse struct {
	ShipsMoved         int
	TargetMarkets      []string
	AverageDistance    float64
	DistanceThreshold  float64
	RebalancingSkipped bool
	SkipReason         string
	Assignments        map[string]string // ship symbol -> market waypoint
}

// ============================================================================
// Ship Balancing
// ============================================================================

// BalanceShipPositionCommand requests repositioning a ship to optimize fleet distribution.
type BalanceShipPositionCommand struct {
	ShipSymbol    string
	PlayerID      shared.PlayerID
	CoordinatorID string // ID of coordinator that spawned this balancing operation (empty for manual balancing)
}

// BalanceShipPositionResponse contains ship balancing results.
type BalanceShipPositionResponse struct {
	TargetMarket  string  // Waypoint symbol of selected market
	AssignedShips int     // Number of ships assigned to this market during balancing
	Distance      float64 // Distance from ship to target market
	Score         float64 // Balancing score (lower is better)
	Navigated     bool    // Whether navigation was successful
}

// ============================================================================
// Dedicated Ship Homing (sp-snmb)
// ============================================================================

// HomeShipCommand requests sending an idle dedicated ship to an
// operator-configured standby station, balanced across the configured set
// (l7h2 Phase 3): the station with the fewest fleet hulls already parked at
// (or heading to) it wins, distance breaking ties - so idle hulls spread
// across the standby hubs instead of clumping on the nearest one.
type HomeShipCommand struct {
	ShipSymbol      string
	PlayerID        shared.PlayerID
	StandbyStations []string // Operator-supplied waypoint symbols (--standby-stations)

	// FleetShips (l7h2 Phase 3): symbols of every hull in this coordinator's
	// dedicated fleet - the homing peers whose positions determine station
	// occupancy for balancing. Empty preserves the original behavior: plain
	// nearest-station homing.
	FleetShips []string
}

// HomeShipResponse contains the result of a homing dispatch.
type HomeShipResponse struct {
	TargetStation string  // Waypoint symbol of the selected standby station
	Distance      float64 // Distance from ship to target station
	Navigated     bool    // Whether navigation was successful (false if already there, or no stations configured)
}
