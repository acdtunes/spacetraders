package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// COMMITTED UNITS — how much of a construction bill is already paid for.
//
// A site's delivered counter moves only when the SERVER accepts a supply, so a unit bought and
// still riding in a hold is invisible to it, and anything sizing a purchase off the raw bill buys
// those units again for cargo the site then refuses (API 4801).
//
// The commitment is DERIVED every tick from the holds of the hulls bound to this gate, never
// carried in memory, so it holds across a restart and across the expiry of a worker registration.
// The in-memory reservation is combined in as the one thing cargo cannot express — a worker
// dispatched but not yet at the market has bought nothing — and the two are taken per hull as a
// MAX, never a sum, so a worker that has already spent is counted once.
//
// Fail-CLOSED throughout (RULINGS #4): every ambiguity resolves toward counting a unit as
// committed, i.e. toward NOT buying. Over-counting costs one tick of under-dispatch; under-
// counting costs the purchase price of a hull-load.

// gateCommitments is one tick's committed-unit arithmetic, per GOOD. Two numbers, because
// answering both questions with one either over-buys or deadlocks:
//
//   - total answers "how many units may I still BUY" — every commitment counts, including cargo
//     aboard a hull this tick is about to dispatch, because that cargo is already bought.
//   - undispatchable answers "how many units still need a HULL this tick" — cargo aboard a hull
//     in this tick's own pool is EXCLUDED, because dispatching that hull is how it gets
//     delivered. Netting it out of the lot count too would leave a laden hull with no lot minted
//     to unload it, and the bill would never close.
type gateCommitments struct {
	total          map[string]int
	undispatchable map[string]int
}

// unitsFor reports the committed units for good — 0 for a good nothing is committed to.
func (c gateCommitments) unitsFor(good string) int {
	if c.total == nil {
		return 0
	}
	return c.total[good]
}

// undispatchableFor reports the units committed to hulls this tick cannot dispatch.
func (c gateCommitments) undispatchableFor(good string) int {
	if c.undispatchable == nil {
		return 0
	}
	return c.undispatchable[good]
}

// commitmentHulls is the hull set whose holds count against a gate bill: every hull carrying a
// GATE fleet tag or this drain's own launch identity, plus every hull one of this drain's supply
// workers holds. The registration arm is not redundant — the drain also claims OPPORTUNISTIC
// undedicated hulls when dedicated capacity is short.
//
// SYSTEM SCOPING REJECTS ON POSITIVE EVIDENCE ONLY. A hull whose location is unreadable is KEPT.
// Discovery can fail closed by declining to dispatch an unlocatable hull; this path fails closed
// by COUNTING it, because dropping it licenses a second purchase for cargo that may be aboard.
func (h *RunConstructionCoordinatorHandler) commitmentHulls(ctx context.Context, cmd *RunConstructionCoordinatorCommand, playerID shared.PlayerID, systemSymbol string) []*navigation.Ship {
	logger := common.LoggerFromContext(ctx)
	all, err := h.shipRepo.FindAllByPlayer(ctx, playerID)
	if err != nil {
		// Fail-closed is not available: with no hull list there is no commitment to net out, and
		// refusing to plan would stall the gate. Say so loudly instead.
		logger.Log("WARNING", fmt.Sprintf("Construction drain: could not read the fleet to net out gate material already bought and in flight — this tick sizes its buys off the RAW bill and may re-buy units already in a hold: %v", err), nil)
		return nil
	}
	own := h.dedicatedFleet(cmd)
	hulls := make([]*navigation.Ship, 0, len(all))
	for _, ship := range all {
		if ship == nil {
			continue
		}
		tag := ship.DedicatedFleet()
		if !gate.IsGateFleetTag(tag) && tag != own && !h.supplies.holds(ship.ShipSymbol()) {
			continue
		}
		if outOfSystem(ship, systemSymbol) {
			continue
		}
		hulls = append(hulls, ship)
	}
	return hulls
}

// outOfSystem reports whether ship is POSITIVELY known to sit outside systemSymbol. An unknown
// location, or an unset filter, is never "out" — see commitmentHulls.
func outOfSystem(ship *navigation.Ship, systemSymbol string) bool {
	if systemSymbol == "" {
		return false
	}
	loc := ship.CurrentLocation()
	if loc == nil {
		return false
	}
	return shared.ExtractSystemSymbol(loc.Symbol) != systemSymbol
}

