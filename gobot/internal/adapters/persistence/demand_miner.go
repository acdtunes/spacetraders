package persistence

import (
	"context"
	"fmt"
	"sort"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Demand-miner tunables. RULINGS #5: operational thresholds are parametrized, not
// hardcoded — these are the defaults the CLI flags fall back to, not fixed limits.
const (
	// DefaultMinRecurrence is the floor on how many distinct contracts must have
	// demanded a good before it counts as recurring (never speculative — RULINGS #6).
	DefaultMinRecurrence = 2
	// DefaultTopN caps the ranked candidate rows returned when the caller does not
	// specify its own limit.
	DefaultTopN = 20
	// DefaultBuyLegSavingsPerUnit is the per-unit value credited to the source→central
	// buy-leg the contract worker skips when a good is pre-positioned (sp-layd reframe).
	// It is what makes IN-SYSTEM pre-positioning worthwhile even when the cheapest source
	// IS the home system (price differential 0): the warehouse compresses the export→A1
	// haul the worker would otherwise fly. A small positive default keeps the in-system
	// case FAIL-OPEN (a home-sourceable recurrent good clears the "savings > 0" guard by
	// default) while the captain/analyst tunes it up to the real haul value (RULINGS #5 —
	// config wins, this is only the fallback).
	DefaultBuyLegSavingsPerUnit = 1
	// sourceMarketScanLimit bounds the cross-system cheapest-ask scan per good. The miner
	// keeps only the cheapest source overall, and few systems have scanned data, so a
	// small window suffices (mirrors the sourcing optimizer's crossSystemCandidateLimit).
	sourceMarketScanLimit = 25
)

// contractDemandSource yields the units-aware, home-scoped per-good contract demand
// the miner ranks. Satisfied by *HistoryRepository.
type contractDemandSource interface {
	ContractGoodDemand(ctx context.Context, eraID *int, deliverySystem *string) ([]ContractGoodDemand, error)
}

// marketAskFinder is the pair of cheapest-ask lookups the miner joins against: the
// all-systems scan for the cheapest SOURCE ask anywhere (home OR foreign — sp-layd) and
// the in-system scan for the HOME ask (the contract-source alternative the worker would
// otherwise buy at). Satisfied by *MarketRepositoryGORM. Kept as a local narrow interface
// (not the application/contract CrossSystemMarketFinder port) so the miner couples only to
// the method shapes it uses.
type marketAskFinder interface {
	FindCheapestMarketsSellingAllSystems(ctx context.Context, goodSymbol string, playerID int, limit int) ([]market.CheapestMarketResult, error)
	FindCheapestMarketSelling(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*market.CheapestMarketResult, error)
}

// DemandMinerOptions parametrizes the miner (RULINGS #5). Zero values fall back to
// the package defaults.
type DemandMinerOptions struct {
	MinRecurrence int // drop goods demanded by fewer than this many contracts (<1 => DefaultMinRecurrence)
	TopN          int // cap on ranked rows returned (<=0 => DefaultTopN)
	// BuyLegSavingsPerUnit is the per-unit value of the source→central buy-leg the
	// contract worker skips when a good is pre-positioned (sp-layd). Added to the
	// price differential so an IN-SYSTEM-sourceable good (cheapest source == home,
	// differential 0) still clears the "savings > 0" guard. <=0 =>
	// DefaultBuyLegSavingsPerUnit (fail OPEN for the in-system case).
	BuyLegSavingsPerUnit int
}

// DemandCandidate is one ranked pre-positioning row: a recurrently-contracted good
// joined to the cheapest SOURCE market anywhere that sells it (home OR foreign — sp-layd)
// and, when the home system sells it, the home ask plus the per-unit savings.
//
// The Foreign* field names are HISTORICAL (sp-dchv shipped cross-system-only): they now
// carry the cheapest source ANYWHERE, which may be a market in the HOME system itself.
// The consumers (stocker buy leg, tour deposit sink) buy at ForeignMarket regardless of
// its system — an in-system source is trivially reachable (0 jumps) and hauled to the
// central warehouse.
//
// A row with NO market anywhere (not even home) is DROPPED — nothing to source, nowhere
// to buy, so it cannot be pre-positioned (fail closed, RULINGS #4; this is NOT the
// in-system case the reframe protects). A row with a source but no known HOME ask is
// RETAINED but flagged StockEligible=false: it is informative for the captain (the
// sp-dchv "home never sells the good" signal, design §5 Q5) while remaining unstockable
// for the deposit guard, which needs a known home ask to price the contract-source
// alternative.
type DemandCandidate struct {
	Good          string `json:"good"`
	ContractCount int    `json:"contract_count"`
	DemandUnits   int    `json:"demand_units"`
	// MaxContractUnits is the largest SINGLE contract's size for the good — the s_G the
	// warehouse auto-cap knapsack buffers FULLY or not at all (sp-5n7v). 0 when unknown.
	MaxContractUnits     int     `json:"max_contract_units"`
	RecurrenceWindowDays float64 `json:"recurrence_window_days"`

	ForeignMarket string `json:"foreign_market"` // cheapest SOURCE waypoint anywhere (may be in the home system)
	ForeignSystem string `json:"foreign_system"`
	ForeignAsk    int    `json:"foreign_ask"` // the source ask (cheapest anywhere) — what the stocker pays to buy

	HomeAsk      int  `json:"home_ask"` // 0 when the home system does not sell the good
	HomeAskKnown bool `json:"home_ask_known"`

	// ContractRewardPerUnit is the per-unit CONTRACT REWARD for the good in the mined
	// (delivery) system — what the destination's contracts actually PAY for a delivered unit,
	// carried through from ContractGoodDemand.RewardPerUnit (sp-64se). It is the TRUE value
	// signal a destination-side depot buffer ranks by; a market ask (HomeAsk/ForeignAsk) is a
	// RESALE proxy that mis-ranks import-only goods. 0 when no contract payment is known.
	ContractRewardPerUnit float64 `json:"contract_reward_per_unit"`

	// ProjectedSavingsPerUnit is the per-unit saving vs the CONTRACT-SOURCE ALTERNATIVE:
	// (HomeAsk + buy-leg) − ForeignAsk when the home ask is known, else 0. The buy-leg
	// term is what makes an in-system-sourceable good (ForeignAsk == HomeAsk) show a
	// positive saving — the warehouse compresses the export→central haul the worker skips.
	ProjectedSavingsPerUnit int  `json:"projected_savings_per_unit"`
	StockEligible           bool `json:"stock_eligible"` // home ask known AND savings > 0
}

// DemandMiner produces the sp-dchv Lane A demand signal: the goods contract history
// keeps needing, home-scoped, joined to where they are cheap. It is READ-ONLY — no
// spending, no dispatch — feeding the deposit economics guard and giving the captain
// visibility now.
type DemandMiner struct {
	demand  contractDemandSource
	markets marketAskFinder
}

// NewDemandMiner wires the miner over a single DB connection (its own HistoryRepository
// for contract demand and MarketRepository for asks), mirroring connectHistoryRepository.
func NewDemandMiner(db *gorm.DB) *DemandMiner {
	return &DemandMiner{
		demand:  NewHistoryRepository(db),
		markets: NewMarketRepository(db),
	}
}

// NewDemandMinerWithSources wires a miner over explicit demand + market sources. It exists
// so callers in other packages (and integration tests) can compose a real miner over
// fakes without a live DB — the parameter interfaces are unexported, but a value that
// satisfies them can still be passed from anywhere.
func NewDemandMinerWithSources(demand contractDemandSource, markets marketAskFinder) *DemandMiner {
	return &DemandMiner{demand: demand, markets: markets}
}

// Mine ranks the pre-positioning candidates for homeSystem. homeSystem is an EXPLICIT
// parameter — there is no global "home" anchor today (design §5 Q1), so the caller
// (CLI flag) must supply it. eraID scopes the contract history (nil = all eras; the
// home-system filter already confines demand to the current universe's waypoints).
//
// Pipeline: home-scoped demand -> minRecurrence filter -> per good, cheapest SOURCE ask
// ANYWHERE (home OR foreign — sp-layd) [required, else drop: no source = nothing to
// pre-position] + home ask [optional, prices the contract-source alternative] -> per-unit
// savings = (home ask + buy-leg) − source ask -> rank (stock-eligible first, then by total
// projected savings) -> TopN.
func (m *DemandMiner) Mine(ctx context.Context, homeSystem string, playerID int, eraID *int, opts DemandMinerOptions) ([]DemandCandidate, error) {
	if homeSystem == "" {
		return nil, fmt.Errorf("home system is required (no default anchor — design §5 Q1)")
	}
	minRecurrence := opts.MinRecurrence
	if minRecurrence < 1 {
		minRecurrence = DefaultMinRecurrence
	}
	topN := opts.TopN
	if topN <= 0 {
		topN = DefaultTopN
	}
	buyLeg := opts.BuyLegSavingsPerUnit
	if buyLeg <= 0 {
		buyLeg = DefaultBuyLegSavingsPerUnit
	}

	rows, err := m.demand.ContractGoodDemand(ctx, eraID, &homeSystem)
	if err != nil {
		return nil, fmt.Errorf("failed to load contract demand: %w", err)
	}

	candidates := make([]DemandCandidate, 0, len(rows))
	for _, d := range rows {
		if d.ContractCount < minRecurrence {
			continue
		}

		source, err := m.cheapestSourceMarket(ctx, d.Good, playerID)
		if err != nil {
			return nil, err
		}
		if source == nil {
			continue // no market sells it anywhere => nothing to source => drop (fail closed)
		}

		c := DemandCandidate{
			Good:                  d.Good,
			ContractCount:         d.ContractCount,
			DemandUnits:           d.UnitsRequired,
			MaxContractUnits:      d.MaxContractUnits,
			RecurrenceWindowDays:  windowDays(d.FirstSeen, d.LastSeen),
			ContractRewardPerUnit: d.RewardPerUnit, // true contract-reward signal (sp-64se), carried for the depot buffer
			ForeignMarket:         source.WaypointSymbol,
			ForeignSystem:         shared.ExtractSystemSymbol(source.WaypointSymbol),
			ForeignAsk:            source.SellPrice,
		}

		home, err := m.markets.FindCheapestMarketSelling(ctx, d.Good, homeSystem, playerID)
		if err != nil {
			return nil, fmt.Errorf("failed to find home ask for %s: %w", d.Good, err)
		}
		if home != nil {
			// Savings vs the contract-source alternative: the worker would source in-system
			// at the home ask AND fly the export→delivery buy-leg; the warehouse buys at the
			// cheapest source anywhere and pre-positions centrally. When the cheapest source
			// IS the home system, the differential is 0 and the buy-leg alone carries the
			// value (sp-layd: in-system pre-positioning is worthwhile, fail OPEN).
			c.HomeAsk = home.SellPrice
			c.HomeAskKnown = true
			c.ProjectedSavingsPerUnit = home.SellPrice + buyLeg - source.SellPrice
			c.StockEligible = c.ProjectedSavingsPerUnit > 0
		}

		candidates = append(candidates, c)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.StockEligible != b.StockEligible {
			return a.StockEligible // stock-eligible rows rank first
		}
		aTotal := a.ProjectedSavingsPerUnit * a.DemandUnits
		bTotal := b.ProjectedSavingsPerUnit * b.DemandUnits
		if aTotal != bTotal {
			return aTotal > bTotal // then by total projected savings desc
		}
		if a.ContractCount != b.ContractCount {
			return a.ContractCount > b.ContractCount // then by recurrence desc
		}
		return a.Good < b.Good // stable tiebreak
	})

	if len(candidates) > topN {
		candidates = candidates[:topN]
	}
	return candidates, nil
}

// cheapestSourceMarket returns the cheapest market selling good ANYWHERE — home system OR
// foreign (sp-layd). It no longer excludes the home system: when home is the only scanned
// system (post-weekly-reset), the home export IS the cheapest source and the good must be
// pre-positionable from it, not dropped. Returns nil only when NO market anywhere sells the
// good. Market data exists only for scouted systems, so "has scanned data" doubles as the
// reachability filter — the same working definition the sourcing optimizer uses.
func (m *DemandMiner) cheapestSourceMarket(ctx context.Context, good string, playerID int) (*market.CheapestMarketResult, error) {
	all, err := m.markets.FindCheapestMarketsSellingAllSystems(ctx, good, playerID, sourceMarketScanLimit)
	if err != nil {
		return nil, fmt.Errorf("failed to scan source markets for %s: %w", good, err)
	}
	if len(all) == 0 {
		return nil, nil // no market sells it anywhere
	}
	// Results are cheapest-first (sell_price ASC), so the first is the cheapest source.
	return &all[0], nil
}

func windowDays(first, last time.Time) float64 {
	if first.IsZero() || last.IsZero() || !last.After(first) {
		return 0
	}
	return last.Sub(first).Hours() / 24
}
