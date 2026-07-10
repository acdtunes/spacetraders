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
	// foreignMarketScanLimit bounds the cross-system cheapest-ask scan per good. The
	// miner keeps only the cheapest FOREIGN market, and few systems have scanned data,
	// so a small window suffices (mirrors the sourcing optimizer's crossSystemCandidateLimit).
	foreignMarketScanLimit = 25
)

// contractDemandSource yields the units-aware, home-scoped per-good contract demand
// the miner ranks. Satisfied by *HistoryRepository.
type contractDemandSource interface {
	ContractGoodDemand(ctx context.Context, eraID *int, deliverySystem *string) ([]ContractGoodDemand, error)
}

// marketAskFinder is the pair of cheapest-ask lookups the miner joins against: the
// cross-system scan for the cheapest FOREIGN ask and the in-system scan for the HOME
// ask. Satisfied by *MarketRepositoryGORM. Kept as a local narrow interface (not the
// application/contract CrossSystemMarketFinder port) so the miner couples only to the
// method shapes it uses.
type marketAskFinder interface {
	FindCheapestMarketsSellingAllSystems(ctx context.Context, goodSymbol string, playerID int, limit int) ([]market.CheapestMarketResult, error)
	FindCheapestMarketSelling(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*market.CheapestMarketResult, error)
}

// DemandMinerOptions parametrizes the miner (RULINGS #5). Zero values fall back to
// the package defaults.
type DemandMinerOptions struct {
	MinRecurrence int // drop goods demanded by fewer than this many contracts (<1 => DefaultMinRecurrence)
	TopN          int // cap on ranked rows returned (<=0 => DefaultTopN)
}

// DemandCandidate is one ranked pre-positioning row: a recurrently-contracted good
// joined to the cheapest FOREIGN market that sells it and, when the home system sells
// it, the home ask plus the per-unit savings.
//
// A row with no known foreign ask is DROPPED — there is nowhere to pre-position from,
// so it is not a candidate (fail closed, RULINGS #4). A row with no known home ask is
// RETAINED but flagged StockEligible=false: it is informative for the captain (and is
// the sp-dchv "home never sells the good" signal, design §5 Q5) while remaining
// unstockable for the deposit guard, which needs a known home ask to price savings.
type DemandCandidate struct {
	Good                 string  `json:"good"`
	ContractCount        int     `json:"contract_count"`
	DemandUnits          int     `json:"demand_units"`
	RecurrenceWindowDays float64 `json:"recurrence_window_days"`

	ForeignMarket string `json:"foreign_market"` // cheapest foreign waypoint selling the good
	ForeignSystem string `json:"foreign_system"`
	ForeignAsk    int    `json:"foreign_ask"`

	HomeAsk      int  `json:"home_ask"` // 0 when the home system does not sell the good
	HomeAskKnown bool `json:"home_ask_known"`

	ProjectedSavingsPerUnit int  `json:"projected_savings_per_unit"` // HomeAsk-ForeignAsk when both known, else 0
	StockEligible           bool `json:"stock_eligible"`             // both asks known AND savings > 0
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

// Mine ranks the pre-positioning candidates for homeSystem. homeSystem is an EXPLICIT
// parameter — there is no global "home" anchor today (design §5 Q1), so the caller
// (CLI flag) must supply it. eraID scopes the contract history (nil = all eras; the
// home-system filter already confines demand to the current universe's waypoints).
//
// Pipeline: home-scoped demand -> minRecurrence filter -> per good, cheapest FOREIGN
// ask (home-system markets excluded) [required, else drop] + home ask [optional] ->
// per-unit savings -> rank (stock-eligible first, then by total projected savings) ->
// TopN.
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

	rows, err := m.demand.ContractGoodDemand(ctx, eraID, &homeSystem)
	if err != nil {
		return nil, fmt.Errorf("failed to load contract demand: %w", err)
	}

	candidates := make([]DemandCandidate, 0, len(rows))
	for _, d := range rows {
		if d.ContractCount < minRecurrence {
			continue
		}

		foreign, err := m.cheapestForeignMarket(ctx, d.Good, homeSystem, playerID)
		if err != nil {
			return nil, err
		}
		if foreign == nil {
			continue // no reachable foreign source => cannot pre-position => drop (fail closed)
		}

		c := DemandCandidate{
			Good:                 d.Good,
			ContractCount:        d.ContractCount,
			DemandUnits:          d.UnitsRequired,
			RecurrenceWindowDays: windowDays(d.FirstSeen, d.LastSeen),
			ForeignMarket:        foreign.WaypointSymbol,
			ForeignSystem:        shared.ExtractSystemSymbol(foreign.WaypointSymbol),
			ForeignAsk:           foreign.SellPrice,
		}

		home, err := m.markets.FindCheapestMarketSelling(ctx, d.Good, homeSystem, playerID)
		if err != nil {
			return nil, fmt.Errorf("failed to find home ask for %s: %w", d.Good, err)
		}
		if home != nil {
			c.HomeAsk = home.SellPrice
			c.HomeAskKnown = true
			c.ProjectedSavingsPerUnit = home.SellPrice - foreign.SellPrice
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

// cheapestForeignMarket returns the cheapest market selling good OUTSIDE homeSystem,
// or nil when only the home system (or no system) sells it. Market data exists only
// for scouted systems, so "has scanned data" doubles as the reachability filter — the
// same working definition the sourcing optimizer uses for cross-system candidates.
func (m *DemandMiner) cheapestForeignMarket(ctx context.Context, good, homeSystem string, playerID int) (*market.CheapestMarketResult, error) {
	all, err := m.markets.FindCheapestMarketsSellingAllSystems(ctx, good, playerID, foreignMarketScanLimit)
	if err != nil {
		return nil, fmt.Errorf("failed to scan foreign markets for %s: %w", good, err)
	}
	// Results are cheapest-first, so the first non-home market is the cheapest foreign one.
	for i := range all {
		if shared.ExtractSystemSymbol(all[i].WaypointSymbol) != homeSystem {
			return &all[i], nil
		}
	}
	return nil, nil
}

func windowDays(first, last time.Time) float64 {
	if first.IsZero() || last.IsZero() || !last.After(first) {
		return 0
	}
	return last.Sub(first).Hours() / 24
}
