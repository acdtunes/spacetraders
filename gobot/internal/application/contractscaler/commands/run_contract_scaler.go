// Package commands holds the dedicated contract auto-scaler (epic sp-9le3x): a
// STANDING, PERMANENT coordinator that ramps a FIXED, EXCLUSIVE contract fleet to
// a live-tunable ceiling behind ONE money guard — a 200000-credit working-capital
// cushion. It replaces the reactive capacity-reconciler's contract-delivery role
// with a fixed plan: no runtime solver, no re-sensing, no demand bridge. It
// resolves this era's waypoint roles ONCE at arm (a lookup, not a solve), builds
// the fixed buy sequence, and each tick tops the fleet up to min(plan, ceiling)
// while the treasury can spare the cushion — scaling as fast as treasury + API
// allow (no per-tick cap). It DRIVES the kept autosizer buy primitive; it does not
// rebuild the buyer. Exclusive HULLS (never shared with gate/trade) kill the churn
// class; treasury stays shared, arbitrated by the 200000 cushion.
package commands

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contractscaler"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// ceilingKey is the live-tunable knob that caps the scaler fleet
	// (contract_fleet_max_hulls, hot-reloaded each tick — the single operator lever).
	ceilingKey = "contract_fleet_max_hulls"

	// DefaultContractFleetMaxHulls is the ceiling when the operator sets none: kept
	// LOW for a gate sprint (contracts are the funding floor, the gate needs the
	// treasury); raised post-gate to 10-12 for full contract throughput.
	DefaultContractFleetMaxHulls = 2

	// ContractCushion is the SOLE money guard on a scaler buy (RULINGS #6 amendment,
	// Admiral-authorized): buy only while treasury-price >= this. It is working
	// capital for warehouse stocking + goods-in-flight, well above the 50000
	// immutable reserve floor (RULINGS #5, untouched).
	ContractCushion = 200_000

	// scalerFleet is the exclusive dedicated-fleet tag every scaler hull carries so
	// no gate/trade coordinator ever poaches it (the churn-class fix).
	scalerFleet = "contract"

	defaultTickSeconds = 900 // 15min — the ramp is strategic, most ticks are a no-op at the ceiling
)

// --- ports (wired by setters at boot; every one nil-safe, fail-CLOSED on unread) ---

// RoleResolver reads this era's home-system markets and resolves the fixed roles
// (central parks, far sources, far sink) plus the per-park demand weights — the
// once-at-arm lookup that drives the whole plan.
type RoleResolver interface {
	ResolveRoles(ctx context.Context, playerID int) (contractscaler.EraRoles, map[string]float64, error)
}

// TreasuryReader reads live credits; readable=false ⇒ the cushion guard fails
// closed (never spend on an unknown balance).
type TreasuryReader interface {
	Treasury(ctx context.Context, playerID int) (credits int64, readable bool, err error)
}

// PriceReader reads the next hull's purchase price + the yard to buy at;
// readable=false ⇒ the buy is skipped (no price → no cushion check).
type PriceReader interface {
	NextHullPrice(ctx context.Context, playerID int, shipType string) (price int64, yard string, readable bool, err error)
}

// FleetCounter reads how many scaler-dedicated hulls exist now — the ramp's
// Current. A read error holds the ramp (fail-closed).
type FleetCounter interface {
	ContractHullCount(ctx context.Context, playerID int) (int, error)
}

// BuyOrder is one approved scaler purchase driven through the kept autosizer buy
// primitive: buy the light frame, dedicate it EXCLUSIVELY to the contract fleet,
// and home it demand-ranked across the spread standby set (C1).
type BuyOrder struct {
	PlayerID        int
	Unit            contractscaler.PlanUnit
	Yard            string
	ExpectedPrice   int64
	DedicatedFleet  string
	StandbyStations []string
	StandbyDemand   map[string]float64
}

// BuyResult reports the executed purchase.
type BuyResult struct {
	ShipSymbol string
	Price      int64
}

// Purchaser buys + dedicates + homes ONE scaler hull. The concrete impl drives the
// kept autosizer BuyAndDedicate primitive (the money-integrity batch path) and the
// contract HomeShipCommand — this coordinator does not rebuild the buyer.
type Purchaser interface {
	BuyAndHome(ctx context.Context, order BuyOrder) (BuyResult, error)
}

