package commands

import (
	"context"
	"fmt"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// GateTopologyResolver resolves this era's terminal factory for a gate material — the waypoint
// that EXPORTS it. *services.GateTopology satisfies it. Only TerminalFactory is declared: the
// delivery fleet buys terminal OUTPUT and never walks the recipe graph, so IsRaw/Inputs are
// deliberately out of this interface (they are also unsound — the recipe map is cyclic and
// !hasRecipe is false for every real raw material, bead sp-4irrr).
type GateTopologyResolver interface {
	TerminalFactory(ctx context.Context, good, systemSymbol string, playerID int) (*mfgServices.MarketLocatorResult, error)
}

// GateBuyer buys a gate material at a PINNED terminal factory, and answers whether a spend of a
// given size would breach the working-capital reserve.
// *services.ProductionExecutor satisfies it via BuyAtTerminalFactory and SpendFloorWouldBreach.
//
// THE PROBE IS ON THE BUYER, AND ON THIS INTERFACE, DELIBERATELY (sp-9eor3). It could have been a
// separate seam reached by type assertion, and that shape is exactly what makes a fix dormant: an
// assertion that fails leaves the precheck silently absent, the leg behaves as it did before, and
// nothing anywhere reports that the guard is not being consulted. Declaring it HERE makes it a
// compile-time obligation of being a GateBuyer at all — every buyer has one, no wiring step can
// omit it, and there is no arming seam between the fix and the running fleet.
//
// Putting it on the BUYER rather than beside it also keeps the pre-dispatch answer and the
// commit-time answer sourced from the SAME object: the thing that will refuse the spend is the
// thing asked to predict it.
type GateBuyer interface {
	BuyAtTerminalFactory(ctx context.Context, ship *navigation.Ship, good string, source *mfgServices.MarketLocatorResult, units int, systemSymbol string, playerID int, opContext *shared.OperationContext) (*mfgServices.ProductionResult, error)
	// SpendFloorWouldBreach reports whether committing projectedCost right now would drop treasury
	// below the reserve, and returns the reserve enforced. A PURE READ that spends, reserves and
	// releases nothing — it can only DECLINE a step earlier, never admit one the commit-time guard
	// would refuse.
	SpendFloorWouldBreach(ctx context.Context, playerID, projectedCost int) (bool, int)
}

// gateDelivery is the drain's delivery-fleet collaborator set plus the live buy policy.
//
// The policy is held HERE, on the handler, rather than rebuilt per leg because its PAUSE STATE
// must persist across legs — that state IS the hysteresis. A leg that builds a fresh policy
// resets `paused` and silently degrades the two floors back to the single threshold that chatters
// (pause, one unit regenerates, resume, immediately deplete), which is the exact defect the
// second floor exists to prevent. It is rebuilt ONLY when the operator changes the floors, which
// resets the pause state deliberately: the rule changed, so re-deriving under the new rule costs
// one tick and never a spend.
type gateDelivery struct {
	topology GateTopologyResolver
	buyer    GateBuyer

	mu     sync.Mutex
	policy *gate.BuyPolicy
	buy    string // the floors the cached policy was built from
	resume string
}

func (g *gateDelivery) enabled() bool { return g != nil && g.topology != nil && g.buyer != nil }

// gateLegRole is THE routing predicate: which gate leg (if any) will a lot with this claim
// identity actually run?
//
// It exists as ONE function because it has two callers that must never disagree — supplyTask,
// which routes to a leg, and the dispatch planner, which declines a hull on the strength of what
// that leg can do. When those drifted apart the result was D2: the decline was made about a leg
// the hull was not going to run, and a recoverable hull became permanently invisible.
//
// IT RETURNS THE ROLE, NOT A BOOLEAN, and that shape is load-bearing. The two callers ask
// DIFFERENT questions of the same fact:
//
//   - routing asks WHICH leg, and there are now two;
//   - the decline asks IS THIS THE DELIVERY ROLE, because wedgedAtFullHold ("full hold, nothing
//     aboard is a material whose bill is still open") is only sound for the delivery leg, whose
//     entire repertoire is flush-then-buy.
//
// A boolean widened to cover both roles would decline EVERY laden factory hull — their holds are
// full of fabrication inputs like IRON_ORE, which are never bill materials — and the factory fleet
// would never run once. Returning the role lets the decline stay exactly as narrow as it was while
// routing widens, with one function still shared, so a phase-4 widening touches one place.
//
// Each role's leg is gated on ITS OWN collaborators. With a role's leg unwired, a hull carrying
// that tag takes the shared fabricate path and recovers there like any other, so the decline is
// never made about a leg that is not going to run. That is the optional-collaborator pattern the
// drain already uses, NOT a feature flag: main.go wires both unconditionally.
//
// It takes the resolved IDENTITY rather than the hull deliberately. The dispatch planner resolves
// it from the live hull (claimIdentityFor) because that is where the lot's identity is minted; the
// worker must use the lot's FROZEN claimIdentity, because it runs long after the planning tick and
// claims under that exact value. Re-deriving from the hull inside the worker would let routing
// disagree with what was actually claimed — the hazard constructionLot.claimIdentity exists to
// prevent.
//
// The zero Role is RoleDelivery, so the boolean is the ONLY safe discriminator for "no role": a
// caller that reads the role without checking ok reads a legacy hull as a delivery hull.
func (h *RunConstructionCoordinatorHandler) gateLegRole(claimIdentity string) (gate.Role, bool) {
	role, ok := gate.ParseFleetTag(claimIdentity)
	if !ok {
		return 0, false // legacy, foreign, or undedicated: no role, no gate leg
	}
	if role == gate.RoleFactory {
		return role, h.factory.enabled()
	}
	return role, h.gate.enabled()
}

// policyFor returns the buy policy for the floors CURRENTLY on the pipeline row, rebuilding it
// only when they changed.
func (g *gateDelivery) policyFor(buyFloor, resumeFloor string) *gate.BuyPolicy {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.policy == nil || g.buy != buyFloor || g.resume != resumeFloor {
		g.policy = gate.NewBuyPolicy(shared.SupplyLevel(buyFloor), shared.SupplyLevel(resumeFloor))
		g.buy, g.resume = buyFloor, resumeFloor
	}
	return g.policy
}

// SetGateDelivery wires the delivery fleet: phase 1's role-based topology and the pinned
// terminal-factory buy. OPTIONAL, following SetTreeResolver/SetInventorySource — a nil in either
// argument leaves the delivery leg unwired and the drain behaves exactly as before, which keeps
// every existing coordinator test unchanged.
func (h *RunConstructionCoordinatorHandler) SetGateDelivery(topology GateTopologyResolver, buyer GateBuyer) {
	if topology == nil || buyer == nil {
		return
	}
	h.gate = &gateDelivery{topology: topology, buyer: buyer}
}

// GateFactoryTopology is the FACTORY role's view of the era's topology: where a good is exported
// (which doubles as the RAW SOURCE role — a raw good is bought from whatever exports it, and the
// two roles differ in the caller's intent, not in the resolution) plus the recipe seam.
// *services.GateTopology satisfies it.
//
// IsRaw/Inputs are GateTopology's, NEVER goods.IsRawMaterial/goods.GetRequiredInputs. The pairs
// diverge in CONTENT since sp-4irrr: Inputs("IRON_ORE") is nil while GetRequiredInputs is
// {"EXPLOSIVES"}, so a swap would descend an ore into
// IRON_ORE -> EXPLOSIVES -> LIQUID_HYDROGEN -> MACHINERY -> IRON -> IRON_ORE and stop terminating.
type GateFactoryTopology interface {
	TerminalFactory(ctx context.Context, good, systemSymbol string, playerID int) (*mfgServices.MarketLocatorResult, error)
	IsRaw(good string) bool
	Inputs(good string) []string
	// ImportSupply reports how short a RESOLVED factory is of one of its INPUTS — that factory's
	// own IMPORT supply level for the good (sp-q9um6). It is a third quantity, distinct from both
	// supply levels TerminalFactory returns: the source's EXPORT supply of the input, and the
	// target's EXPORT supply of its own output. Neither of those answers "which input is this
	// factory shortest of", and ranking on either is wrong in a way that looks right.
	//
	// The boolean is load-bearing: false means "no basis to rank" (unscanned market, absent
	// listing, non-IMPORT listing, null supply), which must leave the caller's existing order
	// untouched rather than sorting the unreadable to either end.
	ImportSupply(ctx context.Context, factoryWaypoint, good string, playerID int) (string, bool)
}

// GateFeeder delivers a hull's inputs INTO a pinned factory.
// *services.ProductionExecutor satisfies it via FeedFactory.
type GateFeeder interface {
	FeedFactory(ctx context.Context, ship *navigation.Ship, destination *mfgServices.MarketLocatorResult, inputs []string, playerID int, opContext *shared.OperationContext) (*mfgServices.FeedResult, error)
}

// Compile-time enforcement of the four conformance claims above, following the same guard
// gate_topology.go puts on its own marketResolver seam. Without these lines each "satisfies it"
// comment is an unchecked assertion: a signature change on either concrete type would silently
// make it false, and nothing would fail until the composition root tried to wire the two together.
//
// That was not hypothetical for the FACTORY pair. Until main.go wired SetGateFactory, no call site
// anywhere passed a *GateTopology as a GateFactoryTopology or a *ProductionExecutor as a
// GateFeeder — the handler's own tests use fixtures — so the compiler checked neither claim. These
// assertions move the check into the package that DECLARES the seam, where a break names the
// interface rather than surfacing as a type error a thousand lines into main.
var (
	_ GateTopologyResolver = (*mfgServices.GateTopology)(nil)
	_ GateFactoryTopology  = (*mfgServices.GateTopology)(nil)
	_ GateBuyer            = (*mfgServices.ProductionExecutor)(nil)
	_ GateFeeder           = (*mfgServices.ProductionExecutor)(nil)
)

// gateFactory is the drain's FACTORY-fleet collaborator set.
//
// It carries its own buyer rather than reaching into gateDelivery's, so the two roles are
// independently wireable and neither's absence nil-panics the other. In production both are the
// SAME *ProductionExecutor — which is the point: one spend primitive, one set of money guards.
type gateFactory struct {
	topology GateFactoryTopology
	buyer    GateBuyer
	feeder   GateFeeder
}

func (g *gateFactory) enabled() bool {
	return g != nil && g.topology != nil && g.buyer != nil && g.feeder != nil
}

// SetGateFactory wires the factory fleet: phase 1's role-based topology (which also answers the
// recipe seam the feed walk needs), phase 2's pinned terminal-factory buy, and the feed terminal.
//
// OPTIONAL, following SetGateDelivery/SetTreeResolver — a nil in any argument leaves the feeding
// leg unwired and a factory-tagged hull keeps taking the shared path, so every existing
// coordinator test is unchanged. This is NOT a feature flag: the wiring task wires it
// unconditionally in main.go, so it ships ARMED. It is the same optional-collaborator pattern the
// drain already uses to keep its own fixtures buildable.
func (h *RunConstructionCoordinatorHandler) SetGateFactory(topology GateFactoryTopology, buyer GateBuyer, feeder GateFeeder) {
	if topology == nil || buyer == nil || feeder == nil {
		return
	}
	h.factory = &gateFactory{topology: topology, buyer: buyer, feeder: feeder}
}

// SetSourceCooldown wires the fleet-shared compression ledger so the feeding leg paces itself
// against sources it is draining, accrued from OUR OWN traded volume and decayed over time.
//
// Volume, not price, is what makes it safe: a baseline derived from the ask would rise as we push
// the ask up and the guard would stop firing. This one cannot — no observed price enters it.
//
// Optional collaborator, like its siblings — nil leaves the leg byte-identical.
func (h *RunConstructionCoordinatorHandler) SetSourceCooldown(ledger *trading.LaneCooldownLedger) {
	h.sourceCooldown = ledger
}

// gateMaterial is one gate material WITH its live market quote. The quote is carried, not
// re-fetched: the leg needs the waypoint and trade volume to buy, the supply level to decide, and
// re-reading between the decision and the buy would let the two disagree.
//
// It is projected down to gate.Material before planning the fill, so the fill arithmetic stays
// pure and cannot accidentally depend on a price or a waypoint symbol.
type gateMaterial struct {
	good      string
	remaining int
	// committed is how many of those remaining units are ALREADY PAID FOR and sitting in another
	// gate hull's hold. Carried alongside remaining, not subtracted into it, so the leg can still
	// tell "the site no longer wants this" from "the site wants it and we already bought it".
	committed int
	source    *mfgServices.MarketLocatorResult // nil when this era exports the good nowhere
}

// flushOnHandGateMaterials unloads every gate material the hull ALREADY HOLDS, before this leg
// sizes or buys anything, and reports the hold units it freed.
//
// This is the delivery role's ONLY unload path. supplyTask routes a delivery lot here ahead of the
// shared deliver-on-hand phase, so a hull that arrives laden has no other way to empty — and a hull
// that arrives FULL cannot buy either, because PlanFill returns zero stops for any capacity <= 0.
// It would then defer, be re-discovered, be re-paired with the same material by haulerPool.take,
// and repeat forever while the drain reports RUNNING.
//
// It reuses the SAME producer terminal the buy loop and deliverOnHandCargo use — no second unload
// path, no duplicated navigation. A material whose bill is already MET is not flown anywhere: the
// site would reject it. That case is logged loudly rather than silently skipped, because a hull
// holding material nobody needs is stuck on capacity this leg cannot recover.
// It returns the freed units PER GOOD, not one total. The per-good grain is load-bearing for the
// buy sizing: only the LOT'S OWN material has its delivery recorded into the pipeline counter
// here, so for every OTHER material the bill this leg reads is stale by exactly the units this
// flush unloaded — and sizing a purchase against that stale bill re-buys them.
func (h *RunConstructionCoordinatorHandler) flushOnHandGateMaterials(
	ctx context.Context,
	leg *supplyLeg,
	pipeline *manufacturing.ManufacturingPipeline,
	playerID shared.PlayerID,
) map[string]int {
	logger := common.LoggerFromContext(ctx)
	task := leg.task()
	freed := make(map[string]int, len(pipeline.Materials()))

	for _, target := range pipeline.Materials() {
		good := target.TradeSymbol()
		aboard := onHandUnits(leg.ship, good)
		if aboard == 0 {
			continue
		}
		if target.RemainingQuantity() <= 0 {
			logger.Log("WARNING", fmt.Sprintf("Gate delivery: %s is holding %d %s whose construction bill is already MET — the site will not accept it, so that hold space stays occupied until the hull is unloaded elsewhere", leg.ship.ShipSymbol(), aboard, good), map[string]interface{}{
				"ship": leg.ship.ShipSymbol(), "good": good, "units": aboard,
			})
			continue
		}

		units, derr := h.producer.DeliverToConstructionSite(ctx, leg.ship.ShipSymbol(), good, task.ConstructionSite(), playerID)
		if derr != nil {
			// A 4219 is NOT a generic delivery error and must never be handled as one. It says the
			// hull does not actually hold what the cached row claims — a PHANTOM. Left unresynced
			// the row still reads laden next tick, the flush 4219s again, capacity stays 0, and the
			// hull is wedged forever with nothing in the system to correct it: this role returns at
			// supplyTask's gate-delivery branch and never reaches handlePhantomCargo/resyncShipCargo,
			// which is how every other role recovers. writeBackDeliveredCargo is explicitly
			// best-effort, and this leg's own buy loop can mint the phantom, so this is reachable.
			if isPhantomCargoSupplyError(derr) {
				logger.Log("WARNING", fmt.Sprintf("Gate delivery: %s reports %d %s aboard but the server says otherwise (API 4219, PHANTOM cargo) — resyncing the hull from server truth so the next leg reads a real hold instead of re-attempting this unload forever", leg.ship.ShipSymbol(), aboard, good), map[string]interface{}{
					"ship": leg.ship.ShipSymbol(), "good": good, "units": aboard,
				})
				h.resyncShipCargo(ctx, leg.ship.ShipSymbol(), playerID)
				continue
			}
			// Never fatal: the buy loop below still has whatever hold is free, and the next leg
			// retries the flush. Recorded so a hull that cannot unload is diagnosable.
			logger.Log("WARNING", fmt.Sprintf("Gate delivery: could not unload the %d %s already aboard %s at %s — that hold space is unavailable this leg: %v", aboard, good, leg.ship.ShipSymbol(), task.ConstructionSite(), derr), map[string]interface{}{
				"ship": leg.ship.ShipSymbol(), "good": good, "units": aboard,
			})
			continue
		}
		if units == 0 {
			continue
		}

		freed[good] += units
		logger.Log("INFO", fmt.Sprintf("Gate delivery: unloaded %d %s already aboard %s to %s before buying — freeing hold for this leg", units, good, leg.ship.ShipSymbol(), task.ConstructionSite()), map[string]interface{}{
			"good": good, "units": units, "construction_site": task.ConstructionSite(), "ship": leg.ship.ShipSymbol(),
		})
		// Only the LOT'S OWN material counts toward this task — both for the pipeline counter and for
		// leg.delivered. leg.delivered is what completeSupply LOGS against task.Good() and what
		// completeOrDefer decides complete-vs-defer on, so crediting another material's units to it
		// would report "Supplied 80 FAB_MATS" for a trip that moved 40, and would COMPLETE a FAB_MATS
		// task that moved zero FAB_MATS. Every sibling supply path accumulates only task.Good() units;
		// this is that same contract. The other material is raised from the live site by the next
		// tick's reconcilePipelinesFromSite, so nothing is lost by not counting it here.
		if good == task.Good() {
			leg.delivered += units
			leg.pipeline = h.recordDelivery(ctx, task, units)
		}
	}
	return freed
}

// deliverGateLeg runs one delivery hull's leg: decide, record, fill, buy, deliver.
//
// THE FLOORS ARE READ OFF THE PIPELINE ROW HERE, ON EVERY LEG. That per-leg read is what makes
// the knob live; hoisting it to handler construction would silently turn a pattern-C tunable into
// a restart-only one, which looks identical from the outside until an operator tunes it and
// nothing happens.
//
// EVERY exit funnels through the SAME completion machinery the rest of supplyTask uses
// (completeSupply / completeOrDefer). A leg that simply returns leaves its task EXECUTING
// forever: nothing persists a terminal status, nothing re-stages the next single load, the ready
// queue drains to nothing, and the drain goes quiet while still reporting RUNNING — a stall
// indistinguishable from a finished gate. A recoverable stand-down PARKS (completeOrDefer) rather
// than failing, so a supply pause never spends a retry and the SupplyMonitor re-activates the leg.
//
// Reports whether the leg delivered anything.
func (h *RunConstructionCoordinatorHandler) deliverGateLeg(
	ctx context.Context,
	cmd *RunConstructionCoordinatorCommand,
	systemSymbol string,
	lot constructionLot,
	playerID shared.PlayerID,
) bool {
	logger := common.LoggerFromContext(ctx)
	task := lot.task

	pipeline, err := h.pipelineRepo.FindByID(ctx, task.PipelineID())
	if err != nil || pipeline == nil {
		logger.Log("WARNING", fmt.Sprintf("Gate delivery: cannot read pipeline %s for %s — standing down this leg rather than buying against an unknown bill: %v", task.PipelineID(), task.Good(), err), nil)
		// Park, never leave EXECUTING: an unreadable pipeline is a transient condition, and a task
		// left in flight behind a returned worker is never picked up again.
		return h.completeOrDefer(ctx, &supplyLeg{lot: lot, ship: lot.ship})
	}

	// ONE leg object, mutated as the sibling supply paths do (deliverOnHandCargo,
	// supplyFromWarehouse, sourceAndDeliverRemainder all do exactly this). Carrying it rather than
	// building a fresh one per exit is what keeps the running delivered total and the POST-delivery
	// pipeline in front of the completion machinery: completeSupply sizes replenishment off
	// leg.pipeline, and a pre-delivery snapshot enqueues a refill for a bill that was just met.
	leg := &supplyLeg{lot: lot, ship: lot.ship, pipeline: pipeline}

	// FLUSH FIRST, before anything is sized or bought. This role skips the shared
	// deliver-on-hand phase (supplyTask routes here ahead of it), so this is the ONLY unload path a
	// delivery hull has. Without it a hull that arrives laden — a delivery that errored after a
	// successful buy, a task deadline, a restart mid-leg — has free capacity 0, PlanFill returns
	// ZERO stops for any capacity <= 0, and the leg defers. The hull is then re-discovered every
	// tick and re-paired with the same material by haulerPool.take (which PREFERS a hull already
	// holding the good), burning a dispatch slot forever while reporting RUNNING. That is the
	// amendment's own failure class moved from the task to the hull.
	//
	// It runs BEFORE the pause check on purpose: cargo already aboard has zero market impact and
	// always advances the gate, so a paused fleet must still unload — the same rule PHASE 1 of
	// supplyTask states for every other role.
	freed := h.flushOnHandGateMaterials(ctx, leg, pipeline, playerID)

	// Size the rest of the leg against the POST-flush bill: the flush just met part of it, and
	// planning against the pre-flush remaining buys toward a bill this leg already closed.
	billSource := pipeline
	if leg.pipeline != nil {
		billSource = leg.pipeline
	}

	// LIVE floors, re-read this leg.
	policy := h.gate.policyFor(pipeline.DeliveryBuyFloor(), pipeline.DeliveryResumeFloor())

	// NET OUT WHAT IS ALREADY BOUGHT, before a single unit is sized.
	//
	// THIS NETTING IS NEEDED HERE AND NOT ONLY IN THE DISPATCH PLANNER. A delivery trip is MIXED:
	// it buys every gate material on the pipeline, not just the one its lot was dispatched for.
	// The planner's budget governs which lots EXIST and never reaches this sizing, so without a
	// second netting a fully committed material is re-bought by the first leg that goes out for
	// anything else.
	//
	// THIS hull is excluded BY SYMBOL, never by subtracting its cargo. Its load was just flushed
	// into billSource, so counting it again nets the same units out twice — and the cached *Ship
	// this leg holds is deliberately NOT updated by DeliverToConstructionSite, so subtracting its
	// cargo would subtract a PRE-flush figure from a POST-flush fold and under-count, which is the
	// over-buying direction. Whatever the flush could not unload still occupies the hold, which
	// the capacity arithmetic below already accounts for.
	self := lot.ship.ShipSymbol()
	commitment := h.commitUnits(
		h.commitmentHulls(ctx, cmd, playerID, systemSymbol),
		task.PipelineID(),
		pipelineMaterialGoods(billSource),
		func(ship *navigation.Ship) bool { return ship.ShipSymbol() == self },
	)

	// Resolve each material's terminal factory and quote.
	materials := make([]gateMaterial, 0, len(billSource.Materials()))
	for _, target := range billSource.Materials() {
		m := gateMaterial{good: target.TradeSymbol(), remaining: target.RemainingQuantity(), committed: commitment[target.TradeSymbol()]}
		// ADD BACK what this hull just flushed of ANOTHER material. Only task.Good()'s delivery is
		// recorded into the pipeline counter (a second material would need its own task and
		// pipeline lock, and would double-count against the next tick's site reconcile), so for
		// every other good billSource is stale by exactly the units the flush unloaded — and
		// sizing against that stale bill buys them a second time.
		if m.good != task.Good() {
			m.committed += freed[m.good]
		}
		warnOnOvershoot(ctx, m.good, m.remaining, m.committed)
		if m.remaining > 0 && m.committed >= m.remaining {
			logInFlightSkip(ctx, m.good, m.remaining, m.committed)
		}
		source, terr := h.gate.topology.TerminalFactory(ctx, m.good, systemSymbol, cmd.PlayerID)
		if terr != nil || source == nil {
			// A refusal, never a substitution: sending a hull to some other waypoint is how cargo
			// ends up somewhere that cannot accept it. Recorded so the miss is diagnosable.
			logger.Log("WARNING", fmt.Sprintf("Gate delivery: no terminal factory for %s in %s this era — skipping it on this trip: %v", m.good, systemSymbol, terr), map[string]interface{}{
				"good": m.good, "system": systemSymbol,
			})
			materials = append(materials, m)
			continue
		}
		m.source = source
		materials = append(materials, m)
	}

	// DECIDE and RECORD, per material. Every ruling is logged: a paused fleet and an idle one must
	// not look identical, which is the whole reason this design exists.
	goodsSeen := make([]string, 0, len(materials))
	paused := make(map[string]bool, len(materials))
	for _, m := range materials {
		if m.source == nil {
			continue // unresolvable: not a policy decision, and must not count toward a fleet pause
		}
		goodsSeen = append(goodsSeen, m.good)
		decision := policy.Decide(m.good, m.source.WaypointSymbol, shared.ParseSupplyLevel(m.source.Supply))
		paused[m.good] = decision.Paused
		level := "INFO"
		if decision.Paused {
			level = "WARNING"
		}
		// A SUSPECTED-STUCK PAUSE IS AN ERROR, NOT A WARNING (sp-c9wuu). The ordinary pause is a
		// WARNING and reads as healthy patience — correctly, most of the time. That is exactly why
		// the stuck case has to leave the band the healthy case occupies: the 7.5-hour deadlock was
		// invisible because it logged the same reassuring level and shape as every legitimate pause,
		// and was twice read as "thin market, being patient".
		action := "gate_delivery_paused"
		if decision.SuspectedStuck {
			level = "ERROR"
			action = "gate_delivery_pause_suspected_stuck"
		}
		if decision.ResumedByTimeout {
			level = "WARNING"
			action = "gate_delivery_resumed_by_timeout"
		}
		logger.Log(level, decision.LogLine(), map[string]interface{}{
			"good": decision.Good, "factory": decision.Factory, "supply": string(decision.Supply),
			"buy_floor": string(decision.BuyFloor), "resume_floor": string(decision.ResumeFloor),
			"paused": decision.Paused, "held_at_buy_floor_minutes": int(decision.HeldAtBuyFloor.Minutes()),
			"suspected_stuck": decision.SuspectedStuck, "resumed_by_timeout": decision.ResumedByTimeout,
			"action": action,
		})
	}

	// The FLEET is paused only when EVERY material is: a hull fills greedily from any eligible
	// material, so one pause still leaves useful work.
	if policy.FleetPaused(goodsSeen) {
		logger.Log("WARNING", fmt.Sprintf("Gate delivery fleet PAUSED: every gate material is below its buy floor — %s stands down this leg and spends nothing", lot.ship.ShipSymbol()), map[string]interface{}{
			"ship": lot.ship.ShipSymbol(), "materials": len(goodsSeen),
		})
		// A paused fleet PARKS its task for the SupplyMonitor to re-activate. Failing it would
		// charge a retry to a market condition that is not the leg's fault. A leg that already
		// flushed a hold completes instead — completeOrDefer honours leg.delivered.
		return h.completeOrDefer(ctx, leg)
	}

	// Project to the pure fill planner and PLAN the mixed load.
	//
	// The freed units are added back explicitly rather than re-read: DeliverToConstructionSite
	// reloads the hull itself and writes the emptied hold back through the repository, so THIS
	// cached *Ship is deliberately not updated by it and would still report a full hold. Units the
	// site accepted are exactly the units that left the hold, so this is the same number a re-read
	// would return, without a second query.
	capacity := totalUnits(freed)
	if cargo := lot.ship.Cargo(); cargo != nil {
		capacity += cargo.Capacity - cargo.Units
	}
	planInput := make([]gate.Material, 0, len(materials))
	for _, m := range materials {
		fm := gate.Material{Good: m.good, Remaining: m.remaining, Committed: m.committed, Paused: paused[m.good]}
		if m.source != nil {
			fm.TradeVolume = m.source.TradeVolume
		}
		planInput = append(planInput, fm)
	}
	trip := gate.PlanFill(capacity, planInput)
	logger.Log("INFO", trip.LogLine(), map[string]interface{}{
		"ship": lot.ship.ShipSymbol(), "capacity": trip.Capacity, "loaded": trip.Loaded(),
		"stops": len(trip.Stops), "skips": len(trip.Skips),
	})
	if len(trip.Stops) == 0 {
		return h.completeOrDefer(ctx, leg)
	}

	// BUY each stop at its PINNED terminal factory, then deliver the whole hold to the gate.
	sources := make(map[string]*mfgServices.MarketLocatorResult, len(materials))
	for _, m := range materials {
		sources[m.good] = m.source
	}
	tripUnits := 0 // units of ANY gate material this leg moved, for the honest trip summary
	for _, stop := range trip.Stops {
		result, berr := h.gate.buyer.BuyAtTerminalFactory(ctx, lot.ship, stop.Good, sources[stop.Good], stop.Units, systemSymbol, cmd.PlayerID, h.operationContext(cmd))
		if berr != nil {
			logger.Log("WARNING", fmt.Sprintf("Gate delivery: buying %d %s at %s failed; delivering what is aboard: %v", stop.Units, stop.Good, sources[stop.Good].WaypointSymbol, berr), nil)
			continue
		}
		if result == nil || result.QuantityAcquired == 0 {
			continue // the money/price guards stopped the fill; nothing to deliver for this good
		}
		units, derr := h.producer.DeliverToConstructionSite(ctx, lot.ship.ShipSymbol(), stop.Good, task.ConstructionSite(), playerID)
		if derr != nil {
			// Same 4219 contract as the flush: a phantom means the cached hold is a lie, and this
			// role has no other resync path. Left uncorrected, the units just bought are recorded
			// aboard a hull the server says is empty, and the NEXT leg's flush inherits the wedge.
			if isPhantomCargoSupplyError(derr) {
				logger.Log("WARNING", fmt.Sprintf("Gate delivery: %s was rejected supplying %s as PHANTOM cargo (API 4219) — resyncing the hull from server truth rather than re-routing it to re-deliver", lot.ship.ShipSymbol(), stop.Good), map[string]interface{}{
					"ship": lot.ship.ShipSymbol(), "good": stop.Good, "construction_site": task.ConstructionSite(),
				})
				h.resyncShipCargo(ctx, lot.ship.ShipSymbol(), playerID)
				continue
			}
			logger.Log("WARNING", fmt.Sprintf("Gate delivery: delivering %s to %s failed: %v", stop.Good, task.ConstructionSite(), derr), nil)
			continue
		}
		tripUnits += units
		logger.Log("INFO", fmt.Sprintf("Gate delivery: supplied %d %s to %s via %s", units, stop.Good, task.ConstructionSite(), lot.ship.ShipSymbol()), map[string]interface{}{
			"good": stop.Good, "units": units, "construction_site": task.ConstructionSite(), "ship": lot.ship.ShipSymbol(),
		})
		// Only the LOT'S OWN material counts against this task — the pipeline counter AND
		// leg.delivered. A mixed trip's other material is picked up by reconcilePipelinesFromSite on
		// the next tick, which re-reads the LIVE site and raises the delivered counters from the
		// server — the authoritative source. Recording it here would need a second material's task
		// and pipeline lock, and would double-count against that reconcile.
		//
		// leg.delivered is NOT a trip total. completeSupply logs it against task.Good() and
		// completeOrDefer decides complete-vs-defer on it, so a cross-material total would report
		// another good's units under this one's name and could COMPLETE a task that moved none of its
		// own material. tripUnits carries the honest cross-material figure for the leg summary below.
		//
		// The returned pipeline REPLACES the leg's: recordDelivery does its own FindByID and the
		// production repository hands back a fresh aggregate, so the object read at the top of this
		// leg is a pre-delivery snapshot. Sizing replenishment off it enqueues a refill for a bill
		// this very delivery met, and nothing ever cleans that task up. Every sibling supply path
		// assigns the same way.
		if stop.Good == task.Good() {
			leg.delivered += units
			leg.pipeline = h.recordDelivery(ctx, task, units)
		}
	}

	// A MIXED trip moved more than this task's own material. Say so explicitly, or the only figures
	// an operator sees are this task's — and the other material's units would look unaccounted for
	// until the next tick's site reconcile raises them.
	if total := totalUnits(freed) + tripUnits; total > leg.delivered {
		logger.Log("INFO", fmt.Sprintf("Gate delivery: %s moved %d unit(s) to %s this leg, of which %d were %s (this task's material); the rest belong to other gate materials and are reconciled from the live site next tick", lot.ship.ShipSymbol(), total, task.ConstructionSite(), leg.delivered, task.Good()), map[string]interface{}{
			"ship": lot.ship.ShipSymbol(), "trip_units": total, "task_units": leg.delivered, "good": task.Good(),
		})
	}

	// Terminal outcomes go through the SAME completion path every other supply path uses. Skipping
	// it leaves the task EXECUTING forever: nothing re-stages the next load, and a silently-drained
	// ready queue is indistinguishable from a finished gate.
	if leg.delivered > 0 {
		return h.completeSupply(ctx, leg, leg.delivered)
	}
	return h.completeOrDefer(ctx, leg)
}
