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
	// buy-leg the contract worker skips when a good is pre-positioned.
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
	// CurrentEraID resolves the era (universe) a player belongs to, so a runtime mine can confine
	// its delivery-system scope to the CURRENT universe: system symbols are reused across
	// weekly resets, so a nil era would otherwise aggregate homonymous systems from every past
	// universe. Returns nil when unknown (fail-open to all-eras).
	CurrentEraID(ctx context.Context, playerID int) (*int, error)
}

// marketAskFinder is the pair of cheapest-ask lookups the miner joins against: the
// all-systems scan for the cheapest SOURCE ask anywhere (home OR foreign) and
// the in-system scan for the HOME ask (the contract-source alternative the worker would
// otherwise buy at). Satisfied by *MarketRepositoryGORM. Kept as a local narrow interface
// (not the application/contract CrossSystemMarketFinder port) so the miner couples only to
// the method shapes it uses.
type marketAskFinder interface {
	FindCheapestMarketsSellingAllSystems(ctx context.Context, goodSymbol string, playerID int, limit int) ([]market.CheapestMarketResult, error)
	FindCheapestMarketSelling(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*market.CheapestMarketResult, error)
}

// CandidateRankMode selects how Mine ranks candidates before the TopN cull. The ranking is
// load-bearing precisely because TopN TRUNCATES the ranked list — a good sorted below the cut
// never reaches the consumer's knapsack — so the ranking must match what the CONSUMER values.
type CandidateRankMode int

const (
	// RankBySavings (the zero value) is the SOURCE-side stocker ordering: stock-eligible goods
	// first, then by total projected buy-leg savings. It is the default so every existing caller —
	// the source warehouse, the stocker coordinator, the tour deposit sink, the CLI — is
	// unaffected: they pass no rank mode and keep the exact savings ordering.
	RankBySavings CandidateRankMode = iota
	// RankByContractReward is the DESTINATION-side depot receipt ordering: by total
	// CONTRACT-REWARD value (ContractCount × ContractRewardPerUnit). The depot buffer's receipt
	// knapsack (PlanReceiptCaps) values received goods by contract reward, so its candidate feed
	// must be culled by reward too — otherwise the savings cull drops high-reward/low-savings goods
	// (MEDICINE/CLOTHING-like) BEFORE the reward knapsack ever weighs them.
	RankByContractReward
)

// DemandMinerOptions parametrizes the miner (RULINGS #5). Zero values fall back to
// the package defaults.
type DemandMinerOptions struct {
	MinRecurrence int // drop goods demanded by fewer than this many contracts (<1 => DefaultMinRecurrence)
	TopN          int // cap on ranked rows returned (<=0 => DefaultTopN)
	// BuyLegSavingsPerUnit is the per-unit value of the source→central buy-leg the
	// contract worker skips when a good is pre-positioned. Added to the
	// price differential so an IN-SYSTEM-sourceable good (cheapest source == home,
	// differential 0) still clears the "savings > 0" guard. <=0 =>
	// DefaultBuyLegSavingsPerUnit (fail OPEN for the in-system case).
	BuyLegSavingsPerUnit int
	// RankBy selects the ranking the TopN cull truncates against. The zero value (RankBySavings)
	// preserves the source-side stocker ordering byte-identically; the DEPOT receipt path sets
	// RankByContractReward so a high-reward/low-savings good is not culled before the reward
	// knapsack.
	RankBy CandidateRankMode
}