// RunContractScalerCommand launches the standing scaler for a player. Disabled is
// the escape hatch (default false ⇒ live once launched); nothing arms the launch
// itself except the operator's arm gate, so a bare deploy is byte-identical.
type RunContractScalerCommand struct {
	PlayerID    int
	ContainerID string
	AgentSymbol string
	Disabled    bool
	DryRun      bool
	TickSeconds int
}

// RunContractScalerResponse reports ramp progress (observed on shutdown only — the
// loop is infinite).
type RunContractScalerResponse struct {
	Ticks  int
	Bought int
	Errors []string
}

// RunContractScalerHandler is the registered singleton (one instance serves every
// player's ticks). All decision inputs are read fresh each pass; the only in-memory
// state is the memoized-at-arm plan, keyed by container ID.
type RunContractScalerHandler struct {
	roles    RoleResolver
	treasury TreasuryReader
	price    PriceReader
	counter  FleetCounter
	buyer    Purchaser
	ceiling  liveconfig.Reader
	clock    shared.Clock

	mu    sync.Mutex
	plans map[string]*armedPlan // keyed by container ID
}

// armedPlan is the per-coordinator once-at-arm resolution: the fixed buy sequence
// + the spread set + the demand weights, all stable for the era.
type armedPlan struct {
	plan            []contractscaler.PlanUnit
	standbyStations []string
	standbyDemand   map[string]float64
}

// NewRunContractScalerHandler wires the handler. clock defaults to the real clock.
func NewRunContractScalerHandler(clock shared.Clock) *RunContractScalerHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunContractScalerHandler{clock: clock, plans: make(map[string]*armedPlan)}
}

func (h *RunContractScalerHandler) SetRoleResolver(r RoleResolver)       { h.roles = r }
func (h *RunContractScalerHandler) SetTreasuryReader(r TreasuryReader)   { h.treasury = r }
func (h *RunContractScalerHandler) SetPriceReader(r PriceReader)         { h.price = r }
func (h *RunContractScalerHandler) SetFleetCounter(r FleetCounter)       { h.counter = r }
func (h *RunContractScalerHandler) SetPurchaser(p Purchaser)             { h.buyer = p }
func (h *RunContractScalerHandler) SetCeilingReader(r liveconfig.Reader) { h.ceiling = r }

// Handle runs the ramp loop until the context is cancelled.
func (h *RunContractScalerHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RunContractScalerCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}
	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", fmt.Sprintf("Contract scaler starting (disabled=%v, dry_run=%v)", cmd.Disabled, cmd.DryRun), map[string]interface{}{
		"action": "contract_scaler_start", "container_id": cmd.ContainerID,
	})

	result := &RunContractScalerResponse{Errors: []string{}}
	tick := time.Duration(cmd.TickSeconds) * time.Second
	if tick <= 0 {
		tick = defaultTickSeconds * time.Second
	}

	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}
		bought, err := h.reconcileOnce(ctx, cmd)
		if err != nil {
			result.Errors = append(result.Errors, err.Error())
			logger.Log("ERROR", fmt.Sprintf("Contract scaler reconcile failed: %v", err), nil)
		}
		result.Ticks++
		result.Bought += bought

		select {
		case <-time.After(tick):
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
}

