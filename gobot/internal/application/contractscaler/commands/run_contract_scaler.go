// Package commands holds the dedicated contract auto-scaler: a
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

	// DefaultContractFleetMaxHulls is the ceiling when the operator sets none: the whole contract
	// operation sized at 3 hulls, filled delivery-first. It is also the cold start's GATE-ENTRY BAR —
	// bootstrap enters GATE once the full contract fleet reaches min(plan size, this ceiling) — so the
	// number is chosen for TIME-TO-GATE, not for contract throughput: a small operation funds the gate
	// sooner than a saturated one (delivery saturates ~7-8). Operators raise it live via
	// contract_fleet_max_hulls once the gate is behind them; the 200000 cushion gates the ramp either way.
	DefaultContractFleetMaxHulls = 3

	// ContractCushion is the SOLE money guard on a scaler buy (RULINGS #6 amendment,
	// Admiral-authorized): buy only while treasury-price >= this. It is the derived
	// SCALER-CAPEX tier common.ContractScalerCushion (200000 = common.ImmutableReserveFloor
	// + 150k, sp-zq635 re-homed it into the ONE floor source) — working capital for
	// warehouse stocking + goods-in-flight, well above the 50000 immutable base floor
	// (RULINGS #5, untouched) and above the contract-operating cushion because a scaler buy
	// is lumpy capex, not per-cycle operating spend.
	ContractCushion = common.ContractScalerCushion

	// scalerFleet is the exclusive dedicated-fleet tag every scaler hull carries so
	// no gate/trade coordinator ever poaches it (the churn-class fix).
	scalerFleet = "contract"

	defaultTickSeconds = 900 // 15min — the ramp is strategic, most ticks are a no-op at the ceiling

	// defaultReadTimeout bounds each per-tick DB read (the once-at-arm plan resolve + the live-ceiling
	// snapshot). database/sql has NO connection-acquire timeout and there is no server statement_timeout,
	// so a read issued when the connection pool is momentarily exhausted waits for a free conn FOREVER on
	// an unbounded ctx — the exact 11h RUNNING-but-silent freeze in sp-ljvxa. This applies the daemon-wide
	// dbOperationTimeout idiom (container_runner.go) to the scaler's reads: a stuck read errors at the
	// deadline → logged → retried next tick (a failed resolve is NOT memoized, so it re-resolves cleanly).
	defaultReadTimeout = 5 * time.Second
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

// DepotElementCounter reads the contract depot's actuated warehouse/stocker element counts
// from the persistent depot registry — the ramp's per-role Current for the depot roles. It is
// what makes the ramp RECONCILE the existing depot (add only the plan-short units) instead of
// buying duplicate warehouses when the ceiling rises. A read error holds the ramp (fail-closed).
// A nil counter (unset at boot) ⇒ warehouse/stocker are unreachable ⇒ delivery-only, byte-identical.
type DepotElementCounter interface {
	WarehouseCount(ctx context.Context, playerID int) (int, error)
	StockerCount(ctx context.Context, playerID int) (int, error)
}

// DepotGrowOrder is one approved depot growth: place ShipSymbol (an idle hull, reclaimed OR
// bought) into the contract depot at Hub — the warehouse anchor waypoint (the plan unit's
// Target). No price/dedication: the grower re-dedicates the hull to its role fleet through the
// depot launch verbs (positionDepotElementHull), and the depot self-solves its own supported_goods.
type DepotGrowOrder struct {
	PlayerID   int
	ShipSymbol string
	Hub        string
}

// DepotGrower grows the EXISTING contract depot by one warehouse/stocker element — the depot
// half of the fixed plan, reusing the persistent depot store (AddElement / AddDepot) + the depot
// launch verbs (launchDepotWarehouse / launchDepotStocker). It ADDS to the existing depot (never
// rebuilds it), so the ramp reconciles TORWIND-15/11 + -18 up to the plan targets. A nil grower
// (unset at boot) ⇒ the depot roles are unreachable ⇒ delivery-only, byte-identical.
type DepotGrower interface {
	GrowWarehouse(ctx context.Context, order DepotGrowOrder) error
	GrowStocker(ctx context.Context, order DepotGrowOrder) error
}

// BuyOrder is one approved scaler purchase driven through the kept autosizer buy
// primitive: buy the light frame, dedicate it EXCLUSIVELY to the contract fleet,
// and home it to its FIXED placement slot (one hull per waypoint). The
// standby set is the ≤6 fixed delivery slots (TopDeliverySlots); the homing zips this
// hull to its slot by symbol — NO runtime demand.
type BuyOrder struct {
	PlayerID        int
	Unit            contractscaler.PlanUnit
	Yard            string
	ExpectedPrice   int64
	DedicatedFleet  string
	StandbyStations []string
}

