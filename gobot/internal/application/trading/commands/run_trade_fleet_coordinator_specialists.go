package commands

import (
	"context"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
)

const specialistStatsWindow = 24 * time.Hour

type specialistPorts struct {
	claims    mvt.ClaimRegistry
	telemetry trading.TourTelemetryRepository
	fees      GateFeeReader
}

// ready reports whether every port the pass dereferences is present. Partial wiring leaves the
// pool inert rather than panicking the coordinator goroutine on the first armed cadence.
func (p *specialistPorts) ready() bool {
	return p != nil && p.claims != nil && p.telemetry != nil && p.fees != nil
}

// SetSpecialistPorts wires the MVT specialist pool. Unwired or partially wired, the pool is inert.
func (h *RunTradeFleetCoordinatorHandler) SetSpecialistPorts(claims mvt.ClaimRegistry, telemetry trading.TourTelemetryRepository, fees GateFeeReader) {
	ports := &specialistPorts{claims: claims, telemetry: telemetry, fees: fees}
	if !ports.ready() {
		return
	}
	h.specialists = ports
}

// effectiveFleetTag is the tag this tick's launch must use: the re-tag the pool just committed
// for this hull, else the one the (unmutated, daemon-shared) entity carries.
func effectiveFleetTag(s *navigation.Ship, retags map[string]string) string {
	if to, ok := retags[s.ShipSymbol()]; ok {
		return to
	}
	return s.DedicatedFleet()
}

func shipSystem(s *navigation.Ship) string {
	if s.CurrentLocation() == nil {
		return ""
	}
	return s.CurrentLocation().SystemSymbol
}

// sortByMarginAsc orders worst-earning specialist first, symbol breaking ties.
func sortByMarginAsc(ships []*navigation.Ship, perHullMargin map[string]float64) {
	sort.Slice(ships, func(i, j int) bool {
		mi, mj := perHullMargin[ships[i].ShipSymbol()], perHullMargin[ships[j].ShipSymbol()]
		if mi != mj {
			return mi < mj
		}
		return ships[i].ShipSymbol() < ships[j].ShipSymbol()
	})
}

// specialistParkedAndDrained is the set of hulls whose re-tag would ride THIS tick's launch: in
// the idle bucket, so no container holds a claim, with an empty hold. It is the plan's
// preference, not its gate — where a hull stands is a fact about it only between tours.
func specialistParkedAndDrained(idle []*navigation.Ship) map[string]bool {
	safe := map[string]bool{}
	for _, s := range idle {
		if s.CargoUnits() == 0 {
			safe[s.ShipSymbol()] = true
		}
	}
	return safe
}

