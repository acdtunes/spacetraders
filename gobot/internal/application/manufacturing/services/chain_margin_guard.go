package services

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// sp-2dv4 (money-integrity #3). A SHIP_PARTS goods factory bled -675k in 18min:
// it bought 1.17M of ELECTRONICS/EQUIPMENT feed and DELIVERED it into the fab
// market's import bid at -47% per churn (our own prior dumping had crushed those
// import bids -38..-61%), and its only recovery sink bid on the finished good
// had trade volume 6 — far too small to ever absorb a 7-figure feed spend. The
// factory model priced the feed leg at stale/assumed spreads and never re-checked
// live, so nothing stopped the spend. The 9aoc solvency floor parked the buys at
// 87k as designed, but the bleed happened BEFORE the floor: the economics were
// negative from the first buy.
//
// ChainMarginGuard closes that gap with TWO checks, both run BEFORE any feed is
// bought and both using LIVE market reads:
//
//	Guard 1 — chain margin: project the whole chain's P&L =
//	  Σ_feed (fab_import_bid − source_ask)·vol  +  (final_sink_bid − fab_ask)·vol
//	and refuse to start when it does not clear a safety margin. Crushed feed
//	import bids drive this negative — Guard 1 alone would have refused the incident.
//
//	Guard 2 — absorption-bounded spend: cap one chain pass's total feed spend at
//	what the FINAL sink can absorb (sink_bid × sink_trade_volume × a bounded number
//	of tranches), NOT at the treasury. A vol-6 sink therefore caps feed spend to a
//	commensurately small number regardless of how much cash is on hand.
//
// It is strictly additive and UPSTREAM: it does not touch the 9aoc solvency
// floor, the bp6f #3 crushed-sink harvest guard (production_executor PollForProduction),
// or the vsfn worker-park path (run_factory_coordinator executeLevelParallel). It
// fails CLOSED — an unpriceable stage parks rather than spends — mirroring 9aoc's
// discipline: can't price it, don't spend on it.
const (
	// chainMarginSafetyFraction is the safety margin on Guard 1: a chain must
	// project a profit of at least this fraction of its feed spend before we
	// commit a single feed buy. "> 0 with a safety margin" — a razor-thin or zero
	// projected margin parks. Conservative buffer against price slippage between
	// projection and execution; tune here.
	chainMarginSafetyFraction = 0.05

	// sinkAbsorptionTranches is how many product trade-volume churns the final
	// sink is assumed to absorb before its bid decays under our own delivery.
	// One chain pass's feed spend must fit inside sink_bid × sink_volume ×
	// sinkAbsorptionTranches; a small-volume sink yields a small cap. Tune here.
	sinkAbsorptionTranches = 4
)

// chainGuardReason is the machine-readable outcome of a projection.
type chainGuardReason string

const (
	chainGuardProceed        chainGuardReason = "proceed"
	chainGuardNegativeMargin chainGuardReason = "negative_chain_margin"
	chainGuardSinkAbsorption chainGuardReason = "feed_spend_exceeds_absorption"
	chainGuardUnpriceable    chainGuardReason = "unpriceable_fail_closed"
	chainGuardNoFabrication  chainGuardReason = "no_fabrication_chain"
)

// ChainMarginGuard projects a fabrication chain's live P&L and its final sink's
// absorption capacity BEFORE any feed is bought (sp-2dv4). See file header.
type ChainMarginGuard struct {
	marketLocator *MarketLocator
	marketRepo    market.MarketRepository
}

// NewChainMarginGuard builds the guard from the same market accessors the
// factory coordinator already holds. Live source/sink prices come from the
// MarketLocator (the same FindExportMarket / FindImportMarket the buy and
// harvest paths use); a fab's per-input import bid comes from marketRepo.
func NewChainMarginGuard(marketLocator *MarketLocator, marketRepo market.MarketRepository) *ChainMarginGuard {
	return &ChainMarginGuard{marketLocator: marketLocator, marketRepo: marketRepo}
}

// ChainProjection is the structured, loggable result of a pre-spend projection.
// The container-log renderer prints only level+message and drops metadata
// (sp-iqyq), so the park/pass messages carry every number in their TEXT while
// the same fields are also exposed as metadata for structured consumers.
type ChainProjection struct {
	Proceed       bool
	Reason        chainGuardReason
	Good          string
	ProjectedPL   int      // summed chain P&L across all stages, live prices
	RequiredPL    int      // chainMarginSafetyFraction × FeedSpend
	FeedSpend     int      // total raw-input (feed) purchase cost for one chain pass
	AbsorptionCap int      // SinkBid × SinkVolume × sinkAbsorptionTranches
	SinkBid       int      // final resale sink's live bid
	SinkVolume    int      // final resale sink's trade volume
	Stages        []string // compact per-stage descriptors, embedded in the message text
	Detail        string   // extra context (e.g. the market-read error) for fail-closed
}

