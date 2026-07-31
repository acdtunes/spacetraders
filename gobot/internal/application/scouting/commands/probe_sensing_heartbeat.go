package commands

// probe_sensing_heartbeat.go is the parked-probe coordinator's reporting half:
// the one structured line per tick, and the gauges behind it.
//
// Under the wake model nobody watches this loop run — the heartbeat IS the
// standing sensor, and a number that is easy to misread here is a number that
// will be misread. Two of them have already bitten and are called out at their
// definitions: QUEUED does not mean "purchase in flight", and Dispatched
// aggregates three different ways a hull starts moving.

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
)

// The staleness tiers published per player. They are percentiles of the parked
// fleet's scan ages, not thresholds: "cold" is the p90 slot's age — how stale the
// WORST-served markets are — which is the number the trade planner's
// sink-freshness cap actually fails closed on. A single mean would hide exactly
// that tail.
const (
	stalenessTierHot    = "hot"    // p10 — the best-served markets
	stalenessTierMedian = "median" // p50
	stalenessTierCold   = "cold"   // p90 — the tail the freshness cap binds on
)

// heartbeat is one tick's reportable outcome, gathered from every engine.
type heartbeat struct {
	sensingRate float64
	pacerRate   float64
	brake       float64
	cutover     int
	screened    int
	// adopted counts stranded scout probes recorded this tick — hulls the
	// one-shot cutover could not place because they were in transit.
	adopted int
	// dispatched counts idle probes WE ALREADY OWN sent to open placements this
	// tick. Never a purchase. Distinct from place.Dispatched below, which counts
	// movement commands issued by the placement machine — one hull shows up in
	// both, one tick apart, and conflating them would double-count the fleet's
	// activity.
	dispatched int
	reap       parkedsensing.ReapReport
	buy        parkedsensing.BuyReport
	place      parkedsensing.PlacementReport
	expand     parkedsensing.ExpandReport
	// yard is the free shipyard-catalogue sweep's accounting. Outstanding is the
	// number an operator watches fall to zero: it is the count of KNOWN shipyards
	// the fleet has never asked what they sell, and while it is non-zero the fleet
	// is hunting hulls it may already be able to see.
	yard parkedsensing.YardCatalogReport
	// presence is the paid half's accounting: yards that need a hull standing on
	// them before any read can price them, and how many hulls were sent. Requested
	// is the backlog; the gap between it and Dispatched is the honest measure of
	// how committed the sensing fleet is, not of a fault.
	presence parkedsensing.YardPresenceReport
	// rotation is how many slots the scan pacer is ACTUALLY watching, which is
	// not the ledger's parked count: the rotation drops anything unscannable.
	rotation int
}