// reconcileOnce runs one ramp pass: resolve the plan (once at arm), read the live
// ceiling + current fleet, and buy the next fixed-sequence units up to
// min(plan, ceiling) while the treasury can spare the 200000 cushion. Returns how
// many hulls it bought this pass. Never a per-tick cap.
func (h *RunContractScalerHandler) reconcileOnce(ctx context.Context, cmd *RunContractScalerCommand) (int, error) {
	logger := common.LoggerFromContext(ctx)

	// Default-off / disabled: never spend.
	if cmd.Disabled {
		return 0, nil
	}

	armed, err := h.armedPlanFor(ctx, cmd)
	if err != nil {
		return 0, err
	}
	if len(armed.plan) == 0 {
		return 0, nil // no roles resolved (empty era) → nothing to ramp
	}

	ceiling := h.liveCeiling(ctx, cmd)
	target := contractscaler.Target(len(armed.plan), ceiling)

	current, err := h.counter.ContractHullCount(ctx, cmd.PlayerID)
	if err != nil {
		return 0, nil // unknown fleet size → hold the ramp (fail closed)
	}

	bought := 0
	for current < target {
		unit := armed.plan[current]

		price, yard, priceOK, _ := h.price.NextHullPrice(ctx, cmd.PlayerID, unit.ShipType)
		if !priceOK {
			logger.Log("INFO", "Contract scaler: hull price unreadable — holding the ramp", map[string]interface{}{
				"action": "contract_scaler_price_unreadable", "container_id": cmd.ContainerID,
			})
			break // fail closed
		}

		treasury, treasuryOK, _ := h.treasury.Treasury(ctx, cmd.PlayerID)
		if !treasuryOK {
			logger.Log("INFO", "Contract scaler: treasury unreadable — holding the ramp", map[string]interface{}{
				"action": "contract_scaler_treasury_unreadable", "container_id": cmd.ContainerID,
			})
			break // fail closed
		}

		// THE SOLE GUARD: keep the 200000 working-capital cushion (RULINGS #6 amendment).
		if treasury-price < ContractCushion {
			logger.Log("INFO", fmt.Sprintf("Contract scaler: buy held — treasury %d minus price %d < %d cushion", treasury, price, ContractCushion), map[string]interface{}{
				"action": "contract_scaler_cushion_hold", "container_id": cmd.ContainerID,
			})
			break
		}

		if cmd.DryRun || h.buyer == nil {
			logger.Log("WARN", fmt.Sprintf("Contract scaler WOULD BUY %s @ %d at %s (dry-run or no purchaser)", unit.ShipType, price, yard), map[string]interface{}{
				"action": "contract_scaler_would_buy", "container_id": cmd.ContainerID,
			})
			break // no spend; the ramp resumes once armed for real
		}

		res, err := h.buyer.BuyAndHome(ctx, BuyOrder{
			PlayerID:        cmd.PlayerID,
			Unit:            unit,
			Yard:            yard,
			ExpectedPrice:   price,
			DedicatedFleet:  scalerFleet,
			StandbyStations: armed.standbyStations,
			StandbyDemand:   armed.standbyDemand,
		})
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("Contract scaler buy failed: %v", err), map[string]interface{}{
				"action": "contract_scaler_buy_error", "container_id": cmd.ContainerID,
			})
			break
		}
		logger.Log("INFO", fmt.Sprintf("Contract scaler BOUGHT %s @ %d at %s for %v %s", res.ShipSymbol, res.Price, yard, unit.Role, unit.Target), map[string]interface{}{
			"action": "contract_scaler_bought", "container_id": cmd.ContainerID, "ship_symbol": res.ShipSymbol, "target": unit.Target,
		})
		bought++
		current++
	}
	return bought, nil
}

// armedPlanFor returns the container's once-at-arm plan, resolving + memoizing it
// on first use. The role lookup is a per-era LOOKUP, so it runs once — subsequent
// ticks reuse the cached plan (no re-sense, no re-solve).
func (h *RunContractScalerHandler) armedPlanFor(ctx context.Context, cmd *RunContractScalerCommand) (*armedPlan, error) {
	h.mu.Lock()
	if armed, ok := h.plans[cmd.ContainerID]; ok {
		h.mu.Unlock()
		return armed, nil
	}
	h.mu.Unlock()

	roles, demand, err := h.roles.ResolveRoles(ctx, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("contract scaler role lookup: %w", err)
	}
	armed := &armedPlan{
		plan:            contractscaler.BuildPlan(roles, demand),
		standbyStations: append([]string(nil), roles.CentralParks...),
		standbyDemand:   contractscaler.StandbyDemand(roles, demand),
	}
	h.mu.Lock()
	h.plans[cmd.ContainerID] = armed
	h.mu.Unlock()
	return armed, nil
}

// liveCeiling reads contract_fleet_max_hulls from the container's own config each
// tick (Pattern-C hot-reload), falling back to the documented default. An
// unreadable snapshot (nil reader / missing row) uses the default rather than
// stalling — the ceiling is a throttle, not a money guard.
func (h *RunContractScalerHandler) liveCeiling(ctx context.Context, cmd *RunContractScalerCommand) int {
	if h.ceiling == nil {
		return DefaultContractFleetMaxHulls
	}
	snap, err := h.ceiling.Snapshot(ctx, cmd.ContainerID, cmd.PlayerID)
	if err != nil {
		return DefaultContractFleetMaxHulls
	}
	if v := snap.PositiveIntOrZero(ceilingKey); v > 0 {
		return v
	}
	return DefaultContractFleetMaxHulls
}
