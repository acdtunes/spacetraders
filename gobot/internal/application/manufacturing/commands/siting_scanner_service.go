package commands

import (
	"context"
	"sort"
)

// This file is the SCAN step's engine (sp-vdld M2): SitingScannerService enumerates every
// candidate (good, system) factory site and applies the gates —
//
//	HARD GATE  an EXPORT market for the good exists in-system (the XX56 lesson: a factory is
//	           map-fixed; you cannot manufacture where the good only IMPORTs/EXCHANGEs).
//	SOFT GATES the recipe resolves entirely in-system (every fabricated sub-node has an
//	           in-system EXPORT factory), AND every BUY-leaf feed input has an ELIGIBLE
//	           in-system source (a5j7 supply-first: a MODERATE+ EXPORT market; the UQ16 lesson:
//	           IMPORT listings do not count), AND the market data is fresh enough to trust.
//
// It depends only on narrow ports (SitingMarketSource / RecipeFeedResolver / EligibleInputSource)
// with slim value types, so the gate logic is unit-tested with fakes; the concrete adapters over
// the market repository, SupplyChainResolver, and MarketLocator are wired in the daemon (M7).
//
// FAIL-CLOSED: siting spends money to launch a chain, so any per-candidate read error excludes
// that candidate (never launch on uncertain data). A system-level enumeration error skips that
// system rather than aborting the whole scan.

// MarketGood is one good traded at some market in a system, with its market-data age. The
// scanner filters to TradeType=="EXPORT" (the hard gate) and reads AgeSecs for the freshness gate.
type MarketGood struct {
	Good      string
	TradeType string // EXPORT / IMPORT / EXCHANGE
	AgeSecs   float64
}

// SitingMarketSource enumerates the market universe SCAN ranges over (adapted over the market
// repository in wiring).
type SitingMarketSource interface {
	// Systems returns every system symbol that has market data for the player.
	Systems(ctx context.Context, playerID int) ([]string, error)
	// GoodsInSystem returns every (good, trade-type) traded in the system, each with its
	// market-data age in seconds.
	GoodsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]MarketGood, error)
}

// RecipeFeedResolver resolves a target good's in-system recipe to its BUY-leaf feed goods
// (adapted over SupplyChainResolver.BuildDependencyTree in wiring).
type RecipeFeedResolver interface {
	// Feeds returns the BUY-leaf feed goods the chain would source at market. inSystem is false
	// when the recipe does NOT resolve entirely in-system (a fabricated node has no in-system
	// EXPORT factory) — the candidate is then excluded.
	Feeds(ctx context.Context, targetGood, systemSymbol string, playerID int) (feeds []string, inSystem bool, err error)
}

// EligibleInputSource resolves the supply-first eligible source for a feed good (adapted over
// MarketLocator.FindExportMarketBySupplyPriority in wiring).
type EligibleInputSource interface {
	// Source returns the chosen source waypoint for the feed. eligible is false when NO
	// MODERATE+ EXPORT source exists in-system (a5j7 supply-first) — the candidate is excluded.
	Source(ctx context.Context, good, systemSymbol string, playerID int) (waypoint string, eligible bool, err error)
}

// SitingScannerService is the concrete SitingScanner (SCAN). It is a pure orchestration over
// the three ports; all daemon-specific joins live in the adapters that satisfy them.
type SitingScannerService struct {
	markets SitingMarketSource
	recipes RecipeFeedResolver
	inputs  EligibleInputSource
}

// NewSitingScannerService wires the scanner from its three ports.
func NewSitingScannerService(markets SitingMarketSource, recipes RecipeFeedResolver, inputs EligibleInputSource) *SitingScannerService {
	return &SitingScannerService{markets: markets, recipes: recipes, inputs: inputs}
}

// ScanCandidates enumerates and gates candidate factory sites. Returns candidates sorted by
// Key for deterministic downstream ranking/logging.
func (s *SitingScannerService) ScanCandidates(ctx context.Context, playerID int, params SitingScanParams) ([]SitingCandidate, error) {
	systems, err := s.markets.Systems(ctx, playerID)
	if err != nil {
		return nil, err
	}

	var candidates []SitingCandidate
	for _, systemSymbol := range systems {
		goods, err := s.markets.GoodsInSystem(ctx, systemSymbol, playerID)
		if err != nil {
			// A single unreadable system must not abort the whole scan.
			continue
		}

		// HARD GATE + dedup: keep each EXPORT good once, at its freshest observation.
		exportAge := make(map[string]float64)
		for _, g := range goods {
			if g.TradeType != "EXPORT" {
				continue
			}
			if age, seen := exportAge[g.Good]; !seen || g.AgeSecs < age {
				exportAge[g.Good] = g.AgeSecs
			}
		}

		for good, age := range exportAge {
			// FRESHNESS GATE.
			if params.FreshnessMaxSecs > 0 && age > params.FreshnessMaxSecs {
				continue
			}
			// SOFT GATE 1: recipe resolves in-system with real feed inputs (a fabrication chain).
			feeds, inSystem, err := s.recipes.Feeds(ctx, good, systemSymbol, playerID)
			if err != nil || !inSystem || len(feeds) == 0 {
				continue
			}
			// SOFT GATE 2: every feed has an eligible in-system source (a5j7 supply-first).
			inputMarkets, ok := s.resolveInputs(ctx, feeds, systemSymbol, playerID)
			if !ok {
				continue
			}
			candidates = append(candidates, SitingCandidate{
				Good:         good,
				System:       systemSymbol,
				DataAgeSecs:  age,
				InputMarkets: inputMarkets,
			})
		}
	}

	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Key() < candidates[j].Key() })
	return candidates, nil
}

// resolveInputs returns the source waypoint for each feed. ok is false (excluding the
// candidate) when any feed has no eligible source or its lookup errors (fail-closed).
func (s *SitingScannerService) resolveInputs(ctx context.Context, feeds []string, systemSymbol string, playerID int) ([]string, bool) {
	inputMarkets := make([]string, 0, len(feeds))
	for _, feed := range feeds {
		waypoint, eligible, err := s.inputs.Source(ctx, feed, systemSymbol, playerID)
		if err != nil || !eligible {
			return nil, false
		}
		inputMarkets = append(inputMarkets, waypoint)
	}
	return inputMarkets, true
}