// heartbeat emits the tick's single structured summary line.
//
// Field meanings that are NOT what their names suggest, and are therefore spelled
// out both here and in the log payload:
//
//   - buy_queued counts placements CLAIMED for purchase this tick. A claim is
//     taken before the price is read, so a claimed placement whose yard then
//     quoted above the floor stays QUEUED with nothing bought. Reading it as
//     "purchases in flight" overstates spend and hides a stalled queue.
//   - dispatched counts hulls SENT MOVING, from all three sources at once:
//     freshly-bought probes, spares re-tasked to a different placement, and
//     seeds claimed at the end of a charting tour. It is not a purchase count.
//   - buy_attempts counts every trip through the buy path including the failures,
//     which is what the per-tick cap actually bounds — so attempts far above
//     bought is the signature of unpriceable or refusing yards, not of spending.
//   - buy_reaped counts claims HANDED BACK, not placements abandoned. It is the
//     other half of buy_queued and sits beside it for that reason: a claim in a
//     system that lost its verdict is undrainable, so reverting it to WANTED is
//     what stops buy_queued reading permanently inflated. A standing non-zero
//     value means verdicts are churning, not that the queue is failing.
func (h *RunProbeSensingCoordinatorHandler) heartbeat(ctx context.Context, cmd *RunProbeSensingCoordinatorCommand, cfg sensingConfig, hb heartbeat) {
	held := ""
	switch {
	case hb.buy.SpendingPaused:
		// FIRST, because it is the only reason on this list that is not the fleet
		// declining to afford something. An operator hunting a money leak reads this
		// line and must be told "you switched this off" in those words rather than
		// left to infer it from a bought count of zero — the whole cost of sp-com1h
		// was a cycle line that looked healthy while the switch it named did not
		// cover the spending.
		held = "expansion switch: expansion_enabled is off, so no probe is bought"
	case hb.buy.CapHeld:
		held = "probe cap"
	case hb.buy.FloorHeld && hb.buy.HeavyReserve > 0:
		held = fmt.Sprintf("buy floor, %d reserved for the next heavy", hb.buy.HeavyReserve)
	case hb.buy.FloorHeld:
		held = "buy floor"
	case hb.buy.HaltedPriceDrift:
		held = "price drift"
	}

	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf(
		"Parked sensing cycle: %.3f req/s pacer (%.3f residual, brake %.2f), %d parked, screened %d, yards read %d of %d outstanding, bought %d reused %d queued %d (%d attempts%s%s), reaped %d adopted %d idle-reused %d, dispatched %d docking %d parked %d, expansion %s",
		hb.pacerRate, hb.sensingRate, hb.brake, hb.rotation, hb.screened,
		hb.yard.Read, hb.yard.Outstanding,
		hb.buy.Bought, hb.buy.Reused, hb.buy.Queued, hb.buy.Attempts, heldSuffix(held), refusalSuffix(hb.buy.Refusals),
		hb.reap.Reaped, hb.adopted, hb.dispatched,
		hb.place.Dispatched, hb.place.Docking, hb.place.Parked, expansionSummary(hb.expand)),
		map[string]interface{}{
			"action":                "parked_sensing_cycle",
			"container_id":          cmd.ContainerID,
			"pacer_rate":            hb.pacerRate,
			"sensing_rate":          hb.sensingRate,
			"brake":                 hb.brake,
			"probe_cap":             cfg.ProbeCap,
			"rotation_slots":        hb.rotation,
			"screened":              hb.screened,
			"cutover_posts_removed": hb.cutover,

			// The shipyard blind spot, as a number. yards_outstanding is every KNOWN
			// shipyard whose catalogue we have never read; while it is non-zero the
			// fleet cannot answer "where does this hull sell" for that many counters.
			// It drains on its own and then stays at zero forever, so a value that
			// stops falling is the signal — either the reads are failing (see
			// yards_failed) or new territory is being charted faster than it is read.
			"yards_read":        hb.yard.Read,
			"yards_failed":      hb.yard.Failed,
			"yards_outstanding": hb.yard.Outstanding,

			// The OTHER shipyard blind spot, and the one no amount of reading can
			// close: yards_need_presence counts counters we know sell a hull we want
			// and whose price the API will not disclose until one of our hulls is
			// standing there. yards_presence_sent is how many were addressed this
			// tick. The two together separate the three states an operator would
			// otherwise have to guess between — a backlog being worked (sent > 0), a
			// fleet with no hull to spare (no_hull high), and an allowance holding
			// the rate down (metered high).
			"yards_need_presence":   hb.presence.Requested,
			"yards_presence_sent":   hb.presence.Dispatched,
			"yards_presence_nohull": hb.presence.NoHull,
			"yards_presence_meter":  hb.presence.Metered,

			// The SAME blind spot seen from the buy queue (sp-7qhum). The pass above
			// moves a hull we already own; these three say what the queue that BUYS
			// hulls did about it.
			//
			// yard_slots_queued is how many unfilled placements stand on such a
			// counter, and yard_slots_at_head how many of those the ordering
			// delivered into the six-placement window the tick can actually reach.
			// The second was effectively zero before the yard term existed — 78 heavy
			// counters among 8,934 rows ordered on coverage, depth and arrival — and a
			// queued figure that stays high beside an at_head of zero is this ordering
			// failing, which is the one reading that used to be indistinguishable from
			// having no dark yards at all.
			//
			// yard_slots_filled is the outcome: placements on a dark yard actually
			// funded this tick. It reads zero while buy_spending_paused is true, and
			// that is correct rather than a fault — the queue is ordered, nothing is
			// bought, and the ordering is banked for the tick the switch comes back on.
			"yard_slots_queued":  hb.buy.YardsQueued,
			"yard_slots_at_head": hb.buy.YardsAtHead,
			"yard_slots_filled":  hb.buy.YardsFilled,

			"buy_bought": hb.buy.Bought,
			"buy_reused": hb.buy.Reused,
			// CLAIMED for purchase — NOT purchases in flight. A QUEUED slot may
			// equally be one whose yard quoted above the floor.
			"buy_queued":          hb.buy.Queued,
			"buy_attempts":        hb.buy.Attempts,
			"buy_skipped_no_yard": hb.buy.SkippedNoYard,
			// Why the counters that refused refused, one entry per distinct
			// refusal. attempts > 0 with bought == 0 and this empty is a
			// contradiction — every attempt-burning path records one.
			"buy_refusals":   refusalPayload(hb.buy.Refusals),
			// The operator's expansion switch, as the buy queue saw it. Queryable
			// beside buy_bought so "the switch is off and money still moved" is one
			// filter rather than a correlation across two engines' log lines.
			"buy_spending_paused": hb.buy.SpendingPaused,
			"buy_cap_held":        hb.buy.CapHeld,
			"buy_floor_held":      hb.buy.FloorHeld,
			// Credits held back for the NEXT heavy. Non-zero beside buy_floor_held
			// means "saving for a heavy", NOT "sensing is broken" — the one signal
			// that tells those two apart (spec risk 3).
			"buy_heavy_reserve": hb.buy.HeavyReserve,
			"buy_price_drift":   hb.buy.HaltedPriceDrift,
			// Claims handed back because their system lost IN_SCOPE — the other
			// half of buy_queued, not a count of abandoned placements.
			"buy_reaped":       hb.reap.Reaped,
			"buy_reap_skipped": hb.reap.Skipped,
			// Hulls we already OWNED and had lost track of, now back on the books
			// — never a purchase. A non-zero value long after the cutover means
			// probes are being stranded somewhere, not that the fleet grew.
			"adopted_stranded": hb.adopted,
			// Idle hulls we already OWNED, sent to open placements this tick at
			// zero credits — never a purchase, and never a hull taken from a live
			// container or post. Non-zero beside a held buy floor is the healthy
			// reading: the fleet is filling placements it cannot afford to buy for.
			"dispatched_orphans": hb.dispatched,

			// Bought-and-sent + re-tasked spares + seed-claimed hulls, together.
			"dispatched": hb.place.Dispatched,
			"docking":    hb.place.Docking,
			"parked":     hb.place.Parked,

			"expansion_skipped":         hb.expand.Skipped,
			"expansion_spending_paused": hb.expand.SpendingPaused,
			"expansion_discovered":      hb.expand.Discovered,
			"seeds_requested":      hb.expand.SeedsRequested,
			"seeds_claimed":        hb.expand.SeedsClaimed,
			"charted":              hb.expand.Charted,
			"markets_found":        hb.expand.MarketsFound,
			"retargeted":           hb.expand.Retargeted,
		})
}

