package hullrepair

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// Outcome is what one repair pass established. It drives the ledger: some outcomes close
// the episode, some spend an attempt, and some only reschedule.
type Outcome string

const (
	// OutcomeRepaired — the fuel write landed and the composite record serves again.
	OutcomeRepaired Outcome = "repaired"
	// OutcomeAlreadyHealthy — the composite served on confirmation, so the earlier
	// failure was transient. Nothing was written.
	OutcomeAlreadyHealthy Outcome = "already_healthy"
	// OutcomeGone — the composite refused with something other than a server error.
	OutcomeGone Outcome = "gone"
	// OutcomeAPIUnavailable — the sub-resources refused too, so the API is down rather
	// than this record being corrupt. Never repair on this.
	OutcomeAPIUnavailable Outcome = "api_unavailable"
	// OutcomeInTransit — the hull is mid-leg and cannot be docked.
	OutcomeInTransit Outcome = "in_transit"
	// OutcomeNavUnreadable — the signature held but /nav was not among the parts that
	// answered, so there is no live position to act on.
	OutcomeNavUnreadable Outcome = "nav_unreadable"
	// OutcomeNoFuelMarket — the hull stands where fuel cannot be bought.
	OutcomeNoFuelMarket Outcome = "no_fuel_market"
	// OutcomeUnpriceable — the spend could not be bounded, so it is not made.
	OutcomeUnpriceable Outcome = "unpriceable"
	// OutcomeUnaffordable — the worst-case fill would breach the reserve floor.
	OutcomeUnaffordable Outcome = "unaffordable"
	// OutcomeWriteFailed — dock or refuel was refused.
	OutcomeWriteFailed Outcome = "write_failed"
	// OutcomeNotFuel — the fuel write landed and the composite still refuses, which
	// proves the corrupt field is not fuel and this repair cannot fix it.
	OutcomeNotFuel Outcome = "not_fuel"
)

// Resolved reports whether the episode is over either way.
func (o Outcome) Resolved() bool {
	return o == OutcomeRepaired || o == OutcomeAlreadyHealthy || o == OutcomeGone
}

// SpentAttempt reports whether the pass got as far as writing to the hull. Only a pass
// that wrote is charged against the attempt bound: an outage or a hull standing somewhere
// it cannot refuel must not exhaust the budget before the repair has ever been tried.
func (o Outcome) SpentAttempt() bool {
	return o == OutcomeWriteFailed || o == OutcomeNotFuel
}

// Terminal reports an outcome no further attempt can improve.
func (o Outcome) Terminal() bool { return o == OutcomeNotFuel }

// Result is one repair pass.
type Result struct {
	Outcome Outcome
	Reason  string
}

// RepairFloor is the reserve a repair refuel must leave intact. It is the flat
// non-contract working-capital floor: this spend is not a contract source-buy, so it
// answers to the floor every other spender answers to (RULINGS #4).
const RepairFloor = common.ImmutableReserveFloor

// Repairer runs the confirmed repair sequence against one hull.
type Repairer struct {
	probe     HullProbe
	writer    HullWriter
	market    FuelMarket
	treasury  Treasury
	tanks     TankSize
	refresher HullRefresher
	report    Reporter
}

// NewRepairer wires the repair. Every collaborator is required except the refresher and
// the reporter, whose absence only costs a row refresh and a metric.
func NewRepairer(
	probe HullProbe,
	writer HullWriter,
	market FuelMarket,
	treasury Treasury,
	tanks TankSize,
	refresher HullRefresher,
	report Reporter,
) *Repairer {
	return &Repairer{
		probe:     probe,
		writer:    writer,
		market:    market,
		treasury:  treasury,
		tanks:     tanks,
		refresher: refresher,
		report:    report,
	}
}

// Repair confirms the corruption signature and, only then, writes fuel to clear it.
//
// The confirmation is the point of the whole function. A single failed read is not
// evidence: the composite is re-read here, and a hull that now serves is left alone. Only
// when the composite refuses AND a part answers is the fault local to this record, which
// is what makes a write safe to spend. When no part answers the API itself is failing and
// firing repairs across the fleet would turn an outage into fleet-wide spend.
func (r *Repairer) Repair(ctx context.Context, playerID int, symbol string) Result {
	res := r.repair(ctx, playerID, symbol)
	if r.report != nil {
		r.report.Attempted(symbol, res.Outcome)
	}
	return res
}

func (r *Repairer) repair(ctx context.Context, playerID int, symbol string) Result {
	switch verdict, err := r.probe.ReadComposite(ctx, symbol); {
	case verdict == ReadOK && err == nil:
		return Result{OutcomeAlreadyHealthy, "the composite record serves; the earlier refusal was transient"}
	case verdict == ReadRefusedClient:
		return Result{OutcomeGone, fmt.Sprintf("the composite record was refused on the request's own merits, which is not the corruption shape: %v", err)}
	case verdict != ReadRefusedServer:
		return Result{OutcomeAPIUnavailable, fmt.Sprintf("the composite record of %s was neither served nor refused for a reason that means anything: %v", symbol, err)}
	}

	parts, err := r.probe.ProbeSubresources(ctx, symbol)
	if err != nil {
		return Result{OutcomeAPIUnavailable, fmt.Sprintf("could not probe the sub-resources of %s: %v", symbol, err)}
	}
	if !parts.AnyAnswered() {
		return Result{OutcomeAPIUnavailable, fmt.Sprintf("no sub-resource of %s answered either, so the API is failing rather than this record", symbol)}
	}
	if parts.Nav == nil {
		return Result{OutcomeNavUnreadable, "the signature holds but /nav did not answer, so there is no live position to repair from"}
	}
	if parts.Nav.Status == NavInTransit {
		return Result{OutcomeInTransit, fmt.Sprintf("%s is mid-leg and cannot be docked", symbol)}
	}

	if guard := r.affordable(ctx, playerID, symbol, parts.Nav.WaypointSymbol); guard != nil {
		return *guard
	}

	return r.write(ctx, playerID, symbol, parts.Nav)
}