// BuyResult reports the executed purchase.
type BuyResult struct {
	ShipSymbol string
	Price      int64
}

// Purchaser buys ONE scaler hull. BuyAndHome buys+dedicates+homes a DELIVERY hull (the kept
// autosizer BuyAndDedicate primitive + the contract HomeShipCommand). BuyHull buys ONE
// UNDEDICATED hull for a DEPOT role (no contract-dedicate, no home): the grower re-dedicates it
// to the "warehouse"/"stocker" fleet through the depot launch verbs, so the buy must leave it
// unclaimed for that adoption (mirrors the reclaim path, which also hands the grower an
// undedicated idle hull). This coordinator rebuilds neither buyer.
type Purchaser interface {
	BuyAndHome(ctx context.Context, order BuyOrder) (BuyResult, error)
	BuyHull(ctx context.Context, order BuyOrder) (BuyResult, error)
}

// ReclaimOrder is one ZERO-SPEND reuse: re-dedicate an already-owned idle undedicated
// cargo hull into the contract fleet and home it to its FIXED placement slot across the SAME
// ≤6 standby set a BuyOrder carries. No purchase — the hull already exists.
type ReclaimOrder struct {
	PlayerID        int
	ShipSymbol      string
	StandbyStations []string
}

// IdleHullReclaimer is the FREE reuse tier the ramp tries BEFORE every buy: find one
// idle, UNDEDICATED, CARGO-CAPABLE hull already owned and reclaim it into the contract
// fleet (RULINGS #7 — only DedicatedFleet=="" hulls; NEVER a dedicated hull of any
// fleet; NEVER a probe/0-cargo hull; NEVER a hull mid-task). Reuse is FREE, so it is
// NOT gated by the buy cushion and may proceed even below it — it strictly REDUCES
// spend. A nil reclaimer skips the tier entirely (byte-identical to the buy-only ramp).
type IdleHullReclaimer interface {
	// FindReclaimable returns one reusable hull symbol (ok=false when none exists). A
	// read error holds the reuse tier (fail-closed) and the ramp falls through to a buy.
	FindReclaimable(ctx context.Context, playerID int) (shipSymbol string, ok bool, err error)
	// Reclaim re-dedicates the hull to the contract fleet (single-writer AssignFleet,
	// RULINGS #3) and homes it exactly like a bought hull. An error means the hull was
	// NOT taken, so the caller safely buys instead (no double-count).
	Reclaim(ctx context.Context, order ReclaimOrder) error
}

// DeliverySurplusReleaser un-dedicates surplus "contract" delivery hulls — the RE-ROLE path
// when the delivery count exceeds the plan knee (sp-mtgje: the delivery cap dropped 8→6, so the
// 2 live-surplus delivery hulls must become warehouses, NOT stay as delivery and NOT be bought
// anew). It releases IDLE delivery hulls (never mid-delivery) to the UNDEDICATED idle pool, from
// where the depot fill's reuse-before-buy tier reclaims them into warehouse roles the same pass —
// a re-composition with zero new hulls. Release is FREE (no spend), so it is never cushion-gated.
// A nil releaser (unset at boot) skips the re-role entirely (byte-identical to the buy-only ramp).
type DeliverySurplusReleaser interface {
	// ReleaseSurplusDelivery un-dedicates up to count idle "contract" delivery hulls (deterministic:
	// highest-symbol first, so the ≤6 lowest-symbol hulls keep the fixed slots the homing zips them
	// to). It returns how many were actually released; a read/assign error releases fewer (fail-closed
	// — a hull left dedicated is safe, it just re-roles a later pass).
	ReleaseSurplusDelivery(ctx context.Context, playerID, count int) (int, error)
}

// DepotHullReclaimer is the HOME-SCOPED reuse tier BOTH depot roles' reuse-before-buy fill consults
// (sp-fihvy stocker, generalized to the warehouse by sp-fis8y, RULINGS #14): every depot element must
// be gate-reachable to the depot's home system, so a reclaim candidate for either role must be
// gate-reachable to the warehouse's home system too — never any idle hull fleet-wide. A nil
// DepotHullReclaimer (unset at boot) degrades BOTH roles' reuse tier back to the fleet-wide
// IdleHullReclaimer, byte-identical to the previous ramp.
type DepotHullReclaimer interface {
	// FindReclaimableForHome returns one reusable, home-reachable hull symbol (ok=false when none
	// exists). A read error holds the reuse tier (fail-closed) and the caller falls through to a buy.
	FindReclaimableForHome(ctx context.Context, playerID int, homeSystem string) (shipSymbol string, ok bool, err error)
}