// planSpecialists decides one cadence's tag changes over the WHOLE cohort, not just the hulls
// that happen to be free at the instant the pass runs: orphaned specialists self-demote, excess
// specialists demote lowest-margin first, and open seats promote from the trade-mvt hulls. A
// pick that cannot move yet is not dropped — reconcileSpecialists earmarks it and settles it
// the next time it stands drained, which is the only way a fully-utilised fleet ever
// specialises. Picks are ordered safe-first, so a seat that can be filled now is filled now.
func planSpecialists(all, idle []*navigation.Ship, fat []mvt.LaneStat, pool int, perHullMargin map[string]float64) (promote, demote []*navigation.Ship) {
	touches := map[string]bool{}
	for _, l := range fat {
		touches[l.Source], touches[l.Sink] = true, true
	}
	safe := specialistParkedAndDrained(idle)
	parked := map[string]bool{}
	for _, s := range idle {
		parked[s.ShipSymbol()] = true
	}

	current := 0
	var parkedLane, cohort []*navigation.Ship
	for _, s := range all {
		tag := s.DedicatedFleet()
		if tag == tradeFleetLane {
			current++
		}
		// A captain reservation outranks the pool: the fleet view drops those hulls before
		// either bucket, so earmarking one would only hold a seat nothing can ever settle.
		if s.IsReservedByCaptain() {
			continue
		}
		switch tag {
		case tradeFleetLane:
			if parked[s.ShipSymbol()] {
				parkedLane = append(parkedLane, s)
			}
		case tradeFleetMVT:
			// A retiring hull is never a candidate: its refusal is PERMANENT, unlike a laden
			// hull's, so a seat picked for it is earmarked to a boundary that never comes.
			if !s.IsRetiring() {
				cohort = append(cohort, s)
			}
		}
	}
	sortByMarginAsc(parkedLane, perHullMargin)

	demoted := map[string]bool{}
	// An orphan is a specialist PARKED off every fat lane. Where a hull stands is a fact about
	// it only between tours; one mid-flight is not judged on where it happens to be.
	for _, s := range parkedLane {
		if !touches[shipSystem(s)] {
			demote = append(demote, s)
			demoted[s.ShipSymbol()] = true
		}
	}
	// A specialist marked retiring never works its lane again, so its seat is freed at the mark
	// rather than left until the excess rule ranks it worst, which on a full pool never comes.
	// Judged wherever it stands: the mark is durable, unlike the orphan test's parked-here.
	for _, s := range all {
		if s.DedicatedFleet() != tradeFleetLane || demoted[s.ShipSymbol()] || s.IsReservedByCaptain() {
			continue
		}
		if s.IsRetiring() {
			demote = append(demote, s)
			demoted[s.ShipSymbol()] = true
		}
	}
	// The excess is ranked over every surviving specialist, running ones included, so a hull
	// sheds its tag for being among the worst rather than merely for being parked.
	var surviving []*navigation.Ship
	for _, s := range all {
		if s.DedicatedFleet() == tradeFleetLane && !demoted[s.ShipSymbol()] {
			surviving = append(surviving, s)
		}
	}
	sortByMarginAsc(surviving, perHullMargin)
	excess := len(surviving) - pool
	for i := 0; i < excess && i < len(surviving); i++ {
		if s := surviving[i]; !s.IsReservedByCaptain() {
			demote = append(demote, s)
			demoted[s.ShipSymbol()] = true
		}
	}
	seats := pool - (current - len(demote))
	if seats <= 0 || len(cohort) == 0 {
		return promote, demote
	}
	sort.Slice(cohort, func(i, j int) bool {
		si, sj := safe[cohort[i].ShipSymbol()], safe[cohort[j].ShipSymbol()]
		if si != sj {
			return si
		}
		return cohort[i].ShipSymbol() < cohort[j].ShipSymbol()
	})
	taken := map[string]bool{}
	pick := func(pred func(*navigation.Ship) bool) *navigation.Ship {
		for _, s := range cohort {
			if !taken[s.ShipSymbol()] && pred(s) {
				return s
			}
		}
		return nil
	}
	for _, l := range fat {
		if seats == 0 {
			break
		}
		lane := l
		// Standing at a lane end is worth preferring only in a hull that can start the lane
		// now. An earmarked hull lands wherever its current tour ends, so its position at
		// this moment says nothing about where it will be when the tag moves; the safe-first
		// ordering leaves it as the last resort rather than a proximity pick.
		s := pick(func(s *navigation.Ship) bool { return safe[s.ShipSymbol()] && shipSystem(s) == lane.Source })
		if s == nil {
			s = pick(func(s *navigation.Ship) bool { return safe[s.ShipSymbol()] && shipSystem(s) == lane.Sink })
		}
		if s == nil {
			s = pick(func(*navigation.Ship) bool { return true })
		}
		if s == nil {
			break
		}
		taken[s.ShipSymbol()] = true
		promote = append(promote, s)
		seats--
	}
	return promote, demote
}

// markSpecialistIntents REPLACES the deferred set with this cadence's plan and stamps the pool
// the settle path caps itself against. Replacing rather than accumulating is what makes a
// deferred change undoable: an intent the pool no longer wants is simply not re-marked, so it
// can never settle later against a lane that has gone thin or a seat that has closed.
func (h *RunTradeFleetCoordinatorHandler) markSpecialistIntents(promote, demote []*navigation.Ship, pool int) {
	pending := make(map[string]string, len(promote)+len(demote))
	for _, s := range demote {
		pending[s.ShipSymbol()] = tradeFleetMVT
	}
	for _, s := range promote {
		pending[s.ShipSymbol()] = tradeFleetLane
	}
	h.specialistPending, h.specialistPool = pending, pool
}