// Evaluate projects the chain rooted at root and returns the pre-spend decision.
// It NEVER returns an error: an unpriceable stage becomes a fail-closed park so
// the caller has one thing to check — proj.Proceed.
func (g *ChainMarginGuard) Evaluate(ctx context.Context, root *goods.SupplyChainNode, systemSymbol string, playerID int) ChainProjection {
	proj := ChainProjection{}
	if root != nil {
		proj.Good = root.Good
	}

	// Only a fabrication chain that resells a product has both feed legs to price
	// and a resale sink to bound. A pure BUY root (no fabrication) has no feed
	// bleed to prevent — let it through untouched.
	if root == nil || root.IsLeaf() || root.AcquisitionMethod != goods.AcquisitionFabricate {
		proj.Proceed = true
		proj.Reason = chainGuardNoFabrication
		return proj
	}

	// Final resale sink for the product: the best market buying it — the same
	// call the bp6f #3 harvest guard uses. Its bid and volume bound both the
	// product leg's revenue and the absorption cap. Fail CLOSED if unpriceable.
	sink, err := g.marketLocator.FindImportMarket(ctx, root.Good, systemSymbol, playerID)
	if err != nil || sink == nil {
		return failClosed(proj, fmt.Sprintf("no priceable resale sink for %s: %v", root.Good, err))
	}
	proj.SinkBid = sink.Price
	proj.SinkVolume = sink.TradeVolume

	// Project every stage's live P&L, delivering the finished product into the
	// sink bid. Any unpriceable stage fails the whole chain closed.
	pl, feedSpend, perr := g.branchPL(ctx, root, sink.Price, sink.TradeVolume, systemSymbol, playerID, &proj.Stages)
	if perr != nil {
		return failClosed(proj, perr.Error())
	}
	proj.ProjectedPL = pl
	proj.FeedSpend = feedSpend
	proj.RequiredPL = int(float64(feedSpend) * chainMarginSafetyFraction)
	proj.AbsorptionCap = sink.Price * sink.TradeVolume * sinkAbsorptionTranches

	// Guard 1 — live chain margin. Crushed feed import bids drive ProjectedPL
	// negative; a thin margin fails to clear RequiredPL. Either parks pre-spend.
	if proj.ProjectedPL <= proj.RequiredPL {
		proj.Proceed = false
		proj.Reason = chainGuardNegativeMargin
		return proj
	}

	// Guard 2 — absorption-bounded spend. Even a per-unit-profitable chain parks
	// if one pass's feed spend exceeds what the final sink can ever absorb.
	if proj.FeedSpend > proj.AbsorptionCap {
		proj.Proceed = false
		proj.Reason = chainGuardSinkAbsorption
		return proj
	}

	proj.Proceed = true
	proj.Reason = chainGuardProceed
	return proj
}