// commitUnits folds a hull set into per-good committed units for one pipeline's materials. skip
// excludes a hull (nil excludes none) — the seam separating "already bought" from "already bought
// AND unreachable by this tick".
//
// PER HULL, PER GOOD, IT TAKES A MAX AND NOT A SUM. A worker holds a buy RESERVATION (what it may
// still spend) and its hull holds CARGO (what it already spent). Before the buy only the
// reservation exists; after it, only the cargo should count. Summing them double-counts every
// worker on its way home and starves the fleet of dispatch.
func (h *RunConstructionCoordinatorHandler) commitUnits(ships []*navigation.Ship, pipelineID string, goods []string, skip func(*navigation.Ship) bool) map[string]int {
	committed := make(map[string]int, len(goods))
	for _, good := range goods {
		committed[good] = 0
	}
	// Every hull this fold has an opinion about — folded or deliberately skipped — so the sweep
	// below adds back only the workers it genuinely never saw.
	accounted := make(map[string]bool, len(ships))
	for _, ship := range ships {
		if ship == nil {
			continue
		}
		accounted[ship.ShipSymbol()] = true
		if skip != nil && skip(ship) {
			continue
		}
		reservation, material := h.supplies.reservationFor(ship.ShipSymbol())
		for _, good := range goods {
			units := onHandUnits(ship, good)
			// A hull's ONE reservation names ONE material, so it is credited only to that
			// material. A mixed-load hull still has its other goods counted from the cargo arm.
			if material == pipelineMaterialKey(pipelineID, good) && reservation > units {
				units = reservation
			}
			committed[good] += units
		}
	}
	// Fail-closed floor: spend authority held against a hull this fold never saw.
	for material, units := range h.supplies.reservationsExcept(accounted) {
		for _, good := range goods {
			if material == pipelineMaterialKey(pipelineID, good) {
				committed[good] += units
			}
		}
	}
	return committed
}

// pipelineMaterialKey is materialKey's decomposed form: the same pipeline+good identity built from
// parts rather than from a task. The two MUST agree — commitUnits matches a worker's registered
// material against it — so both go through one formatter.
func pipelineMaterialKey(pipelineID, good string) string {
	return pipelineID + "\x00" + good
}

// totalUnits sums a per-good unit map.
func totalUnits(byGood map[string]int) int {
	total := 0
	for _, units := range byGood {
		total += units
	}
	return total
}

// pipelineMaterialGoods lists a pipeline's construction materials by trade symbol.
func pipelineMaterialGoods(pipeline *manufacturing.ManufacturingPipeline) []string {
	if pipeline == nil {
		return nil
	}
	materials := pipeline.Materials()
	goods := make([]string, 0, len(materials))
	for _, target := range materials {
		goods = append(goods, target.TradeSymbol())
	}
	return goods
}

// gateMaterialCommitments computes the tick's commitments for one pipeline's goods, splitting out
// what is aboard hulls this tick can dispatch. dispatchable is the tick's idle pool keyed by hull
// symbol; nil means this tick dispatches nothing, so every commitment is undispatchable.
func (h *RunConstructionCoordinatorHandler) gateMaterialCommitments(
	ctx context.Context,
	cmd *RunConstructionCoordinatorCommand,
	playerID shared.PlayerID,
	systemSymbol, pipelineID string,
	goods []string,
	dispatchable map[string]bool,
) gateCommitments {
	hulls := h.commitmentHulls(ctx, cmd, playerID, systemSymbol)
	return gateCommitments{
		total: h.commitUnits(hulls, pipelineID, goods, nil),
		undispatchable: h.commitUnits(hulls, pipelineID, goods, func(ship *navigation.Ship) bool {
			return dispatchable[ship.ShipSymbol()]
		}),
	}
}

// logInFlightSkip records a purchase declined because in-flight cargo already covers the bill: a
// WARNING in the message body (the container log renderer drops metadata maps) plus the counter,
// so a guard that stops being consulted flatlines visibly rather than vanishing.
func logInFlightSkip(ctx context.Context, good string, remaining, committed int) {
	metrics.RecordGateInFlightSkip(good)
	common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Construction drain: NOT buying %s — the site still wants %d unit(s) but %d are already paid for and sitting in a hull's hold, so buying again would spend full price for cargo the site will refuse", good, remaining, committed), map[string]interface{}{
		"good": good, "remaining": remaining, "committed": committed, "action": "skip_inflight_covered",
	})
}

// warnOnOvershoot fires when committed units EXCEED what the site still wants. The surplus is real
// cargo the site will reject, so it is operator-actionable and is both logged and counted.
func warnOnOvershoot(ctx context.Context, good string, remaining, committed int) {
	overshoot := committed - remaining
	if overshoot <= 0 {
		return
	}
	metrics.RecordGateOvershootUnits(good, overshoot)
	common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Construction drain: %s is OVER-BOUGHT by %d unit(s) — %d are in hulls' holds against an outstanding requirement of only %d. The surplus cannot be delivered (the site rejects a met material with API 4801) and occupies gate-hauling capacity until it is sold or transferred", good, overshoot, committed, remaining), map[string]interface{}{
		"good": good, "remaining": remaining, "committed": committed, "overshoot": overshoot, "action": "gate_material_overshoot",
	})
}
