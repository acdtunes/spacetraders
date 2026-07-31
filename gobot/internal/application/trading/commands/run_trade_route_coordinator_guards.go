package commands

// run_trade_route_coordinator_guards.go — pre-buy money/stale guards: the working-capital spend floor and the stale-ask abort (sp-wads move-only split).

import (
	"context"
	"errors"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// TreasuryReader reports a player's credit balance for the money guards. The daemon
// satisfies it with the LEDGER-backed reader (sp-muq66), which serves the balance from the
// transaction ledger — no API call — and falls back to the coalesced live read only when
// the ledger is older than its freshness bound. `Get Agent` was 8.3% of the API ceiling and
// did not fall under request coalescing, because the reads are invalidation-driven rather
// than duplicated; the only way to remove them is to stop asking.
//
// An error means UNREADABLE, and every caller here fails closed on it (RULINGS #4). The
// reader never converts a failed read into a stale or zero success.
type TreasuryReader interface {
	Credits(ctx context.Context, playerID int) (int64, error)
}

// readTreasuryCredits is this package's ONE money-guard balance read: through the
// ledger-backed reader when the daemon has wired one, otherwise the direct live call the
// guards made before the reader existed. The direct path is the optional-port contract
// every nil-apiClient test relies on, not an arming switch — the daemon wires the reader
// unconditionally, with no config gate between.
//
// It is a free function rather than a method because FOUR guards on three different
// handlers need it — the circuit's working-capital floor and the tour's money reads
// (sp-muq66), plus the stocker's capital ceiling and the one-shot arb's spend floor
// (sp-45s6f). Each of those handlers holds its own optional reader beside its own API
// client; sharing the read keeps them from drifting into four subtly different answers to
// "how many credits do we have".
//
// Every failure is an ERROR — never a zero, never a retained value. Callers read that as
// "do not spend this episode" (RULINGS #4).
func readTreasuryCredits(ctx context.Context, treasury TreasuryReader, api domainPorts.APIClient, playerID int) (int64, error) {
	if treasury != nil {
		return treasury.Credits(ctx, playerID)
	}
	if api == nil {
		return 0, errors.New("no treasury source wired")
	}
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return 0, fmt.Errorf("player token unavailable: %w", err)
	}
	agentData, err := api.GetAgent(ctx, token)
	if err != nil {
		return 0, fmt.Errorf("live agent read failed: %w", err)
	}
	return int64(agentData.Credits), nil
}

// treasuryCredits reads the player's balance for the circuit's working-capital guards.
func (h *RunTradeRouteCoordinatorHandler) treasuryCredits(ctx context.Context, playerID int) (int64, error) {
	return readTreasuryCredits(ctx, h.treasury, h.apiClient, playerID)
}

// reserveHeadroom performs the single treasury read that BOTH working-capital
// money guards share (sp-agzj): the circuit's spendFloorBreached (abort-on-breach)
// and the tour's buy-time tranche shrink (executeBuy). It reports how many credits
// may be spent right now while keeping treasury at or above reserve, so the two
// call sites make the same read against the same fail-open/fail-closed contract
// rather than each rolling its own.
//
// Three outcomes, mirroring the original spendFloorBreached optional-port contract:
//   - available=false                 → no treasury source wired at all (guard disabled,
//     the contract most tests rely on); the caller proceeds UNCONSTRAINED.
//   - available=true, readable=false  → a source IS wired but the read FAILED
//     (unresolvable token, an erroring GetAgent, or a ledger too stale to trust with no
//     live fallback available); the caller MUST fail CLOSED — never spend on a balance it
//     could not read (RULINGS #4). The read-failure cause is logged here; the caller logs
//     the action it took.
//   - available=true, readable=true   → headroom = balance - effectiveFloor (may be
//     <= 0), and balance is the treasury the decision was made against.
//
// The floor subtracted is the ABSOLUTE reserve (sp-05glh: the prior sp-yqx4 counter-cyclical
// proportional-of-treasury shrink was scrapped — every caller now enforces the flat
// configured/default reserve, unconditionally, whatever treasury is).
func (h *RunTradeRouteCoordinatorHandler) reserveHeadroom(ctx context.Context, playerID, reserve int) (headroom, liveBalance int, available, readable bool) {
	logger := common.LoggerFromContext(ctx)
	if h.apiClient == nil && h.treasury == nil {
		return 0, 0, false, false
	}

	credits, err := h.treasuryCredits(ctx, playerID)
	if err != nil {
		logger.Log("WARNING", "Could not read treasury for working-capital spend-floor check (fail-closed)", map[string]interface{}{
			"error": err.Error(),
		})
		return 0, 0, true, false
	}

	balance := int(credits)
	return balance - reserve, balance, true, true
}