// DepotPriceReader is the HOME-SCOPED price signal the depot STOCKER role's buy fallback consults
// (sp-fihvy, RULINGS #14): only a yard IN the warehouse's home system may price the hull — the
// fleet-wide PriceReader could return a cheaper yard in a foreign system, which would buy a stocker
// hull the depot can never route home. Warehouse buys are unaffected; they keep the shared
// PriceReader. A nil DepotPriceReader (unset at boot) degrades the stocker buy fallback back to the
// shared PriceReader, byte-identical to the previous ramp.
type DepotPriceReader interface {
	NextHullPriceForHome(ctx context.Context, playerID int, shipType, homeSystem string) (price int64, yard string, readable bool, err error)
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
	roles          RoleResolver
	treasury       TreasuryReader
	price          PriceReader
	counter        FleetCounter
	depotCounter   DepotElementCounter
	grower         DepotGrower
	buyer          Purchaser
	reclaimer      IdleHullReclaimer
	releaser       DeliverySurplusReleaser // Re-role surplus delivery → warehouse (nil-safe: skip re-role)
	depotReclaimer DepotHullReclaimer      // sp-fihvy: home-scoped stocker reuse tier (nil-safe fallback to reclaimer)
	depotPrice     DepotPriceReader        // sp-fihvy: home-scoped stocker buy-fallback price (nil-safe fallback to price)
	ceiling        liveconfig.Reader
	clock          shared.Clock
	readTimeout    time.Duration // per-tick DB-read deadline; 0 ⇒ defaultReadTimeout (sp-ljvxa). Injectable for tests.

	mu    sync.Mutex
	plans map[string]*armedPlan // keyed by container ID
}

// armedPlan is the per-coordinator once-at-arm resolution: the fixed buy sequence + the ≤6
// FIXED placement slots (TopDeliverySlots) the homing zips hulls onto, stable for the era. No
// demand map — the runtime homing carries no demand.
type armedPlan struct {
	plan            []contractscaler.PlanUnit
	standbyStations []string
}

// NewRunContractScalerHandler wires the handler. clock defaults to the real clock.
func NewRunContractScalerHandler(clock shared.Clock) *RunContractScalerHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunContractScalerHandler{clock: clock, plans: make(map[string]*armedPlan)}
}

func (h *RunContractScalerHandler) SetRoleResolver(r RoleResolver)     { h.roles = r }
func (h *RunContractScalerHandler) SetTreasuryReader(r TreasuryReader) { h.treasury = r }
func (h *RunContractScalerHandler) SetPriceReader(r PriceReader)       { h.price = r }
func (h *RunContractScalerHandler) SetFleetCounter(r FleetCounter)     { h.counter = r }
func (h *RunContractScalerHandler) SetDepotElementCounter(r DepotElementCounter) {
	h.depotCounter = r
}
func (h *RunContractScalerHandler) SetDepotGrower(g DepotGrower)             { h.grower = g }
func (h *RunContractScalerHandler) SetPurchaser(p Purchaser)                 { h.buyer = p }
func (h *RunContractScalerHandler) SetIdleHullReclaimer(r IdleHullReclaimer) { h.reclaimer = r }
func (h *RunContractScalerHandler) SetDeliverySurplusReleaser(r DeliverySurplusReleaser) {
	h.releaser = r
}
func (h *RunContractScalerHandler) SetCeilingReader(r liveconfig.Reader)       { h.ceiling = r }
func (h *RunContractScalerHandler) SetDepotHullReclaimer(r DepotHullReclaimer) { h.depotReclaimer = r }
func (h *RunContractScalerHandler) SetDepotPriceReader(r DepotPriceReader)     { h.depotPrice = r }

// SetReadTimeout overrides the per-tick DB-read deadline (default defaultReadTimeout). Tests inject a
// short value to assert the bound fires without waiting the full 5s.
func (h *RunContractScalerHandler) SetReadTimeout(d time.Duration) { h.readTimeout = d }

// boundReadCtx derives the per-tick read deadline from ctx — the daemon dbOperationTimeout idiom applied
// to the scaler's reads so an exhausted-pool DB read errors instead of hanging the loop forever (sp-ljvxa).
func (h *RunContractScalerHandler) boundReadCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	d := h.readTimeout
	if d <= 0 {
		d = defaultReadTimeout
	}
	return context.WithTimeout(ctx, d)
}

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

