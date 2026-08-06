package grpc

import (
	"context"
	"encoding/json"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// autosizerLightSources supplies the LIGHT class's demand signals.
type autosizerLightSources struct {
	shipRepo navigation.ShipRepository
	server   *DaemonServer
}

const autosizerHaulerRole = "HAULER"

func (s *autosizerLightSources) WorkerCount(ctx context.Context, playerID int) (int, error) {
	return countShips(ctx, s.shipRepo, playerID, func(sh *navigation.Ship) bool { return sh.Role() == autosizerHaulerRole })
}

func (s *autosizerLightSources) DesiredChains(ctx context.Context, playerID int) (int, error) {
	// Running standing goods_factory chains (iterations=-1) — the same portfolio the siting
	// controller enumerates. When siting is worker-limited these are the chains that need workers.
	models, err := s.server.containerRepo.ListByStatus(ctx, container.ContainerStatusRunning, &playerID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range models {
		if m.ContainerType != "goods_factory_coordinator" {
			continue
		}
		var cfg map[string]interface{}
		if m.Config != "" {
			if json.Unmarshal([]byte(m.Config), &cfg) != nil {
				continue
			}
		}
		if iter, ok := cfg["max_iterations"].(float64); ok && iter == -1 {
			n++
		}
	}
	return n, nil
}

func (s *autosizerLightSources) Vacancies(ctx context.Context, playerID int) (int, error) {
	// The rebalancer hub-vacancy query is a later enrichment (banked). 0 leaves the chain-derived
	// base demand intact (vacancies are additive).
	return 0, nil
}

// profitableLaneCounter counts the profitable, feasible trade lanes ranked across the given systems,
// read-only, off the persisted market cache. Satisfied by tradingQueries.ProfitableLaneReader.
type profitableLaneCounter interface {
	CountProfitableLanes(ctx context.Context, playerID int, systems []string) (count int, readable bool, err error)
}

type autosizerHeavySources struct {
	shipRepo   navigation.ShipRepository
	laneReader profitableLaneCounter
}

func (s *autosizerHeavySources) HeavyCount(ctx context.Context, playerID int) (int, error) {
	return countShips(ctx, s.shipRepo, playerID, func(sh *navigation.Ship) bool { return sh.DedicatedFleet() == "trade" })
}

// UnservedLaneCount surfaces the trade solver's profitable-but-unflown lane count as the heavy
// capacity-short signal: the number of profitable, feasible lanes the player's trading
// grounds rank BEYOND the current trade-hull pool. It discovers those grounds from the player's hull
// locations (the yard-price reader's system-discovery idiom), asks the read-only lane reader how many
// profitable lanes they hold, and subtracts the current heavies. READ-ONLY: it never perturbs the
// trade coordinator (it consumes the same pure trading.RankSpreads ranking off the market cache).
// Fails CLOSED (readable=false) on a genuine ship/market read failure; a readable zero (empty cache,
// no floor-clearing lane) yields 0 unserved (no demand, no buy) — not a fail-closed.
func (s *autosizerHeavySources) UnservedLaneCount(ctx context.Context, playerID int) (int, bool, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, false, nil // invalid player → unreadable → fail closed
	}
	ships, err := s.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return 0, false, err // a genuine ship read failure fails closed
	}
	profitable, readable, err := s.laneReader.CountProfitableLanes(ctx, playerID, distinctShipSystems(ships))
	if err != nil || !readable {
		return 0, false, err // unreadable lane surface → fail closed
	}
	heavies := 0
	for _, sh := range ships {
		if sh.DedicatedFleet() == "trade" {
			heavies++
		}
	}
	unserved := profitable - heavies
	if unserved < 0 {
		unserved = 0 // never a negative demand (the pool already covers the lanes)
	}
	return unserved, true, nil
}

// distinctShipSystems returns the distinct systems the player's hulls are located in — the trading
// grounds the unserved-lane count scans. Mirrors autosizerYardPriceReader's system discovery.
func distinctShipSystems(ships []*navigation.Ship) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(ships))
	for _, sh := range ships {
		loc := sh.CurrentLocation()
		if loc == nil {
			continue
		}
		system := shared.ExtractSystemSymbol(loc.Symbol)
		if _, ok := seen[system]; ok {
			continue
		}
		seen[system] = struct{}{}
		out = append(out, system)
	}
	return out
}

func countShips(ctx context.Context, shipRepo navigation.ShipRepository, playerID int, pred func(*navigation.Ship) bool) (int, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, err
	}
	ships, err := shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, s := range ships {
		if pred(s) {
			n++
		}
	}
	return n, nil
}
