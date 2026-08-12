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

// The label values of the three shipyard gauges. Named rather than inlined
// because publishYards must emit EVERY one of them on EVERY tick — see the
// comment there — and a list is checkable where scattered string literals are
// not.
const (
	yardCatalogueOutstanding = "outstanding"
	yardCatalogueRead        = "read"
	yardCatalogueFailed      = "failed"

	yardPresenceRequested  = "requested"
	yardPresenceDispatched = "dispatched"
	yardPresenceNoHull     = "no_hull"
	yardPresenceMetered    = "metered"

	yardSlotsQueued = "queued"
	yardSlotsAtHead = "at_head"
	yardSlotsFilled = "filled"

	darkMarketsInReach   = "in_reach"
	darkMarketsHeld      = "held"
	darkMarketsUnreached = "unreached"
	darkMarketsReadable  = "readable"
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
	// surged counts surplus probes sent into CHARTED-BUT-UNPRICED systems this tick.
	// (sp-zvywu). Kept apart from dispatched above because the two answer different
	// questions about coverage: dispatched fills placements the screen already
	// declared, while this one is the fleet reaching systems no placement exists for
	// — the number an operator watches to see the 90% of charted space that has never
	// been priced actually shrinking.
	surged int
	reap   parkedsensing.ReapReport
	buy    parkedsensing.BuyReport
	place  parkedsensing.PlacementReport
	expand parkedsensing.ExpandReport
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
	h.publishYards(cmd.PlayerID.Value(), hb)

	h.publishWave(cmd.PlayerID.Value(), hb)

	held := ""
	switch {
	case hb.buy.Wave == common.WaveHeavy:
		// NAMED BEFORE THE SWITCH ARM because it is the more specific answer, and both are true
		// whenever the switch is off too — "expansion switch off" sends an operator hunting a knob
		// nobody touched.
		held = "heavy wave: probe buying is paused while the treasury climbs toward a heavy hull"
	case hb.buy.ProbeSpendHold != "":
		// AHEAD OF THE SWITCH ARM, which the hold also sets, or a switch that is ON reads as off.
		held = "heavy cap reached and the trade surface already holds more than the fleet can lift" +
			" — probe buying earns nothing until a hull can consume the depth (" + string(hb.buy.ProbeSpendHold) + ")"
	case hb.buy.SpendingPaused:
		// AHEAD OF EVERY CEILING BELOW and BEHIND the two arms above, which are the more
		// specific answers whenever they are also true: this is the only reason on the list
		// that is not the fleet declining to afford something. An operator hunting a money
		// leak must be told "you switched this off" in those words rather than left to infer
		// it from a bought count of zero — the whole cost of sp-com1h was a cycle line that
		// looked healthy while the switch it named did not cover the spending.
		held = "expansion switch: expansion_enabled is 2, so no probe is bought"
	case hb.buy.CapHeld:
		held = "probe cap"
	case hb.buy.FloorHeld:
		held = "buy floor"
	case hb.buy.HaltedPriceDrift:
		held = "price drift"
	}

	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf(
		"Parked sensing cycle: %.3f req/s pacer (%.3f residual, brake %.2f), %d parked, screened %d, yards read %d of %d outstanding, bought %d reused %d queued %d (%d attempts%s%s), reaped %d adopted %d idle-reused %d surged %d, dispatched %d docking %d parked %d, expansion %s",
		hb.pacerRate, hb.sensingRate, hb.brake, hb.rotation, hb.screened,
		hb.yard.Read, hb.yard.Outstanding,
		hb.buy.Bought, hb.buy.Reused, hb.buy.Queued, hb.buy.Attempts, heldSuffix(held), refusalSuffix(hb.buy.Refusals),
		hb.reap.Reaped, hb.adopted, hb.dispatched, hb.surged,
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
			// failing, which without these two counters is indistinguishable from
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
			"buy_refusals": refusalPayload(hb.buy.Refusals),
			// No purchase this tick because a purchase gate was shut. Queryable beside
			// buy_bought so "the gate is shut and money still moved" is one filter
			// rather than a correlation across two engines' log lines. WHICH gate is
			// buy_wave below: the operator's switch, or the regime.
			"buy_spending_paused": hb.buy.SpendingPaused,
			"buy_cap_held":        hb.buy.CapHeld,
			"buy_floor_held":      hb.buy.FloorHeld,
			// The regime, which clause forced PROBE, what the pause is FOR, and the THIRD gate
			// buy_wave cannot explain. An EMPTY wave means no regime could be derived — a third
			// state, never a PROBE; an empty hold on a paused PROBE tick is the operator's switch.
			"buy_wave":                 string(hb.buy.Wave),
			"buy_wave_probe_reason":    string(hb.buy.WaveProbeReason),
			"buy_heavy_reserve_target": int64(hb.buy.HeavyReserveTarget),
			"buy_probe_spend_hold":     string(hb.buy.ProbeSpendHold),
			"buy_price_drift":          hb.buy.HaltedPriceDrift,
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
			// Surplus hulls sent into CHARTED-BUT-UNPRICED systems this tick, at zero
			// credits (sp-zvywu). This is the coverage number: it counts systems the
			// fleet has never held a price for that now have a probe on the way, which
			// dispatched_orphans structurally cannot — that pass can only fill
			// placements the screen already declared.
			"surged_unpriced": hb.surged,

			// Bought-and-sent + re-tasked spares + seed-claimed hulls, together.
			"dispatched": hb.place.Dispatched,
			"docking":    hb.place.Docking,
			"parked":     hb.place.Parked,

			"expansion_skipped":        hb.expand.Skipped,
			"expansion_seeding_paused": hb.expand.SeedingPaused,
			"expansion_discovered":     hb.expand.Discovered,
			"seeds_requested":          hb.expand.SeedsRequested,
			// The cold-start deadlock, made readable. A fleet with no staffed probe
			// counter reported "0 seeds requested" and nothing else for an entire era —
			// indistinguishable from a fleet with nothing left to seed. seeds_unstaged
			// says WHY it is zero, and counters_staffed says whether the escape is
			// running (counterstaff.go).
			"seeds_unstaged":   hb.expand.SeedsUnstaged,
			"counters_staffed": hb.expand.CountersStaffed,
			// SPARE rows released because the hull they named was already charting. A
			// standing non-zero value is that invariant being re-broken every tick.
			"spare_ghosts_released": hb.expand.SpareGhostsReleased,
			"seeds_claimed":         hb.expand.SeedsClaimed,
			"charted":               hb.expand.Charted,
			"markets_found":         hb.expand.MarketsFound,
			"retargeted":            hb.expand.Retargeted,
		})
}