// reconcileOnce runs one ramp pass: resolve the plan (once at arm), read the live ceiling, and fill
// the fixed plan's roles IN PRIORITY ORDER — delivery, then warehouse, then stocker — each up to its
// per-role target, budgeted by the ceiling, while the treasury can spare the 200000 cushion. Reuse
// precedes every buy. Warehouse/stocker actuate the EXISTING depot (reconcile: add only the short
// units); a nil depot counter/grower makes the ramp DELIVERY-ONLY (byte-identical). Returns how many
// hulls it bought this pass. Never a per-tick cap.
func (h *RunContractScalerHandler) reconcileOnce(ctx context.Context, cmd *RunContractScalerCommand) (int, error) {
	// Default-off / disabled: never spend.
	if cmd.Disabled {
		return 0, nil
	}

	// Bound the top-of-tick DB reads (once-at-arm plan resolve + live ceiling): an exhausted-pool read
	// errors at the deadline instead of freezing the whole loop forever (sp-ljvxa). The BUYS below keep
	// the original ctx — they are already bounded by the HTTP client timeout + rate-limiter aging, and a
	// 5s cap would false-kill a legitimate multi-hop home navigation.
	planCtx, planCancel := h.boundReadCtx(ctx)
	armed, err := h.armedPlanFor(planCtx, cmd)
	planCancel()
	if err != nil {
		return 0, err
	}
	if len(armed.plan) == 0 {
		return 0, nil // no roles resolved (empty era) → nothing to ramp
	}

	ceilCtx, ceilCancel := h.boundReadCtx(ctx)
	ceiling := h.liveCeiling(ceilCtx, cmd)
	ceilCancel()
	deliveryTarget, warehouseTarget, stockerTarget := contractscaler.RoleTargets(armed.plan)

	// Budgeted per-role fill: allocate the ceiling across the roles in PRIORITY ORDER, each capped at
	// its plan target. Delivery is the scarce primary; the depot bundle draws only leftover budget, and
	// only when the depot is actuatable (counter + grower wired). Nil either ⇒ zero depot budget ⇒ the
	// ramp is delivery-only, byte-identical to the pre-actuation scaler.
	remaining := ceiling
	takeDelivery := clampTake(deliveryTarget, remaining)
	remaining -= takeDelivery
	takeWarehouse, takeStocker := 0, 0
	if h.DepotActuatable() {
		// FUNCTIONAL DEPOT FIRST: fund the ANCHOR warehouse, THEN the stocker that fills it, THEN the
		// remaining warehouse depth — a usable mini-depot (1 warehouse + 1 stocker) forms before the depth
		// deepens (an empty warehouse is useless until a stocker deposits into it). So the stocker outranks
		// warehouse #2+ for the leftover budget; the plan role COUNTS are unchanged, only the fill priority.
		anchorTarget := 0
		if warehouseTarget > 0 {
			anchorTarget = 1
		}
		anchor := clampTake(anchorTarget, remaining)
		remaining -= anchor
		takeStocker = clampTake(stockerTarget, remaining)
		remaining -= takeStocker
		extra := clampTake(warehouseTarget-anchor, remaining)
		remaining -= extra
		takeWarehouse = anchor + extra
	}

	bought := h.fillDeliveryRole(ctx, cmd, armed, takeDelivery)

	// RE-ROLE the delivery surplus: when the live delivery count exceeds the knee (the cap
	// dropped 8→6), un-dedicate the surplus into the idle pool so the depot fill below RECLAIMS it into
	// the warehouse deficit the SAME pass — a re-composition with ZERO new hulls, never left idle. Runs
	// between the delivery fill (which never removes) and the depot fill (which reclaims the released
	// hulls). Skipped under dry-run and when the depot is not actuatable (nowhere to re-role to).
	if !cmd.DryRun && h.DepotActuatable() {
		h.reroleSurplusDelivery(ctx, cmd, takeDelivery, takeWarehouse)
	}

	// Reconcile the EXISTING depot up to the plan targets (add only the short warehouse/stocker). Skipped
	// wholesale when no depot budget — so the default ceiling makes ZERO depot counter/grower calls.
	if takeWarehouse > 0 || takeStocker > 0 {
		bought += h.fillDepotRoles(ctx, cmd, armed, takeWarehouse, takeStocker)
	}
	return bought, nil
}