// affordable bounds the spend and holds it against the reserve floor. It fails closed on
// every unreadable input — a market with no fuel, an unpriced one, an unreadable tank or
// an unreadable treasury all mean the spend cannot be bounded, and a guard that cannot
// read does not spend (RULINGS #4). Returning a nil Result means the spend may proceed.
func (r *Repairer) affordable(ctx context.Context, playerID int, symbol, waypoint string) *Result {
	price, sells, err := r.market.FuelAsk(ctx, playerID, waypoint)
	switch {
	case err != nil:
		return &Result{OutcomeUnpriceable, fmt.Sprintf("could not read the fuel market at %s: %v", waypoint, err)}
	case !sells:
		// Deliberately not answered by flying the hull somewhere that does: its fuel is
		// the very field that will not read, so no leg can be shown to be reachable.
		return &Result{OutcomeNoFuelMarket, fmt.Sprintf("%s sells no fuel, and a hull whose fuel cannot be read must not be flown to find some", waypoint)}
	case price <= 0:
		return &Result{OutcomeUnpriceable, fmt.Sprintf("no fuel price is known for %s, so the spend cannot be bounded", waypoint)}
	}

	capacity, err := r.tanks.FuelCapacity(ctx, playerID, symbol)
	if err != nil || capacity <= 0 {
		return &Result{OutcomeUnpriceable, fmt.Sprintf("could not read the tank size of %s, so the worst-case fill cannot be bounded: %v", symbol, err)}
	}
	worstCase := int64(capacity) * int64(price)

	credits, err := r.treasury.Credits(ctx, playerID)
	if err != nil {
		return &Result{OutcomeUnaffordable, fmt.Sprintf("the live treasury is unreadable, so the repair spend fails closed: %v", err)}
	}
	gate := common.ReserveFloorGate{Active: true, Treasury: credits, Floor: RepairFloor}
	if gate.Holds(0, worstCase) {
		return &Result{OutcomeUnaffordable, fmt.Sprintf("a full tank for %s could cost %d, which would drop treasury %d below the %d reserve floor", symbol, worstCase, credits, int64(RepairFloor))}
	}
	return nil
}

// write performs the confirmed sequence and verifies it. The hull is returned to the nav
// state it was found in: a coordinator that left it in orbit reads a docked hull as a
// state it did not cause.
func (r *Repairer) write(ctx context.Context, playerID int, symbol string, nav *NavReading) Result {
	undock := false
	if nav.Status != NavDocked {
		if err := r.writer.Dock(ctx, symbol); err != nil {
			return Result{OutcomeWriteFailed, fmt.Sprintf("could not dock %s to refuel: %v", symbol, err)}
		}
		undock = true
	}

	receipt, err := r.writer.Refuel(ctx, symbol)
	if undock {
		// Restored whether or not the fill worked: leaving a hull docked that its owner
		// believes is in orbit is a second fault on top of the one being repaired.
		if oerr := r.writer.Orbit(ctx, symbol); oerr != nil && err == nil {
			err = fmt.Errorf("refuelled but could not return to orbit: %w", oerr)
		}
	}
	if err != nil {
		return Result{OutcomeWriteFailed, fmt.Sprintf("the fuel write on %s failed: %v", symbol, err)}
	}

	verdict, verr := r.probe.ReadComposite(ctx, symbol)
	if verdict == ReadOK && verr == nil {
		if r.refresher != nil {
			if rerr := r.refresher.Refresh(ctx, playerID, symbol); rerr != nil {
				return Result{OutcomeRepaired, fmt.Sprintf("fuel %d/%d restored the record, but its row could not be refreshed: %v", receipt.FuelCurrent, receipt.FuelCapacity, rerr)}
			}
		}
		return Result{OutcomeRepaired, fmt.Sprintf("fuel written to %d/%d and the composite record serves again", receipt.FuelCurrent, receipt.FuelCapacity)}
	}
	if verdict != ReadRefusedServer {
		return Result{OutcomeWriteFailed, fmt.Sprintf("the fuel write landed but the record could not be re-read to verify it: %v", verr)}
	}
	// The write landed and did not help, so the corrupt field is not fuel. Repeating it
	// would spend credits on an action already proven not to work.
	return Result{OutcomeNotFuel, fmt.Sprintf("fuel written to %d/%d and the composite record still refuses, so the corrupt field is not fuel and this repair cannot clear it", receipt.FuelCurrent, receipt.FuelCapacity)}
}