// branchPL projects the live P&L of obtaining node.Good and delivering it into
// deliverBid at up to deliverVol units, plus the recursive P&L of obtaining its
// inputs. Returns (P&L, feed spend on raw BUY inputs, error). Any market-read
// failure returns an error so Evaluate fails CLOSED.
//
//   - BUY leaf (a raw feed input): buy at the source ask, deliver into deliverBid
//     (the fab's import bid). Leg P&L = (deliverBid − source_ask)·vol; its cost is
//     counted as feed spend.
//   - FABRICATE node: harvest node.Good at the fab ask and deliver into deliverBid
//     (for the root, that's the final sink; for an intermediate, the parent fab's
//     import bid). Leg P&L = (deliverBid − fab_ask)·vol. Its inputs are then priced
//     recursively, each delivered into THIS fab's import bid for that input good.
//     The harvest ask is real P&L but is NOT feed spend — only raw BUY inputs are.
func (g *ChainMarginGuard) branchPL(
	ctx context.Context,
	node *goods.SupplyChainNode,
	deliverBid int,
	deliverVol int,
	systemSymbol string,
	playerID int,
	stages *[]string,
) (int, int, error) {
	// Raw feed input.
	if node.IsLeaf() && node.AcquisitionMethod == goods.AcquisitionBuy {
		src, err := g.marketLocator.FindExportMarket(ctx, node.Good, systemSymbol, playerID)
		if err != nil || src == nil {
			return 0, 0, fmt.Errorf("no priceable source for feed %s: %v", node.Good, err)
		}
		vol := min(src.TradeVolume, deliverVol)
		legPL := (deliverBid - src.Price) * vol
		feedSpend := src.Price * vol
		*stages = append(*stages, fmt.Sprintf("feed %s bid%d-ask%d=%d×%d", node.Good, deliverBid, src.Price, deliverBid-src.Price, vol))
		return legPL, feedSpend, nil
	}

	// Fabrication node: locate its fab (export market), price the harvest→deliver
	// leg, then recurse into inputs delivered into this fab's import bids.
	fab, err := g.marketLocator.FindExportMarket(ctx, node.Good, systemSymbol, playerID)
	if err != nil || fab == nil {
		return 0, 0, fmt.Errorf("no priceable fab (export market) for %s: %v", node.Good, err)
	}
	nodeVol := min(fab.TradeVolume, deliverVol)
	nodePL := (deliverBid - fab.Price) * nodeVol
	*stages = append(*stages, fmt.Sprintf("make %s bid%d-ask%d=%d×%d", node.Good, deliverBid, fab.Price, deliverBid-fab.Price, nodeVol))

	fabMarket, err := g.marketRepo.GetMarketData(ctx, fab.WaypointSymbol, playerID)
	if err != nil || fabMarket == nil {
		return 0, 0, fmt.Errorf("no market data for fab %s of %s: %v", fab.WaypointSymbol, node.Good, err)
	}

	totalPL := nodePL
	totalFeed := 0
	for _, child := range node.Children {
		fabImport := fabMarket.FindGood(child.Good)
		if fabImport == nil {
			return 0, 0, fmt.Errorf("fab %s does not import feed %s (unpriceable delivery leg)", fab.WaypointSymbol, child.Good)
		}
		childPL, childFeed, cerr := g.branchPL(ctx, child, fabImport.PurchasePrice(), fabImport.TradeVolume(), systemSymbol, playerID, stages)
		if cerr != nil {
			return 0, 0, cerr
		}
		totalPL += childPL
		totalFeed += childFeed
	}
	return totalPL, totalFeed, nil
}

func failClosed(proj ChainProjection, detail string) ChainProjection {
	proj.Proceed = false
	proj.Reason = chainGuardUnpriceable
	proj.Detail = detail
	return proj
}

// ParkMessage renders the human/greppable park reason with every number in the
// text (the container-log renderer drops metadata, sp-iqyq).
func (p ChainProjection) ParkMessage() string {
	switch p.Reason {
	case chainGuardNegativeMargin:
		return fmt.Sprintf(
			"PARKED factory %s pre-spend: projected chain P&L %d ≤ required %d (feed spend %d) — feed import bids crushed/negative margin, refusing to spend. stages[%s]",
			p.Good, p.ProjectedPL, p.RequiredPL, p.FeedSpend, strings.Join(p.Stages, "; "),
		)
	case chainGuardSinkAbsorption:
		return fmt.Sprintf(
			"PARKED factory %s pre-spend: feed spend %d exceeds sink absorption cap %d (sink bid %d × vol %d × %d tranches) — sink too small to recover feed, refusing to spend. stages[%s]",
			p.Good, p.FeedSpend, p.AbsorptionCap, p.SinkBid, p.SinkVolume, sinkAbsorptionTranches, strings.Join(p.Stages, "; "),
		)
	case chainGuardUnpriceable:
		return fmt.Sprintf(
			"PARKED factory %s pre-spend: chain not priceable (%s) — failing closed, refusing to spend.",
			p.Good, p.Detail,
		)
	default:
		return fmt.Sprintf("PARKED factory %s pre-spend: %s", p.Good, p.Reason)
	}
}

// ProceedMessage renders the cleared-projection line (numbers in text for parity).
func (p ChainProjection) ProceedMessage() string {
	return fmt.Sprintf(
		"Chain-margin guard cleared %s: projected P&L %d ≥ required %d, feed spend %d within absorption cap %d (sink bid %d × vol %d). stages[%s]",
		p.Good, p.ProjectedPL, p.RequiredPL, p.FeedSpend, p.AbsorptionCap, p.SinkBid, p.SinkVolume, strings.Join(p.Stages, "; "),
	)
}

// LogFields exposes the projection as structured metadata for consumers that
// keep it (mirrors the "factory_parked" action idiom used by the harvest guard).
func (p ChainProjection) LogFields(factoryID string) map[string]interface{} {
	action := "chain_guard_pass"
	if !p.Proceed {
		action = "factory_parked"
	}
	return map[string]interface{}{
		"action":         action,
		"reason":         string(p.Reason),
		"factory_id":     factoryID,
		"good":           p.Good,
		"projected_pl":   p.ProjectedPL,
		"required_pl":    p.RequiredPL,
		"feed_spend":     p.FeedSpend,
		"absorption_cap": p.AbsorptionCap,
		"sink_bid":       p.SinkBid,
		"sink_volume":    p.SinkVolume,
		"stages":         p.Stages,
	}
}