// reroleSurplusDelivery un-dedicates the delivery hulls that exceed the plan knee so the depot fill's
// reuse-before-buy tier reclaims them into the warehouse deficit the SAME pass — the delivery-over-target
// path (live 8 delivery, knee 6 → the 2 surplus become warehouses). It releases AT MOST the
// warehouse deficit (takeWarehouse − haveWarehouse), so a released hull is always absorbed by a warehouse
// slot rather than stranded idle. Best-effort + fail-closed: a nil releaser, an unreadable count, or no
// surplus is a no-op; a partial release just re-roles fewer this pass (retried next tick). FREE — never
// cushion-gated (un-dedicating spends nothing and strictly re-composes the existing fleet).
func (h *RunContractScalerHandler) reroleSurplusDelivery(ctx context.Context, cmd *RunContractScalerCommand, takeDelivery, takeWarehouse int) {
	if h.releaser == nil {
		return
	}
	haveDelivery, err := h.counter.ContractHullCount(ctx, cmd.PlayerID)
	if err != nil || haveDelivery <= takeDelivery {
		return // fail-closed on an unknown count; no surplus → nothing to re-role
	}
	haveWarehouse, err := h.depotCounter.WarehouseCount(ctx, cmd.PlayerID)
	if err != nil {
		return // fail-closed: without the warehouse Current the release can't be bounded safely
	}
	release := haveDelivery - takeDelivery
	if deficit := takeWarehouse - haveWarehouse; deficit < release {
		release = deficit
	}
	if release <= 0 {
		return // no warehouse deficit to absorb the surplus → leave delivery over rather than strand it idle
	}
	logger := common.LoggerFromContext(ctx)
	released, err := h.releaser.ReleaseSurplusDelivery(ctx, cmd.PlayerID, release)
	if err != nil {
		logger.Log("WARN", fmt.Sprintf("Contract scaler: releasing %d surplus delivery hull(s) partially failed (%d released): %v", release, released, err), map[string]interface{}{
			"action": "contract_scaler_surplus_release_error", "container_id": cmd.ContainerID,
		})
	}
	if released > 0 {
		logger.Log("INFO", fmt.Sprintf("Contract scaler RE-ROLED %d surplus delivery hull(s) toward the warehouse deficit (delivery %d over the %d knee)", released, haveDelivery, takeDelivery), map[string]interface{}{
			"action": "contract_scaler_surplus_reroled", "container_id": cmd.ContainerID,
		})
	}
}

// fillDeliveryRole fills the delivery role up to takeDelivery — the byte-identical buy/reclaim+home
// path: reuse a free idle hull before every buy, else buy the next demand-ranked delivery unit and home
// it, gated by the sole 200000 cushion. haveDelivery reads the "contract"-tag Current (fail-closed on a
// read error). Returns how many hulls it BOUGHT (reclaims are free, not counted).
func (h *RunContractScalerHandler) fillDeliveryRole(ctx context.Context, cmd *RunContractScalerCommand, armed *armedPlan, takeDelivery int) int {
	logger := common.LoggerFromContext(ctx)
	haveDelivery, err := h.counter.ContractHullCount(ctx, cmd.PlayerID)
	if err != nil {
		return 0 // unknown fleet size → hold the ramp (fail closed)
	}

	bought := 0
	for haveDelivery < takeDelivery {
		// ZERO-SPEND REUSE TIER (RULINGS #7): before spending, reclaim a free IDLE UNDEDICATED
		// CARGO-CAPABLE hull into the contract fleet. FREE ⇒ NOT cushion-gated (the cushion gates
		// buys only) — reuse may proceed below the cushion and strictly reduces spend.
		if h.tryReclaim(ctx, cmd, armed) {
			haveDelivery++ // reused one — advance the ramp with no purchase
			continue
		}

		unit := armed.plan[haveDelivery]

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
		haveDelivery++
	}
	return bought
}

// DepotActuatable reports whether the depot roles can be filled — BOTH the depot counter (per-role
// Current) and the grower (the AddElement + launch actuator) must be wired. A nil either makes the ramp
// delivery-only, byte-identical to the pre-actuation scaler. Exported as the wiring/diagnostic predicate
// (is the depot half armed?), and the gate the budgeted fill consults each pass.
func (h *RunContractScalerHandler) DepotActuatable() bool {
	return h.depotCounter != nil && h.grower != nil
}

// fillDepotRoles reconciles the EXISTING depot in FUNCTIONAL-DEPOT-FIRST order: the ANCHOR warehouse,
// THEN the stocker that fills it, THEN the remaining warehouse depth — so a usable mini-depot (1
// warehouse + 1 stocker) forms before the depth deepens. The stocker is grown ONLY once ≥1 warehouse
// exists to deposit into (no orphan stocker). Reuse-before-buy + the 200000 cushion live in fillDepotRole;
// dry-run observes only. fillDepotRoles is entered only when a role has budget, so a delivery-only pass
// never touches the depot.
func (h *RunContractScalerHandler) fillDepotRoles(ctx context.Context, cmd *RunContractScalerCommand, armed *armedPlan, takeWarehouse, takeStocker int) int {
	logger := common.LoggerFromContext(ctx)
	if cmd.DryRun {
		logger.Log("WARN", "Contract scaler WOULD GROW the depot (dry-run, no actuation)", map[string]interface{}{
			"action": "contract_scaler_would_grow", "container_id": cmd.ContainerID,
		})
		return 0
	}
	hub := planHub(armed.plan)
	if hub == "" {
		return 0 // no central anchor (empty era already returned) → nothing to grow
	}

	haveWarehouse, err := h.depotCounter.WarehouseCount(ctx, cmd.PlayerID)
	if err != nil {
		return 0 // fail-closed: hold the depot fill on an unreadable Current
	}

	bought := 0
	// Phase 1 — the ANCHOR warehouse: the hold the stocker deposits into. Grow at most one before the
	// stocker so a functional mini-depot forms first.
	if takeWarehouse >= 1 && haveWarehouse < 1 {
		grew := h.fillDepotRole(ctx, cmd, contractscaler.Warehouse, hub, haveWarehouse, 1)
		bought += grew
		haveWarehouse += grew
	}
	// Phase 2 — the STOCKER: only once ≥1 warehouse EXISTS to deposit into (a stocker with no warehouse has
	// nowhere to deposit). This is the reorder — the stocker precedes the remaining warehouse depth. If the
	// anchor failed to form (grow held/errored), haveWarehouse is still 0 and the stocker is safely skipped.
	if takeStocker > 0 && haveWarehouse >= 1 {
		haveStocker, err := h.depotCounter.StockerCount(ctx, cmd.PlayerID)
		if err != nil {
			return bought // fail-closed
		}
		bought += h.fillDepotRole(ctx, cmd, contractscaler.Stocker, hub, haveStocker, takeStocker)
	}
	// Phase 3 — the remaining warehouse DEPTH, behind the now-functional mini-depot.
	if haveWarehouse < takeWarehouse {
		bought += h.fillDepotRole(ctx, cmd, contractscaler.Warehouse, hub, haveWarehouse, takeWarehouse)
	}
	return bought
}