// spendFloorBreached checks whether spending projectedCost right now would
// drop treasury below reserve (sp-bp6f), setting the abort fields on response
// and returning true if so — the caller must not proceed with the buy. It is the
// circuit's abort-on-breach reading of the shared reserveHeadroom seam (sp-agzj).
//
// Fails OPEN when NO treasury source is wired at all (neither the ledger-backed
// reader nor an apiClient): the guard is simply unavailable, the same optional-port
// contract staleAskAborts already uses for marketRefresher, so callers that cannot
// supply one (most tests) run without it rather than being forced to fake one.
//
// Fails CLOSED on every treasury-read failure (an unresolvable player token, an
// erroring GetAgent, or a ledger too stale to trust with the live fallback also
// down): unlike staleAskAborts' infrastructure-gap
// tolerance, a guard whose entire job is stopping the treasury from going
// negative must never let a buy through just because it went blind. An API
// hiccup here aborts the circuit instead of silently trading past the floor.
func (h *RunTradeRouteCoordinatorHandler) spendFloorBreached(
	ctx context.Context,
	playerID int,
	projectedCost int,
	reserve int,
	response *RunTradeRouteCoordinatorResponse,
) bool {
	logger := common.LoggerFromContext(ctx)
	headroom, liveBalance, available, readable := h.reserveHeadroom(ctx, playerID, reserve)
	if !available {
		return false // no treasury source wired — guard unavailable, fail OPEN
	}
	if !readable {
		// reserveHeadroom already logged the read-failure cause; record the
		// circuit-level decision (a guard that went blind never trades past the floor).
		logger.Log("WARNING", "Working-capital spend-floor check went blind - aborting circuit (fail-closed)", nil)
		response.SpendFloorAbort = true
		response.ReserveFloor = reserve
		return true
	}

	if headroom < projectedCost {
		logger.Log("WARNING", "Buy would breach the working-capital reserve floor - aborting circuit before spending", map[string]interface{}{
			"treasury": liveBalance, "projected_cost": projectedCost, "reserve": reserve,
		})
		response.SpendFloorAbort = true
		response.TreasuryAtAbort = liveBalance
		response.ReserveFloor = reserve
		return true
	}

	return false
}

// staleAskAborts live-verifies the source ask before the first buy and reports
// whether the circuit must abort because the ask has moved beyond
// trading.StaleAskMovePercent from the basis the lane was ranked on (hazard b). The
// lane is ranked from a cache that can be many minutes stale; executing on a moved
// basis has realised large losses. It refreshes the source market from the API (the
// hull is docked there, so the API returns live prices), re-reads the ask, and
// compares it to lane.SourceAsk.
//
// Fail-open on infrastructure gaps, fail-closed only on a CONFIRMED move: with no
// refresher wired, or when the refresh/read itself fails, it proceeds on the ranked
// basis (a transient scan hiccup must not strand an otherwise-good circuit). Only a
// live ask that is actually present AND beyond tolerance aborts the run.
func (h *RunTradeRouteCoordinatorHandler) staleAskAborts(
	ctx context.Context,
	lane trading.ArbitrageLane,
	playerID int,
	response *RunTradeRouteCoordinatorResponse,
) bool {
	logger := common.LoggerFromContext(ctx)
	if h.marketRefresher == nil {
		return false
	}

	// LIVE, not budgeted: the whole point of this guard is that the RANKED basis
	// came from a cache that can be many minutes stale, and executing on a moved
	// basis has realised large losses. A budgeted cache read here would verify the
	// basis against itself (sp-ntgfj).
	if err := h.marketRefresher.ScanAndSaveMarket(shared.WithLiveScanRequired(ctx), uint(playerID), lane.SourceWaypoint); err != nil {
		logger.Log("WARNING", "Could not refresh source market to live-verify basis - proceeding on ranked basis", map[string]interface{}{
			"waypoint": lane.SourceWaypoint, "good": lane.Good, "error": err.Error(),
		})
		return false
	}

	liveSrc, err := h.observeGood(ctx, lane.SourceWaypoint, lane.Good, playerID)
	if err != nil {
		logger.Log("WARNING", "Could not read live source ask after refresh - proceeding on ranked basis", map[string]interface{}{
			"waypoint": lane.SourceWaypoint, "good": lane.Good, "error": err.Error(),
		})
		return false
	}

	liveAsk := liveSrc.PurchasePrice() // the ASK is purchase_price — what we pay (sp-en5h7)
	if trading.AskMovedBeyondTolerance(liveAsk, lane.SourceAsk) {
		response.StaleAskAbort = true
		response.RankedSourceAsk = lane.SourceAsk
		response.LiveSourceAsk = liveAsk
		logger.Log("WARNING", "Source ask moved beyond tolerance since the lane was ranked - aborting circuit before first buy", map[string]interface{}{
			"good": lane.Good, "source": lane.SourceWaypoint,
			"ranked_ask": lane.SourceAsk, "live_ask": liveAsk, "tolerance_pct": trading.StaleAskMovePercent,
		})
		return true
	}

	logger.Log("INFO", "Live-verified source ask within tolerance - proceeding with the circuit", map[string]interface{}{
		"good": lane.Good, "source": lane.SourceWaypoint, "ranked_ask": lane.SourceAsk, "live_ask": liveAsk,
	})
	return false
}
