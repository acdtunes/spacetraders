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

// planSpecialists decides tag changes over IDLE, EMPTY hulls only: orphaned specialists
// self-demote, excess specialists demote lowest-margin first, and open seats promote the idle
// trade-mvt hull standing at a fat lane's source (else sink, else lowest symbol).
func planSpecialists(all, idle []*navigation.Ship, fat []mvt.LaneStat, pool int, perHullMargin map[string]float64) (promote, demote []*navigation.Ship) {
	touches := map[string]bool{}
	for _, l := range fat {
		touches[l.Source], touches[l.Sink] = true, true
	}
	current := 0
	for _, s := range all {
		if s.DedicatedFleet() == tradeFleetLane {
			current++
		}
	}
	var idleLane, idleMVT []*navigation.Ship
	for _, s := range idle {
		// A parked hull can still be holding cargo bought for the tour path it is
		// tagged for; switching the tag would strand that load. Caught next cadence.
		if s.CargoUnits() > 0 {
			continue
		}
		switch s.DedicatedFleet() {
		case tradeFleetLane:
			idleLane = append(idleLane, s)
		case tradeFleetMVT:
			idleMVT = append(idleMVT, s)
		}
	}
	sortByMarginAsc(idleLane, perHullMargin)
	sort.Slice(idleMVT, func(i, j int) bool { return idleMVT[i].ShipSymbol() < idleMVT[j].ShipSymbol() })

	demoted := map[string]bool{}
	for _, s := range idleLane {
		if !touches[shipSystem(s)] {
			demote = append(demote, s)
			demoted[s.ShipSymbol()] = true
		}
	}
	// The excess is ranked over every surviving specialist, running ones included, so a
	// hull sheds its tag for being among the worst rather than merely for being parked.
	// A low-ranked running hull keeps it until it parks and the next cadence catches it.
	// Off the cargo-filtered buckets, never the raw idle slice: a laden hull is not
	// re-taggable through this door either.
	idleSet := map[string]bool{}
	for _, bucket := range [][]*navigation.Ship{idleLane, idleMVT} {
		for _, s := range bucket {
			idleSet[s.ShipSymbol()] = true
		}
	}
	var surviving []*navigation.Ship
	for _, s := range all {
		if s.DedicatedFleet() == tradeFleetLane && !demoted[s.ShipSymbol()] {
			surviving = append(surviving, s)
		}
	}
	sortByMarginAsc(surviving, perHullMargin)
	excess := len(surviving) - pool
	for i := 0; i < excess && i < len(surviving); i++ {
		if s := surviving[i]; idleSet[s.ShipSymbol()] {
			demote = append(demote, s)
			demoted[s.ShipSymbol()] = true
		}
	}
	seats := pool - (current - len(demote))
	if seats <= 0 || len(idleMVT) == 0 {
		return promote, demote
	}
	taken := map[string]bool{}
	pick := func(pred func(*navigation.Ship) bool) *navigation.Ship {
		for _, s := range idleMVT {
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
		s := pick(func(s *navigation.Ship) bool { return shipSystem(s) == lane.Source })
		if s == nil {
			s = pick(func(s *navigation.Ship) bool { return shipSystem(s) == lane.Sink })
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

// reconcileSpecialists derives the pool on the specialist cadence and applies tag changes to
// idle hulls. Every failure leaves the fleet as it was. It returns the re-tags it committed,
// keyed by hull, because it must NOT write them into the caller's *navigation.Ship: those
// pointers come from the 15s shipListCache and every other coordinator in the daemon is
// holding the same ones (sp-oq4wq).
func (h *RunTradeFleetCoordinatorHandler) reconcileSpecialists(ctx context.Context, cmd *RunTradeFleetCoordinatorCommand, all, idle []*navigation.Ship, now time.Time, logger common.ContainerLogger) (promoted, demoted int, retags map[string]string) {
	// No arm flag (RULINGS #22): unwired ports are the only off switch. Below ten cohort
	// hulls the derived pool is 0, so the legacy fleet is untouched by construction.
	if !h.specialists.ready() {
		return 0, 0, nil
	}
	cadenceMin := cmd.SpecialistCadenceMinutes
	if cadenceMin <= 0 {
		cadenceMin = DefaultSpecialistCadenceMinutes
	}
	if !h.specialistsAt.IsZero() && now.Sub(h.specialistsAt) < time.Duration(cadenceMin)*time.Minute {
		return 0, 0, nil
	}
	// N is the migrated cohort, not the whole trade fleet: a seat can only ever be drawn
	// from a trade-mvt hull, so counting legacy 'trade' hulls would size a pool that eats
	// the cohort. During migration the pool grows only as the cohort does. With no cohort
	// nothing can move, so the legacy fleet also skips the telemetry scan.
	n, current := 0, 0
	for _, s := range all {
		switch t := s.DedicatedFleet(); t {
		case tradeFleetLane:
			n, current = n+1, current+1
		case tradeFleetMVT:
			n++
		}
	}
	if n == 0 {
		return 0, 0, nil
	}
	playerID := cmd.PlayerID.Value()
	legs, err := h.specialists.telemetry.ListByPlayer(ctx, playerID, now.Add(-specialistStatsWindow))
	if err != nil {
		logger.Log("WARNING", "Specialist pool: telemetry unreadable; pool unchanged", map[string]interface{}{"error": err.Error()})
		return 0, 0, nil
	}
	stats := mvt.ComputeFleetStats(legs, specialistStatsWindow)
	// No baseline is absence of evidence, not evidence of no fat lane: without it every
	// lane fails IsFatLane and the whole pool would demote on a pruned or empty window.
	if len(legs) == 0 || stats.IntraMarginPerTranche <= 0 {
		logger.Log("INFO", "Specialist pool: no intra-system baseline yet; pool unchanged", nil)
		return 0, 0, nil
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
	// One line per cadence: below ten cohort hulls the derived pool is 0, and a silent
	// no-op is indistinguishable from a broken pass at the live gate.
	logger.Log("INFO", "Specialist pool: sized", map[string]interface{}{
		"cohort": n, "pool": pool, "fat_lanes": len(fat), "promote": len(promote), "demote": len(demote),
	})
	retags = map[string]string{}
	apply := func(s *navigation.Ship, to string) bool {
		from := s.DedicatedFleet()
		// AssignFleet is the ONLY writer of dedicated_fleet — a general Save re-reads the
		// persisted tag and discards the outgoing one (sp-90a3), so the row would never move.
		if err := h.shipRepo.AssignFleet(ctx, s.ShipSymbol(), to, cmd.PlayerID); err != nil {
			logger.Log("WARNING", "Specialist pool: re-tag failed", map[string]interface{}{"hull": s.ShipSymbol(), "to": to, "error": err.Error()})
			return false
		}
		retags[s.ShipSymbol()] = to
		logger.Log("INFO", "Specialist pool: hull re-tagged", map[string]interface{}{"hull": s.ShipSymbol(), "from_tag": from, "to_tag": to, "pool": pool, "fat_lanes": len(fat)})
		return true
	}
	// Demotions first, and seats are counted off the ones that COMMITTED: a demote whose
	// write failed leaves its hull in the pool, so promoting into its seat overfills it.
	for _, s := range demote {
		if apply(s, tradeFleetMVT) {
			demoted++
		}
	}
	seats := pool - (current - demoted)
	for _, s := range promote {
		if seats <= 0 {
			break
		}
		if apply(s, tradeFleetLane) {
			// Non-fatal, but never silent: a surviving claim row is an occupancy penalty
			// that steers every OTHER hull away from a system nobody works.
			if err := h.specialists.claims.Release(ctx, playerID, s.ShipSymbol()); err != nil {
				logger.Log("WARNING", "Specialist pool: claim release failed on promotion", map[string]interface{}{"hull": s.ShipSymbol(), "error": err.Error()})
			}
			promoted++
			seats--
		}
	}
	return promoted, demoted, retags
}