// settleSpecialistIntents commits the earmarked tag changes whose hulls have reached the
// boundary, on every reconcile tick rather than on the cadence. The boundary is the HOLD, not
// the idle bucket: a re-tag neither claims a hull nor evicts the container flying it, which
// reads its path from the config it launched with, so a load bought for the path the tag
// selects is all a tag write can strand. Demanding a PARK too waits on a boundary a continuous
// tour does not reach, its container keeping the claim across tours, so every deferred change
// expired unsettled. Demotions settle first — a seat one frees is one a promotion may take —
// and among equals a PARKED hull, whose re-tag rides this tick's launch.
func (h *RunTradeFleetCoordinatorHandler) settleSpecialistIntents(ctx context.Context, cmd *RunTradeFleetCoordinatorCommand, all, idle []*navigation.Ship, retags map[string]string, logger common.ContainerLogger) (promoted, demoted int) {
	if len(h.specialistPending) == 0 {
		return 0, 0
	}
	parked := specialistParkedAndDrained(idle)
	occupied := 0
	var ready []*navigation.Ship
	for _, s := range all {
		if s.DedicatedFleet() == tradeFleetLane {
			occupied++
		}
		if _, want := h.specialistPending[s.ShipSymbol()]; !want {
			continue
		}
		// A reservation outranks the pool wherever the hull stands, as the plan has it.
		if s.IsReservedByCaptain() || s.CargoUnits() > 0 {
			continue
		}
		ready = append(ready, s)
	}
	sort.Slice(ready, func(i, j int) bool {
		ti, tj := h.specialistPending[ready[i].ShipSymbol()], h.specialistPending[ready[j].ShipSymbol()]
		if ti != tj {
			return ti == tradeFleetMVT
		}
		if pi, pj := parked[ready[i].ShipSymbol()], parked[ready[j].ShipSymbol()]; pi != pj {
			return pi
		}
		return ready[i].ShipSymbol() < ready[j].ShipSymbol()
	})
	for _, s := range ready {
		to := h.specialistPending[s.ShipSymbol()]
		if s.DedicatedFleet() == to {
			delete(h.specialistPending, s.ShipSymbol())
			continue
		}
		// A late arrival never overfills the pool: a hull whose seat closed while it was still
		// flying keeps its tag and waits for the next pass to re-plan without it.
		if to == tradeFleetLane && occupied >= h.specialistPool {
			continue
		}
		if !h.applySpecialistTag(ctx, cmd, s, to, retags, logger) {
			continue
		}
		delete(h.specialistPending, s.ShipSymbol())
		if to == tradeFleetLane {
			// Only a PARKED hull's claim is the pool's to drop — one mid-tour IS working that
			// system. Never silent: a stale row steers every OTHER hull off unworked ground.
			if parked[s.ShipSymbol()] {
				if err := h.specialists.claims.Release(ctx, cmd.PlayerID.Value(), s.ShipSymbol()); err != nil {
					logger.Log("WARNING", "Specialist pool: claim release failed on promotion", map[string]interface{}{"hull": s.ShipSymbol(), "error": err.Error()})
				}
			}
			promoted, occupied = promoted+1, occupied+1
			continue
		}
		demoted, occupied = demoted+1, occupied-1
	}
	return promoted, demoted
}

// applySpecialistTag is the pool's ONLY tag write, and both guards sit inside it rather than at
// any one caller: a hull holding cargo keeps its tag whichever path reached here, because that
// load was bought for the path the tag selects; and a hull marked retiring never TAKES the tag,
// since it stands down for good once drained and would hold the seat without ever using it.
// That guard is one-way — a marked hull already carrying the tag must still be able to shed it.
// It returns the re-tag through retags because it must NOT write it into the caller's
// *navigation.Ship: those pointers come from the shared ship list cache that every other
// coordinator in the daemon is reading.
func (h *RunTradeFleetCoordinatorHandler) applySpecialistTag(ctx context.Context, cmd *RunTradeFleetCoordinatorCommand, s *navigation.Ship, to string, retags map[string]string, logger common.ContainerLogger) bool {
	if s.CargoUnits() > 0 {
		return false
	}
	if to == tradeFleetLane && s.IsRetiring() {
		return false
	}
	from := s.DedicatedFleet()
	// AssignFleet is the ONLY writer of dedicated_fleet — a general Save re-reads the
	// persisted tag and discards the outgoing one, so the row would never move.
	if err := h.shipRepo.AssignFleet(ctx, s.ShipSymbol(), to, cmd.PlayerID); err != nil {
		logger.Log("WARNING", "Specialist pool: re-tag failed", map[string]interface{}{"hull": s.ShipSymbol(), "to": to, "error": err.Error()})
		return false
	}
	retags[s.ShipSymbol()] = to
	logger.Log("INFO", "Specialist pool: hull re-tagged", map[string]interface{}{"hull": s.ShipSymbol(), "from_tag": from, "to_tag": to, "pool": h.specialistPool})
	return true
}