// heldSuffix names the ceiling that stopped the drain, or nothing at all.
func heldSuffix(held string) string {
	if held == "" {
		return ""
	}
	return ", held at the " + held
}

// refusalPayload renders the refusals as structured rows for the log payload.
//
// Unlike the human-readable suffix this is NOT truncated: the message line has a
// reader with finite patience, the payload has a query engine. Truncating here
// would mean the one refusal an operator is hunting could be the one dropped.
func refusalPayload(refusals []parkedsensing.BuyRefusal) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(refusals))
	for _, r := range refusals {
		out = append(out, map[string]interface{}{
			"step": string(r.Step),
			"yard": r.Yard,
			// Empty on a quote refusal: no hull was engaged.
			"buyer":              r.Buyer,
			"reason":             r.Reason,
			"placements_blocked": r.Count,
		})
	}
	return out
}

// maxLoggedRefusals bounds how many distinct refusals reach the cycle line. The
// drain can try at most maxDrainAttempts counters per tick so the list is
// already short, but the bound is explicit because this line is emitted every
// ~30s forever and a summary that can grow without limit is its own defect.
const maxLoggedRefusals = 3

// refusalSuffix renders why the counters that refused this tick refused.
//
// This is the line that used to read "(6 attempts)" and nothing else. Six silent
// failures per tick, forever, with no way to tell an out-of-stock yard from a
// hull that cannot dock from an API outage — so the underlying reason is
// reported VERBATIM rather than mapped to a category, because the category is
// exactly what nobody could work out from the outside.
//
// Aggregated, never per attempt: one row per distinct refusal with the number of
// placements it blocked. A count well above one is the signature of a single
// counter holding up the whole queue, which is a different fault from several
// counters each having a bad minute.
func refusalSuffix(refusals []parkedsensing.BuyRefusal) string {
	if len(refusals) == 0 {
		return ""
	}
	shown := refusals
	if len(shown) > maxLoggedRefusals {
		shown = shown[:maxLoggedRefusals]
	}
	parts := make([]string, 0, len(shown))
	for _, r := range shown {
		who := r.Yard
		// The buyer is only carried on a BUY refusal, and its presence is what
		// separates "this counter refused" from "this hull could not buy".
		if r.Buyer != "" {
			who += " via " + r.Buyer
		}
		blocked := ""
		if r.Count > 1 {
			blocked = fmt.Sprintf(" ×%d", r.Count)
		}
		parts = append(parts, fmt.Sprintf("%s at %s%s: %s", r.Step, who, blocked, r.Reason))
	}
	more := ""
	if len(refusals) > len(shown) {
		more = fmt.Sprintf(" (+%d more)", len(refusals)-len(shown))
	}
	return ", refused: " + strings.Join(parts, "; ") + more
}