// fillDepotRole fills one depot role (warehouse or stocker) from have up to take: reuse a free idle
// undedicated hull before every buy (the grower re-dedicates it to the role fleet — FREE, never cushion-
// gated), else buy ONE undedicated hull behind the 200000 cushion and grow the depot with it. Returns
// how many hulls it BOUGHT.
func (h *RunContractScalerHandler) fillDepotRole(ctx context.Context, cmd *RunContractScalerCommand, role contractscaler.UnitRole, hub string, have, take int) int {
	logger := common.LoggerFromContext(ctx)
	// homeSystem scopes both depot roles' REUSE tier to the warehouse's home system (sp-fihvy stocker,
	// generalized to the warehouse by sp-fis8y, RULINGS #14). hub is always the warehouse's Target
	// waypoint (planHub), so this is the depot's one true home system. The BUY-fallback PRICE stays
	// role-split: depotHullPrice keeps the Warehouse path on the pre-existing fleet-wide price reader,
	// byte-identical — a freshly bought warehouse hull is not proven stranded the way a reused idle hull
	// can be, and the warehouse's own coordinator self-navigates cross-system to place it, so narrowing
	// the buy-time yard search is left out of this fix's scope.
	homeSystem := shared.ExtractSystemSymbol(hub)
	bought := 0
	for have < take {
		// REUSE BEFORE BUY (FREE, ungated): reclaim a free idle undedicated cargo hull and grow the
		// depot with it. FindReclaimable hands back a symbol WITHOUT dedicating; the grower re-dedicates
		// it to the role fleet (positionDepotElementHull). A grow failure falls through to the buy.
		if symbol, ok := h.findReclaimableForDepot(ctx, cmd, role, homeSystem); ok {
			if err := h.growDepot(ctx, cmd, role, symbol, hub); err == nil {
				have++
				continue
			}
			logger.Log("WARN", "Contract scaler: reclaimed-hull depot grow failed — falling through to a buy", map[string]interface{}{
				"action": "contract_scaler_depot_grow_reclaim_error", "container_id": cmd.ContainerID, "ship_symbol": symbol,
			})
		}

		price, yard, priceOK := h.depotHullPrice(ctx, cmd, role, homeSystem)
		if !priceOK {
			break // fail closed
		}
		treasury, treasuryOK, _ := h.treasury.Treasury(ctx, cmd.PlayerID)
		if !treasuryOK {
			break // fail closed
		}
		// THE SOLE GUARD, applied to depot buys too: keep the 200000 cushion (RULINGS #6 amendment).
		if treasury-price < ContractCushion {
			logger.Log("INFO", fmt.Sprintf("Contract scaler: depot %v buy held — treasury %d minus price %d < %d cushion", role, treasury, price, ContractCushion), map[string]interface{}{
				"action": "contract_scaler_depot_cushion_hold", "container_id": cmd.ContainerID,
			})
			break
		}
		if h.buyer == nil {
			break
		}
		res, err := h.buyer.BuyHull(ctx, BuyOrder{
			PlayerID:      cmd.PlayerID,
			Unit:          contractscaler.PlanUnit{Role: role, ShipType: contractscaler.ScalerShipType, Target: hub},
			Yard:          yard,
			ExpectedPrice: price,
		})
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("Contract scaler depot-hull buy failed: %v", err), map[string]interface{}{
				"action": "contract_scaler_depot_buy_error", "container_id": cmd.ContainerID,
			})
			break
		}
		if err := h.growDepot(ctx, cmd, role, res.ShipSymbol, hub); err != nil {
			logger.Log("ERROR", fmt.Sprintf("Contract scaler bought %s but depot grow failed: %v", res.ShipSymbol, err), map[string]interface{}{
				"action": "contract_scaler_depot_grow_error", "container_id": cmd.ContainerID, "ship_symbol": res.ShipSymbol,
			})
			break // bought but could not place — stop this role rather than spin
		}
		logger.Log("INFO", fmt.Sprintf("Contract scaler GREW the depot with %s for %v at %s", res.ShipSymbol, role, hub), map[string]interface{}{
			"action": "contract_scaler_depot_grew", "container_id": cmd.ContainerID, "ship_symbol": res.ShipSymbol,
		})
		bought++
		have++
	}
	return bought
}