// publishYards puts the shipyard blind spot on the scrape surface.
//
// THESE NUMBERS EXISTED FOR A WHILE BEFORE ANYONE COULD READ THEM. They were
// written into the cycle line's structured payload and nowhere else, and the
// question they were built to answer — is the presence pass dispatching and
// finding no work, or not running at all — could not be answered from outside
// the process at all (sp-qkskz). A log payload is a forensic record; a gauge is
// what a panel draws and an alert fires on, and the distinction is the whole
// difference between a counter that is stored and a counter that is watched.
//
// EVERY LABEL VALUE IS SET ON EVERY TICK, INCLUDING THE ZEROS, for the same
// reason slotCensus republishes its empty states: a gauge that simply stopped
// reporting would leave its last non-zero value standing in Prometheus until the
// series went stale, so a backlog that drained to nothing would read as
// permanently jammed — which is the exact misreading these counters exist to
// prevent, restored one layer down.
//
// Observation only (RULINGS #4): a nil recorder returns immediately and nothing
// below can touch a decision path.
func (h *RunProbeSensingCoordinatorHandler) publishYards(playerID int, hb heartbeat) {
	if h.recorder == nil {
		return
	}

	for state, count := range map[string]int{
		yardCatalogueOutstanding: hb.yard.Outstanding,
		yardCatalogueRead:        hb.yard.Read,
		yardCatalogueFailed:      hb.yard.Failed,
	} {
		h.recorder.RecordYardCatalogue(playerID, state, count)
	}

	for outcome, count := range map[string]int{
		yardPresenceRequested:  hb.presence.Requested,
		yardPresenceDispatched: hb.presence.Dispatched,
		yardPresenceNoHull:     hb.presence.NoHull,
		yardPresenceMetered:    hb.presence.Metered,
	} {
		h.recorder.RecordYardPresence(playerID, outcome, count)
	}

	for stage, count := range map[string]int{
		yardSlotsQueued: hb.buy.YardsQueued,
		yardSlotsAtHead: hb.buy.YardsAtHead,
		yardSlotsFilled: hb.buy.YardsFilled,
	} {
		h.recorder.RecordYardSlots(playerID, stage, count)
	}

	// THE DEMAND, published beside the yards and under the same every-label-every-tick
	// rule. It is what makes an operator hold legible: the buy queue reports zero
	// bought whether it was forbidden to spend or had nothing to spend on, and only
	// this surface separates them. `readable` is published as a 0/1 gauge rather than
	// folded into the counts, so a blind reachability read is never mistaken for a map
	// with nothing out of reach.
	for component, count := range map[string]int{
		darkMarketsInReach:   hb.buy.DarkMarkets,
		darkMarketsHeld:      hb.buy.DarkMarketsHeld,
		darkMarketsUnreached: hb.buy.DarkMarketsUnreached,
		darkMarketsReadable:  boolGauge(hb.buy.DarkMarketsReadable),
	} {
		h.recorder.RecordCoverageSurface(playerID, component, count)
	}
}

