package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
)

// This file holds the sp-pvw3 `frontier status` READ-ONLY query: one view of the frontier
// coordinator's live state — the effective discovery/scan split, the discovery frontier depth, the
// HONEST dark-market backlog (charted-with-marketplaces with no/stale price data, NOT the
// coverage-excluded internal queue), probe allocation, the last probe buy, and the current blockers.
// It reuses the coordinator's own ports (no new wiring) and mutates nothing.

// FrontierStatusView is the assembled one-view state the `frontier status` command renders.
type FrontierStatusView struct {
	DiscoveryShare int    // effective 0-100 percent charting virgin
	ScanShare      int    // 100 - DiscoveryShare
	SplitSummary   string // e.g. "60% discover / 40% scan" or "100% discover — backlog empty"

	VirginQueueDepth int // reachable, uncovered virgin frontier systems (discovery work available)

	DarkSystems      int // charted-with-marketplaces systems with no/stale market_data (the HONEST backlog)
	DarkMarketplaces int // total unscanned marketplace count across those systems

	ProbeFleet    int // total satellites owned
	ProbeCap      int // MaxProbeFleet
	ProbesIdle    int // idle undedicated/scout satellites the reconciler can relay
	PostsInFlight int // outstanding sweep-once posts

	LastBuyPrice      int // price of the most recent probe buy (0 if none recorded)
	LastBuyAgeSeconds int // seconds since the most recent probe buy (-1 if none)

	Blockers []string // read-only reasons the buy path would currently fail closed
}

// Status assembles the live frontier view (sp-pvw3). It is a READ — it lists posts, ranks the virgin
// frontier, censuses the honest dark-market backlog (the RAW broadened dark set, NOT coverage-excluded,
// so a system with an unmanned post or stale prices still counts), reads the fleet + purchase ledger,
// and derives the current blockers — without declaring or buying anything. The effective split and its
// degradation note come from the same frontierCapacitySplit the reconcile uses, so status and behavior
// never disagree.
func (h *RunFrontierExpansionCoordinatorHandler) Status(ctx context.Context, cmd *RunFrontierExpansionCoordinatorCommand) (*FrontierStatusView, error) {
	cfg := resolveConfig(cmd, h.liveConfigSnapshot(ctx, cmd))

	posts, err := h.postRepo.ListActive(ctx, cmd.PlayerID.Value())
	if err != nil {
		return nil, fmt.Errorf("failed to list scout posts: %w", err)
	}

	queue := h.buildExpansionQueue(ctx, cmd, cfg, posts)
	darkSystems, darkMarketplaces := h.darkBacklogCensus(ctx, cmd)

	idle, err := h.availableProbes(ctx, cmd)
	if err != nil {
		return nil, err
	}
	satCount, err := h.satelliteCount(ctx, cmd)
	if err != nil {
		return nil, err
	}
	postsInFlight := countSweepOncePosts(posts)

	discoveryHasWork := len(queue) > 0
	scanHasWork := darkSystems > 0
	discoveryBudget, scanBudget := frontierCapacitySplit(cfg.DiscoveryShare, cfg.MaxFrontierPostsInFlight, discoveryHasWork, scanHasWork)

	lastPrice, lastWhen, hadLast, err := h.lastProbePurchaseDetail(ctx, cmd)
	if err != nil {
		return nil, err
	}
	lastAge := -1
	if hadLast {
		lastAge = int(h.clock.Now().Sub(lastWhen).Seconds())
	}

	view := &FrontierStatusView{
		DiscoveryShare:    cfg.DiscoveryShare,
		ScanShare:         100 - cfg.DiscoveryShare,
		SplitSummary:      splitSummary(cfg.DiscoveryShare, discoveryBudget, scanBudget, discoveryHasWork, scanHasWork),
		VirginQueueDepth:  len(queue),
		DarkSystems:       darkSystems,
		DarkMarketplaces:  darkMarketplaces,
		ProbeFleet:        satCount,
		ProbeCap:          cfg.MaxProbeFleet,
		ProbesIdle:        len(idle),
		PostsInFlight:     postsInFlight,
		LastBuyPrice:      lastPrice,
		LastBuyAgeSeconds: lastAge,
		Blockers:          h.statusBlockers(cfg, satCount, postsInFlight, hadLast, lastWhen),
	}
	return view, nil
}