// reconcileSpecialists settles the deferred tag changes every tick and re-derives the pool on
// the specialist cadence. Every failure leaves the fleet as it was. It returns the re-tags it
// committed, keyed by hull, so this tick's launch reads the new tag without any entity being
// mutated (sp-oq4wq).
func (h *RunTradeFleetCoordinatorHandler) reconcileSpecialists(ctx context.Context, cmd *RunTradeFleetCoordinatorCommand, all, idle []*navigation.Ship, now time.Time, logger common.ContainerLogger) (promoted, demoted int, retags map[string]string) {
	// No arm flag (RULINGS #22): unwired ports are the only off switch. Below ten cohort
	// hulls the derived pool is 0, so the legacy fleet is untouched by construction.
	if !h.specialists.ready() {
		return 0, 0, nil
	}
	// Deferred changes settle on EVERY tick, never on the cadence: a hull drains its hold
	// several times an hour and an hourly pass is almost never looking at one of those
	// moments — which is how a fully-utilised fleet stayed starved.
	retags = map[string]string{}
	promoted, demoted = h.settleSpecialistIntents(ctx, cmd, all, idle, retags, logger)

	cadenceMin := cmd.SpecialistCadenceMinutes
	if cadenceMin <= 0 {
		cadenceMin = DefaultSpecialistCadenceMinutes
	}
	if !h.specialistsAt.IsZero() && now.Sub(h.specialistsAt) < time.Duration(cadenceMin)*time.Minute {
		return promoted, demoted, retags
	}
	// N is the migrated cohort, not the whole trade fleet: a seat can only ever be drawn
	// from a trade-mvt hull, so counting legacy 'trade' hulls would size a pool that eats
	// the cohort. During migration the pool grows only as the cohort does. With no cohort
	// nothing can move, so the legacy fleet also skips the telemetry scan.
	n := 0
	for _, s := range all {
		switch s.DedicatedFleet() {
		case tradeFleetLane, tradeFleetMVT:
			n++
		}
	}
	if n == 0 {
		return promoted, demoted, retags
	}
	playerID := cmd.PlayerID.Value()
	legs, err := h.specialists.telemetry.ListByPlayer(ctx, playerID, now.Add(-specialistStatsWindow))
	if err != nil {
		logger.Log("WARNING", "Specialist pool: telemetry unreadable; pool unchanged", map[string]interface{}{"error": err.Error()})
		return promoted, demoted, retags
	}
	stats := mvt.ComputeFleetStats(legs, specialistStatsWindow)
	// No baseline is absence of evidence, not evidence of no fat lane: without it every
	// lane fails IsFatLane and the whole pool would demote on a pruned or empty window.
	if len(legs) == 0 || stats.IntraMarginPerTranche <= 0 {
		logger.Log("INFO", "Specialist pool: no intra-system baseline yet; pool unchanged", nil)
		return promoted, demoted, retags
	}
	// Stamped only once the pass has a real window to work from, so a transient read error
	// retries next tick instead of after a full cadence.
	h.specialistsAt = now
	fees := h.specialists.fees.GateFees(ctx, playerID)
	multiple := cmd.FatLaneMultiplePct
	if multiple <= 0 {
		multiple = DefaultFatLaneMultiplePct
	}
	fraction := cmd.SpecialistFractionPct
	if fraction <= 0 {
		fraction = DefaultSpecialistFractionPct
	}
	// The same fee ceiling the MVT claim guard applies, on the same visit-scaled basis: a lane
	// whose gate eats a fifth of what the crossing earns is not ground to dedicate a hull to.
	share := cmd.MVTJumpFeeMaxSharePct
	if share <= 0 {
		share = DefaultMVTJumpFeeMaxSharePct
	}
	var fat []mvt.LaneStat
	for _, l := range stats.Lanes {
		if mvt.IsFatLane(l.MarginPerTranche, l.MeanTransitSeconds, stats.CreditsPerHullSec, fees[l.Source], stats.IntraMarginPerTranche, stats.MeanMarginPerSystemVisit, multiple, share) {
			fat = append(fat, l)
		}
	}
	pool := mvt.PoolSize(len(fat), n, fraction)
	promote, demote := planSpecialists(all, idle, fat, pool, stats.PerHullMargin)
	h.markSpecialistIntents(promote, demote, pool)
	// One line per cadence: below ten cohort hulls the derived pool is 0, and a silent
	// no-op is indistinguishable from a broken pass at the live gate. promote/demote are the
	// hulls CHOSEN; what could not move now is deferred, not lost.
	logger.Log("INFO", "Specialist pool: sized", map[string]interface{}{
		"cohort": n, "pool": pool, "fat_lanes": len(fat), "promote": len(promote), "demote": len(demote),
	})
	p, d := h.settleSpecialistIntents(ctx, cmd, all, idle, retags, logger)
	promoted, demoted = promoted+p, demoted+d
	// The deferred count is this pass's own liveness signal: a plan that chose hulls but
	// committed none is waiting for a boundary, not stuck.
	logger.Log("INFO", "Specialist pool: settled", map[string]interface{}{
		"promoted": promoted, "demoted": demoted, "deferred": len(h.specialistPending),
	})
	return promoted, demoted, retags
}