// DemandCandidate is one ranked pre-positioning row: a recurrently-contracted good
// joined to the cheapest SOURCE market anywhere that sells it (home OR foreign)
// and, when the home system sells it, the home ask plus the per-unit savings.
//
// The Foreign* field names are a naming artifact of an earlier cross-system-only
// version: they now carry the cheapest source ANYWHERE, which may be a market in the
// HOME system itself. The consumers (stocker buy leg, tour deposit sink) buy at
// ForeignMarket regardless of its system — an in-system source is trivially reachable
// (0 jumps) and hauled to the central warehouse.
//
// A row with NO market anywhere (not even home) is DROPPED — nothing to source, nowhere
// to buy, so it cannot be pre-positioned (fail closed, RULINGS #4; this is not the
// in-system-sourcing case above). A row with a source but no known HOME ask is RETAINED
// but flagged StockEligible=false: it is informative for the captain (a "home never
// sells the good" signal, design §5 Q5) while remaining unstockable for the deposit
// guard, which needs a known home ask to price the contract-source alternative.
type DemandCandidate struct {
	Good          string `json:"good"`
	ContractCount int    `json:"contract_count"`
	DemandUnits   int    `json:"demand_units"`
	// MaxContractUnits is the largest SINGLE contract's size for the good — the s_G the
	// warehouse auto-cap knapsack buffers FULLY or not at all. 0 when unknown.
	MaxContractUnits     int     `json:"max_contract_units"`
	RecurrenceWindowDays float64 `json:"recurrence_window_days"`

	ForeignMarket string `json:"foreign_market"` // cheapest SOURCE waypoint anywhere (may be in the home system)
	ForeignSystem string `json:"foreign_system"`
	ForeignAsk    int    `json:"foreign_ask"` // the source ask (cheapest anywhere) — what the stocker pays to buy

	HomeAsk      int  `json:"home_ask"` // 0 when the home system does not sell the good
	HomeAskKnown bool `json:"home_ask_known"`

	// ContractRewardPerUnit is the per-unit CONTRACT REWARD for the good in the mined
	// (delivery) system — what the destination's contracts actually PAY for a delivered unit,
	// carried through from ContractGoodDemand.RewardPerUnit. It is the TRUE value
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

// DemandMiner produces the demand signal: the goods contract history
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
// ANYWHERE (home OR foreign) [required, else drop: no source = nothing to
// pre-position] + home ask [optional, prices the contract-source alternative] -> per-unit
// savings = (home ask + buy-leg) − source ask -> rank (opts.RankBy: RankBySavings, the default —
// stock-eligible first, then total projected savings; or RankByContractReward for the depot
// receipt path — total contract-reward value) -> TopN.
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

	// A nil era at a RUNTIME call site means "the current universe", NOT "all universes": resolve
	// the current player's era so the delivery-system scope below cannot aggregate a past
	// universe's contracts reached via a REUSED system symbol. SpaceTraders regenerates
	// the universe each weekly reset and reuses system symbols, so homeSystem alone does not pin a
	// universe. An explicit era (the CLI history-analysis path) is honored as-is; an unresolved era
	// fails open to all-eras (the prior behavior).
	if eraID == nil {
		resolved, err := m.demand.CurrentEraID(ctx, playerID)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve current era for player %d: %w", playerID, err)
		}
		eraID = resolved
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
			ContractRewardPerUnit: d.RewardPerUnit, // true contract-reward signal, carried for the depot buffer
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
			// value (in-system pre-positioning is worthwhile, fail OPEN).
			c.HomeAsk = home.SellPrice
			c.HomeAskKnown = true
			c.ProjectedSavingsPerUnit = home.SellPrice + buyLeg - source.SellPrice
			c.StockEligible = c.ProjectedSavingsPerUnit > 0
		}

		candidates = append(candidates, c)
	}

	sort.SliceStable(candidates, candidateRankLess(candidates, opts.RankBy))

	if len(candidates) > topN {
		candidates = candidates[:topN]
	}
	return candidates, nil
}

// candidateRankLess picks the ordering the TopN cull truncates against. RankBySavings (the zero
// value) is the SOURCE-side stocker ordering, left byte-identical so every source / stocker / tour
// / CLI caller is unaffected. RankByContractReward is the DESTINATION-side depot receipt ordering.
// The comparator is chosen ONCE here, never per-comparison, so the sort stays stable.
func candidateRankLess(candidates []DemandCandidate, mode CandidateRankMode) func(i, j int) bool {
	if mode == RankByContractReward {
		return func(i, j int) bool {
			return lessByContractReward(candidates[i], candidates[j])
		}
	}
	return func(i, j int) bool {
		return lessBySavings(candidates[i], candidates[j])
	}
}

// lessBySavings is the SOURCE-side stocker ranking (UNCHANGED — this is the exact comparator the
// miner has always used): stock-eligible rows first, then by total projected buy-leg savings
// (per-unit × demand) desc, then recurrence desc, then good symbol for a stable tiebreak.
func lessBySavings(a, b DemandCandidate) bool {
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
}

// lessByContractReward is the DESTINATION-side depot receipt ranking: by total
// CONTRACT-REWARD value (ContractCount × ContractRewardPerUnit) desc — the same value axis the
// receipt knapsack (PlanReceiptCaps) itself optimizes — so a high-reward/low-savings good is NOT
// culled by the savings sort + TopN before the knapsack weighs it. It deliberately does NOT gate
// on StockEligible: that is a buy-leg-savings concept irrelevant to receipt demand, and gating on
// it is the very defect that sank the high-reward goods below every eligible filler. Ties fall to
// recurrence then good symbol for a stable ordering.
func lessByContractReward(a, b DemandCandidate) bool {
	aValue := a.ContractRewardPerUnit * float64(a.ContractCount)
	bValue := b.ContractRewardPerUnit * float64(b.ContractCount)
	if aValue != bValue {
		return aValue > bValue // by total contract-reward value desc
	}
	if a.ContractCount != b.ContractCount {
		return a.ContractCount > b.ContractCount // then by recurrence desc
	}
	return a.Good < b.Good // stable tiebreak
}

// cheapestSourceMarket returns the cheapest market selling good ANYWHERE — home system OR
// foreign. It no longer excludes the home system: when home is the only scanned
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