// findReclaimableForDepot returns one free idle undedicated cargo hull symbol for a depot role, or
// ok=false when the reuse tier is unwired, hits a scan error (fail-closed → the ramp buys), or finds no
// free hull. Unlike the delivery reuse it does NOT dedicate the hull to "contract" — the grower's launch
// re-dedicates it to the "warehouse"/"stocker" fleet.
//
// BOTH depot roles are home-scoped (sp-fihvy stocker, generalized to the warehouse by sp-fis8y, RULINGS
// #14): every depot element must be in, or gate-reachable to, the depot's home system, so the reuse tier
// consults ONLY h.depotReclaimer.FindReclaimableForHome — never any idle hull fleet-wide — for both roles
// alike. This is what stops a stranded hull evicted by the daemon's depotElementHullViable precondition
// (e.g. TORWIND-19) from being re-offered to the very next grow: the home-scoped reclaimer already
// excludes it. A nil depotReclaimer or blank homeSystem falls back to the fleet-wide h.reclaimer, so
// either role degrades to its historical (before) behavior until fully wired.
func (h *RunContractScalerHandler) findReclaimableForDepot(ctx context.Context, cmd *RunContractScalerCommand, role contractscaler.UnitRole, homeSystem string) (string, bool) {
	if (role == contractscaler.Stocker || role == contractscaler.Warehouse) && h.depotReclaimer != nil && homeSystem != "" {
		symbol, ok, err := h.depotReclaimer.FindReclaimableForHome(ctx, cmd.PlayerID, homeSystem)
		if err != nil {
			return "", false // scan error → fall through to a buy (fail-closed)
		}
		return symbol, ok
	}
	if h.reclaimer == nil {
		return "", false
	}
	symbol, ok, err := h.reclaimer.FindReclaimable(ctx, cmd.PlayerID)
	if err != nil {
		return "", false // scan error → fall through to a buy (fail-closed)
	}
	return symbol, ok
}

// depotHullPrice resolves the buy-fallback price + yard for a depot role's hull.
//
// STOCKER is home-scoped (sp-fihvy, RULINGS #14): only a yard IN homeSystem may price the hull, via
// h.depotPrice.NextHullPriceForHome — never the fleet-wide h.price reader, which could return a cheaper
// yard in a foreign system and buy a stocker hull the depot can never route home. Warehouse is
// unaffected: it keeps the shared h.price reader, byte-identical. A nil depotPrice or blank homeSystem
// falls back to the shared reader too, so the stocker path degrades to its historical behavior until
// fully wired.
func (h *RunContractScalerHandler) depotHullPrice(ctx context.Context, cmd *RunContractScalerCommand, role contractscaler.UnitRole, homeSystem string) (int64, string, bool) {
	if role == contractscaler.Stocker && h.depotPrice != nil && homeSystem != "" {
		price, yard, ok, err := h.depotPrice.NextHullPriceForHome(ctx, cmd.PlayerID, contractscaler.ScalerShipType, homeSystem)
		if err != nil {
			return 0, "", false // fail closed
		}
		return price, yard, ok
	}
	price, yard, ok, _ := h.price.NextHullPrice(ctx, cmd.PlayerID, contractscaler.ScalerShipType)
	return price, yard, ok
}

// growDepot places one hull into the depot for the given role via the grower (AddElement + launch). It
// is the single dispatch point mapping a plan role to its depot growth verb.
func (h *RunContractScalerHandler) growDepot(ctx context.Context, cmd *RunContractScalerCommand, role contractscaler.UnitRole, shipSymbol, hub string) error {
	order := DepotGrowOrder{PlayerID: cmd.PlayerID, ShipSymbol: shipSymbol, Hub: hub}
	switch role {
	case contractscaler.Warehouse:
		return h.grower.GrowWarehouse(ctx, order)
	case contractscaler.Stocker:
		return h.grower.GrowStocker(ctx, order)
	}
	return fmt.Errorf("contract scaler: not a depot role: %v", role)
}