// darkBacklogCensus counts the HONEST dark-market backlog: the RAW broadened dark set (charted markets
// with no/stale price data) straight from the scanner, NOT run through the coverage exclusion the scan
// side applies. So a charted-with-marketplaces system that already carries an (unmanned) post — or one
// whose prices went stale — still shows in the count, which is exactly the sp-pvw3 coverage-gap point:
// the operator sees every dark market, not just the ones the internal queue would declare this cycle.
func (h *RunFrontierExpansionCoordinatorHandler) darkBacklogCensus(ctx context.Context, cmd *RunFrontierExpansionCoordinatorCommand) (systems, marketplaces int) {
	if h.darkScanner == nil {
		return 0, 0
	}
	candidates, err := h.darkScanner.ChartedUnscannedMarketSystems(ctx, cmd.PlayerID.Value())
	if err != nil {
		return 0, 0
	}
	for _, candidate := range candidates {
		systems++
		marketplaces += candidate.MarketCount
	}
	return systems, marketplaces
}

// splitSummary renders the effective discover/scan split with its graceful-degradation note, matching
// the reconcile's actual budgets: a balanced concurrent split reads as "X% discover / Y% scan"; a
// redirected side reads as "100% … — <why>"; a fully dry cycle reads as idle.
func splitSummary(share, discoveryBudget, scanBudget int, discoveryHasWork, scanHasWork bool) string {
	switch {
	case discoveryBudget > 0 && scanBudget > 0:
		return fmt.Sprintf("%d%% discover / %d%% scan", share, 100-share)
	case discoveryBudget > 0 && !scanHasWork:
		return "100% discover — dark-market backlog empty (scan share redirected)"
	case scanBudget > 0 && !discoveryHasWork:
		return "100% scan — no reachable virgin frontier (discovery share redirected)"
	case discoveryBudget > 0:
		return "100% discover"
	case scanBudget > 0:
		return "100% scan"
	default:
		return "idle — no reachable virgin frontier and no dark-market backlog"
	}
}

// statusBlockers derives the read-only reasons the probe-buy path would currently fail closed, from
// state already in hand — no extra I/O, no side effects. It mirrors the decideAndMaybeBuy gate stack
// (fleet cap, post-in-flight cap, treasury/purchaser wiring, purchase cooldown) so the operator sees
// WHY the frontier is not buying without having to read the logs.
func (h *RunFrontierExpansionCoordinatorHandler) statusBlockers(cfg frontierConfig, satCount, postsInFlight int, hadLast bool, lastWhen time.Time) []string {
	blockers := []string{}
	if satCount >= cfg.MaxProbeFleet {
		blockers = append(blockers, fmt.Sprintf("fleet cap reached (%d/%d satellites)", satCount, cfg.MaxProbeFleet))
	}
	if postsInFlight >= cfg.MaxFrontierPostsInFlight {
		blockers = append(blockers, fmt.Sprintf("post-in-flight cap reached (%d/%d)", postsInFlight, cfg.MaxFrontierPostsInFlight))
	}
	if h.treasury == nil {
		blockers = append(blockers, "no treasury reader wired — buys fail closed")
	}
	if h.purchaser == nil {
		blockers = append(blockers, "no probe purchaser wired — buys fail closed")
	}
	if hadLast {
		if elapsed := h.clock.Now().Sub(lastWhen); elapsed < cfg.PurchaseCooldown {
			blockers = append(blockers, fmt.Sprintf("purchase cooldown active (%s of %s elapsed)", elapsed.Round(time.Second), cfg.PurchaseCooldown))
		}
	}
	return blockers
}

// lastProbePurchaseDetail returns the price + timestamp of the most recent probe buy from the
// persisted ledger (the same source the cooldown gate reads), so status reflects real spend, not
// memory. ok is false when no probe purchase is on record. Amounts are stored negative (expenses),
// so the price is the negated amount.
func (h *RunFrontierExpansionCoordinatorHandler) lastProbePurchaseDetail(ctx context.Context, cmd *RunFrontierExpansionCoordinatorCommand) (price int, when time.Time, ok bool, err error) {
	transactionType := ledger.TransactionTypePurchaseShip
	txns, err := h.ledgerRepo.FindByPlayer(ctx, cmd.PlayerID, ledger.QueryOptions{
		TransactionType: &transactionType,
		OrderBy:         "timestamp DESC",
		Limit:           50,
	})
	if err != nil {
		return 0, time.Time{}, false, err
	}
	for _, transaction := range txns {
		if isProbePurchase(transaction) {
			return -transaction.Amount(), transaction.Timestamp(), true, nil
		}
	}
	return 0, time.Time{}, false, nil
}