// boolGauge renders a readability flag as the 0/1 a gauge can carry.
func boolGauge(v bool) int {
	if v {
		return 1
	}
	return 0
}

// publishWave republishes THIS reader's regime under the drain's own reader label: a gauge only the
// growth coordinator writes cannot see a drain that disagreed with it, and a tick that derived NO
// regime publishes nothing, because a fabricated PROBE reports a release the drain never made.
func (h *RunProbeSensingCoordinatorHandler) publishWave(playerID int, hb heartbeat) {
	if h.recorder == nil || hb.buy.Wave == "" {
		return
	}
	h.recorder.RecordWave(playerID, hb.buy.Wave, hb.buy.WaveProbeReason)

	// EVERY REASON, EVERY TICK, so a hold that lifts is seen to lift — and behind the same guard,
	// since a tick that derived no regime evaluated no refusal either.
	for _, hold := range common.ProbeSpendHolds() {
		h.recorder.RecordProbeSpendHold(playerID, string(hold), hold == hb.buy.ProbeSpendHold)
	}
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
// The reason is reported VERBATIM rather than mapped to a category, because the
// category is exactly what nobody could work out from the outside: a bare
// "(6 attempts)" is six silent failures per tick, forever, with no way to tell an
// out-of-stock yard from a hull that cannot dock from an API outage.
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
	// APPENDED ONLY WHEN IT BINDS, so the ordinary line is unchanged. "0 seed(s)
	// requested" on its own reads as "nothing left to seed"; it is the opposite
	// condition that needs saying out loud, and it is the one that ran for an era
	// unnoticed.
	if rep.SeedsUnstaged > 0 {
		summary += fmt.Sprintf(" (%d target(s) have no staffed probe counter in reach; %d hull(s) lent to one)",
			rep.SeedsUnstaged, rep.CountersStaffed)
	}
	// Appended only when it binds, for the same reason: a yard held by a row for a
	// hull that is not there reads exactly like a fleet with nowhere left to stage.
	if rep.SpareGhostsReleased > 0 {
		summary += fmt.Sprintf(", %d ghost spare row(s) released", rep.SpareGhostsReleased)
	}
	if rep.SeedingPaused {
		return summary + " (seed dispatch off: errands in flight still finish)"
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
//
// IT MEASURES LastDataAt, NOT LastScan, and the difference is the whole of
// sp-zml2u. LastScan is the rotation's pacing clock and advances on every turn
// including the ones the market-scan budget declines — which is most of them
// (92%, measured). Reading it here reported the age of the last ATTEMPT while
// claiming to report the age of the DATA, so the gauge said 909 slots were fresh
// while 216 markets actually were. LastDataAt moves only when a scan wrote
// something, so this now measures what it says it measures.
func stalenessPercentiles(views []parkedsensing.SensingSlotView, now time.Time) (hot, median, cold float64, ok bool) {
	ages := make([]float64, 0, len(views))
	for _, view := range views {
		if view.LastDataAt.IsZero() {
			continue
		}
		age := now.Sub(view.LastDataAt).Seconds()
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