// clampTake is the per-role budget cut: as many of a role's target as the remaining ceiling budget
// allows, never negative. remaining is kept >= 0 by the caller (each take is <= remaining).
func clampTake(target, remaining int) int {
	if remaining < target {
		if remaining < 0 {
			return 0
		}
		return remaining
	}
	return target
}

// planHub is the warehouse/stocker anchor — the central hub the fixed plan homes the depot bundle at
// (the top-demand park). "" when the plan has no depot bundle (a hub-less era).
func planHub(plan []contractscaler.PlanUnit) string {
	for _, unit := range plan {
		if unit.Role == contractscaler.Warehouse {
			return unit.Target
		}
	}
	return ""
}

// tryReclaim runs the zero-spend reuse tier for one ramp slot: find ONE idle undedicated cargo-capable
// hull and re-dedicate+home it into the contract fleet. Returns true ONLY when a hull was actually
// reclaimed (advance the ramp, no spend). Returns false — proceed to the buy path — when the tier is
// unwired, hits a scan error (fail-closed), finds no free hull, or the re-dedicate fails (the hull was
// NOT taken, so a buy is safe and cannot double-count). Dry-run logs the candidate and never actuates.
func (h *RunContractScalerHandler) tryReclaim(ctx context.Context, cmd *RunContractScalerCommand, armed *armedPlan) bool {
	if h.reclaimer == nil {
		return false // reuse tier unwired → byte-identical to the buy-only ramp
	}
	logger := common.LoggerFromContext(ctx)
	symbol, ok, err := h.reclaimer.FindReclaimable(ctx, cmd.PlayerID)
	if err != nil {
		logger.Log("INFO", fmt.Sprintf("Contract scaler: reuse scan failed (%v) — falling through to buy", err), map[string]interface{}{
			"action": "contract_scaler_reuse_scan_error", "container_id": cmd.ContainerID,
		})
		return false // fail-closed: a scan error never blocks the ramp, it just skips reuse
	}
	if !ok {
		return false // no free hull → buy
	}
	if cmd.DryRun {
		logger.Log("WARN", fmt.Sprintf("Contract scaler WOULD RECLAIM %s (dry-run, no spend)", symbol), map[string]interface{}{
			"action": "contract_scaler_would_reclaim", "container_id": cmd.ContainerID, "ship_symbol": symbol,
		})
		return false // observe-only: never actuate under dry-run (the buy path also holds on dry-run)
	}
	if err := h.reclaimer.Reclaim(ctx, ReclaimOrder{
		PlayerID:        cmd.PlayerID,
		ShipSymbol:      symbol,
		StandbyStations: armed.standbyStations,
	}); err != nil {
		logger.Log("WARN", fmt.Sprintf("Contract scaler: reclaim of %s failed (%v) — falling through to buy", symbol, err), map[string]interface{}{
			"action": "contract_scaler_reclaim_error", "container_id": cmd.ContainerID, "ship_symbol": symbol,
		})
		return false // re-dedicate failed → hull NOT taken → buy is safe
	}
	logger.Log("INFO", fmt.Sprintf("Contract scaler RECLAIMED %s into the contract fleet (no spend)", symbol), map[string]interface{}{
		"action": "contract_scaler_reclaimed", "container_id": cmd.ContainerID, "ship_symbol": symbol,
	})
	return true
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
	// The standby set is the SAME ≤6 fixed placement slots the buy sequence uses (TopDeliverySlots),
	// so the homing zips hulls onto exactly the parks the scaler bought against — one slot set, no
	// drift. Not all central parks (the previous set that let idle hulls spread demand-ranked and pile).
	armed := &armedPlan{
		plan:            contractscaler.BuildPlan(roles, demand),
		standbyStations: contractscaler.TopDeliverySlots(roles, demand),
	}
	logAnchorMisses(ctx, cmd.ContainerID, roles.Anchors)
	h.mu.Lock()
	h.plans[cmd.ContainerID] = armed
	h.mu.Unlock()
	return armed, nil
}

// logAnchorMisses WARNs once per arm when this era's charted template failed to produce one of
// the era-invariant standby anchors (contractscaler/anchors.go). Those slots fail OPEN to the
// demand-ranked central set, which is deliberate but SILENT — without this line a changed
// generator template looks exactly like a healthy era, and the analyst never learns to re-rank
// the slot from the contract corpus. A fully resolved era logs nothing.
func logAnchorMisses(ctx context.Context, containerID string, anchors contractscaler.EraAnchors) {
	misses := anchors.Misses()
	if len(misses) == 0 {
		return
	}
	common.LoggerFromContext(ctx).Log("WARN", fmt.Sprintf(
		"Contract scaler: this era charted no %v standby anchor(s) — those placement slots fall back to the demand-ranked central set; re-rank them from the contract corpus",
		misses), map[string]interface{}{
		"action": "contract_standby_anchor_miss", "container_id": containerID, "missing_anchors": misses,
	})
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