// expansionSummary states why expansion did nothing, or what it did. The Skipped
// reason is reported verbatim because the gates behind it are operationally
// different, and the API being under pressure is the one worth reading twice.
//
// A SPEND PAUSE IS REPORTED ALONGSIDE THE WORK, NOT INSTEAD OF IT. The paused
// tick still discovers, so printing "skipped" for it would hide the very number
// the operator turned the switch to watch — whether coverage keeps growing on
// hulls already bought.
func expansionSummary(rep parkedsensing.ExpandReport) string {
	if rep.Skipped != "" {
		return "skipped (" + rep.Skipped + ")"
	}
	summary := fmt.Sprintf("+%d discovered, %d seed(s) requested, %d claimed, %d charted",
		rep.Discovered, rep.SeedsRequested, rep.SeedsClaimed, rep.Charted)
	if rep.SpendingPaused {
		return summary + " (spending paused: no seed purchase, no explorer demand)"
	}
	return summary
}

// publish sets the sensing gauges for one tick. Observation only (RULINGS #4):
// a nil recorder, and every arithmetic edge below, leaves the decision path
// untouched.
func (h *RunProbeSensingCoordinatorHandler) publish(ctx context.Context, playerID int, pacerRate float64, views []parkedsensing.SensingSlotView, ports SensingEnginePorts) {
	if h.recorder == nil {
		return
	}
	h.recorder.RecordRate(playerID, pacerRate)

	for state, count := range slotCensus(ctx, ports, playerID) {
		h.recorder.RecordSlots(playerID, state, count)
	}

	hot, median, cold, ok := stalenessPercentiles(views, h.clock.Now())
	if !ok {
		return
	}
	h.recorder.RecordStaleness(playerID, stalenessTierHot, hot)
	h.recorder.RecordStaleness(playerID, stalenessTierMedian, median)
	h.recorder.RecordStaleness(playerID, stalenessTierCold, cold)
}

// slotCensus counts the ledger's placements by state, for the slots gauge.
//
// Every state is emitted on every tick, including the ones at zero. A gauge that
// simply stopped reporting an empty state would leave its last non-zero value
// standing in Prometheus until the series went stale — so a queue that drained to
// nothing would read as permanently backed up.
func slotCensus(ctx context.Context, ports SensingEnginePorts, playerID int) map[string]int {
	states := []string{
		parkedsensing.SlotStateWanted, parkedsensing.SlotStateQueued, parkedsensing.SlotStateBought,
		parkedsensing.SlotStateInTransit, parkedsensing.SlotStateParked,
	}
	census := make(map[string]int, len(states))
	for _, state := range states {
		census[state] = 0
	}

	slots, err := ports.Ledger.SlotsByState(ctx, playerID, states...)
	if err != nil {
		// A census we could not take is not a census of zero. Report nothing
		// rather than publish an all-zero picture of a populated ledger.
		return nil
	}
	for _, slot := range slots {
		census[slot.State]++
	}
	return census
}

// stalenessPercentiles reports the parked fleet's scan-age distribution at p10,
// p50 and p90, in seconds.
//
// A slot that has NEVER been scanned carries the zero time, and its age is
// therefore meaningless rather than enormous — it is excluded, not clamped. It is
// a placement waiting for its first turn, and folding it in as an unbounded age
// would peg the cold tier at the process's uptime and make the gauge unreadable
// for exactly as long as the rotation is warming up. With nothing measured at all
// the tiers are not published, so the series is absent rather than false.
func stalenessPercentiles(views []parkedsensing.SensingSlotView, now time.Time) (hot, median, cold float64, ok bool) {
	ages := make([]float64, 0, len(views))
	for _, view := range views {
		if view.LastScan.IsZero() {
			continue
		}
		age := now.Sub(view.LastScan).Seconds()
		if age < 0 {
			// A stamp from the future is clock skew, not freshness. Zero is the
			// honest reading: it was scanned as recently as we can tell.
			age = 0
		}
		ages = append(ages, age)
	}
	if len(ages) == 0 {
		return 0, 0, 0, false
	}
	sort.Float64s(ages)
	return percentile(ages, 0.10), percentile(ages, 0.50), percentile(ages, 0.90), true
}

// percentile is the nearest-rank percentile of a sorted slice. Nearest-rank
// rather than interpolated because the tiers name REAL slots — "the p90 slot is
// this stale" — and an interpolated value would describe a market that does not
// exist.
func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(q*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}
