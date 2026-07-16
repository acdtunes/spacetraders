package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipapp "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipQueries "github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// --- cross-system travel (sp-wlev: the multi-system gate-crossing unlock) ---

const (
	// DefaultCooldownMarginFactor mirrors ship.DefaultArrivalMarginFactor
	// (sp-ht1f) exactly: the multiplicative term scales naturally with
	// however long the wait is, so the same ratio is appropriate whether
	// the thing being waited on is an arrival or a jump cooldown.
	DefaultCooldownMarginFactor = 1.25

	// DefaultCooldownMinMargin is sized for jump cooldowns specifically -
	// NOT copied from arrival's 2-minute floor. Arrival's 2-minute margin
	// is a small fraction of transits that are themselves minutes-to-hours
	// long; reusing it here would make the margin the DOMINANT term for a
	// ~60s jump cooldown (a 3x inflation) instead of the small clock-skew/
	// API-latency correction it's meant to be. 10s comfortably absorbs
	// that jitter without ballooning the wait on the much shorter cooldown
	// timescale.
	DefaultCooldownMinMargin = 10 * time.Second
)

// calculateCooldownWaitBudget mirrors calculateArrivalWaitBudget's formula
// (internal/application/ship/arrival_wait.go, sp-ht1f pattern):
// budget = max(remaining*marginFactor, remaining+minMargin). The margin
// absorbs scheduler jitter and API latency around the real cooldown-expiry
// instant; it is not what keeps a short cooldown from being polled early. A
// negative remaining (clock skew already putting "now" past the reported
// expiry) is clamped to zero before either term is computed.
func calculateCooldownWaitBudget(remaining time.Duration, marginFactor float64, minMargin time.Duration) time.Duration {
	if remaining < 0 {
		remaining = 0
	}
	scaled := time.Duration(float64(remaining) * marginFactor)
	floor := remaining + minMargin
	if scaled > floor {
		return scaled
	}
	return floor
}

// waitForJumpCooldown waits out the cooldown the jump API just reported,
// using an ETA-scaled budget (sp-ht1f pattern) instead of a flat buffer
// (contrast run_siphon_worker.go's waitForShipCooldown, which adds a flat
// 1s - that resumes a cooldown persisted from a PREVIOUS session and has no
// fresher number to scale from; here the jump response just told us the
// exact cooldown synchronously, so the ETA-scaled budget applies cleanly).
//
// sp-wc5h: the wait is now ctx-interruptible (see sleepInterruptibly). A bare
// h.clock.Sleep(budget) here was the tour-death exit path — a daemon shutdown
// while a tour was settling out a jump cooldown blocked the whole graceful
// window on a ~440s sleep the STOPPING flag could not reach (the wait sits
// mid-iteration, not between iterations), then force-killed the goroutine
// mid-sleep. Racing the sleep against ctx.Done() lets the iteration take
// execute()'s ctx-cancel path instead — exiting RUNNING/resumable so the tour
// is re-adopted at the next boot rather than stranded. Returns ctx.Err() on
// cancellation so travel() aborts the circuit immediately.
func (h *RunTradeRouteCoordinatorHandler) waitForJumpCooldown(ctx context.Context, cooldownSeconds int) error {
	if cooldownSeconds <= 0 {
		return nil
	}
	remaining := time.Duration(cooldownSeconds) * time.Second
	budget := calculateCooldownWaitBudget(remaining, DefaultCooldownMarginFactor, DefaultCooldownMinMargin)

	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", "Waiting out jump cooldown before continuing the circuit", map[string]interface{}{
		"action":              "jump_cooldown_wait",
		"cooldown_seconds":    cooldownSeconds,
		"wait_budget_seconds": int(budget.Seconds()),
	})
	return h.sleepInterruptibly(ctx, budget)
}

// sleepInterruptibly blocks for d via the handler's injected clock (instant
// under the test MockClock, a real sleep in production) but races it against
// ctx.Done(), so a Stop/shutdown never has to wait the full cooldown budget
// out. The same shape as the sp-l709 factory park wait and the container
// runner's sleepOrCancel: the detached sleeper goroutine outlives an early
// return by at most one budget before exiting, so it cannot leak. Returns
// ctx.Err() when the context is cancelled first, nil when the sleep completed.
func (h *RunTradeRouteCoordinatorHandler) sleepInterruptibly(ctx context.Context, d time.Duration) error {
	done := make(chan struct{})
	go func() {
		h.clock.Sleep(d)
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// jumpHop dispatches ONE jump hop, riding out an active jump cooldown instead
// of crashing on it (sp-wc5h). A tour re-adopted mid-circuit after a daemon
// restart (RULING #2) re-attempts the jump while the hull is STILL cooling down
// from the hop it made just before the restart — the ship's cooldown clock is
// persisted (jump_ship.go SetCooldown), so the live jump API rejects the
// re-jump with 409 code-4000 "Ship action is still on cooldown for N
// second(s)". The pre-sp-wc5h path surfaced that 409 as a hard iteration error,
// and the container runner's escalating restart budget (5s+30s+120s ≈ 155s)
// cannot outlast a jump cooldown of 226–775s, so the tour crashed FAILED and
// the hull stranded idle (the tour-death incident: TORWIND-2B-a2856bfc crashed
// on a 325s-remaining cooldown; TORWIND-2C-52422c31 burned its whole restart
// budget riding out an 88s one). Riding it — parse the remaining cooldown from
// the 409, wait it out ctx-interruptibly, retry — resumes the circuit the
// moment the cooldown clears, exactly as the gas siphon worker already does
// (parseCooldownFromError). Bounded by maxCooldownRides so a jump that keeps
// failing for any OTHER reason still surfaces the error rather than looping;
// a non-cooldown error propagates on the first attempt, so a genuine jump
// failure is never masked as a cooldown.
func (h *RunTradeRouteCoordinatorHandler) jumpHop(ctx context.Context, cmd *navCmd.JumpShipCommand) (*navCmd.JumpShipResponse, error) {
	const maxCooldownRides = 3
	for ride := 0; ; ride++ {
		resp, err := h.mediator.Send(ctx, cmd)
		if err != nil {
			cooldown := parseJumpCooldownRemaining(err)
			if cooldown <= 0 || ride >= maxCooldownRides {
				return nil, err
			}
			logger := common.LoggerFromContext(ctx)
			logger.Log("INFO", "Jump still on cooldown from a pre-restart hop — riding it out before retrying (resume-safe)", map[string]interface{}{
				"action":             "jump_cooldown_ride",
				"ship_symbol":        cmd.ShipSymbol,
				"destination_system": cmd.DestinationSystem,
				"cooldown_seconds":   int(cooldown.Seconds()),
				"ride":               ride + 1,
			})
			// Wait the reported remaining plus the same jitter margin the
			// post-jump budget uses, so a rounded-down remainingSeconds or minor
			// clock skew doesn't drop the retry back inside the cooldown.
			if werr := h.sleepInterruptibly(ctx, cooldown+DefaultCooldownMinMargin); werr != nil {
				return nil, werr
			}
			continue
		}
		jumpResp, ok := resp.(*navCmd.JumpShipResponse)
		if !ok {
			return nil, fmt.Errorf("unexpected jump response type %T", resp)
		}
		return jumpResp, nil
	}
}

// parseJumpCooldownRemaining extracts the remaining cooldown from a jump 409
// (SpaceTraders error code 4000). Returns 0 for any non-cooldown error, so
// callers ride ONLY a genuine cooldown and propagate everything else. Mirrors
// the gas package's parseCooldownFromError (run_siphon_worker.go) — deliberately
// duplicated rather than shared to keep this fix inside the travel/runner files;
// the wire format it parses is the stable SpaceTraders cooldown error:
//
//	API error (status 409): {"error":{"code":4000,...,"data":{"cooldown":{"remainingSeconds":88,...}}}}
func parseJumpCooldownRemaining(err error) time.Duration {
	if err == nil {
		return 0
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "cooldown") {
		return 0
	}
	jsonStart := strings.Index(errStr, "{")
	if jsonStart == -1 {
		return 0
	}
	var apiErr struct {
		Error struct {
			Code int `json:"code"`
			Data struct {
				Cooldown struct {
					RemainingSeconds int `json:"remainingSeconds"`
				} `json:"cooldown"`
			} `json:"data"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(errStr[jsonStart:]), &apiErr) != nil {
		return 0
	}
	if apiErr.Error.Code != 4000 || apiErr.Error.Data.Cooldown.RemainingSeconds <= 0 {
		return 0
	}
	return time.Duration(apiErr.Error.Data.Cooldown.RemainingSeconds) * time.Second
}

// travel moves the ship toward destinationWaypoint, crossing a system
// boundary via jump (sp-n0x7's ship-jump verb) when needed - the sp-wlev
// multi-system trade-route unlock. A same-system destination takes the
// existing navigate/dock fast path unchanged, returning the SAME ship
// pointer untouched. A cross-system destination jumps instead: the jump
// opts out of taking its own claim (SkipClaim: true) since the coordinator
// already holds the ship claimed under its own container for the whole
// circuit, waits out the resulting cooldown, then reloads the ship from the
// repository so the caller continues with a pointer reflecting the ship's
// new system - every downstream verb (dock/purchase/sell) already
// dispatches by ship SYMBOL, never the cached pointer, so this reload is
// the only place staleness could otherwise leak in.
func (h *RunTradeRouteCoordinatorHandler) travel(
	ctx context.Context,
	ship *navigation.Ship,
	destinationWaypoint string,
	playerID int,
) (*navigation.Ship, error) {
	// Strict reach: heavies/trade/arb resolve the jump path through the fetch-through
	// gategraph.Path (MaxJumpPath, fail-closed on unreadable gates). repositionJumpBound 0
	// selects it — every existing caller's behavior is byte-for-byte unchanged (sp-8k9m).
	return h.travelWithJumpBound(ctx, ship, destinationWaypoint, playerID, 0)
}

// travelWithJumpBound is travel() with an explicit reposition jump bound (sp-8k9m). A
// bound of 0 uses the strict fetch-through Path (heavies/trade/arb); a positive bound
// routes the cross-system leg over the PERSISTED stored adjacency via RepositionPath —
// the expendable probe/scout reposition class only, reached through
// RepositionToWaypointWithinJumps. Everything ELSE about the flight (in-transit wait,
// source/arrival gate hops, per-hop cooldowns) is identical; only WHICH resolver picks the
// system sequence changes.
func (h *RunTradeRouteCoordinatorHandler) travelWithJumpBound(
	ctx context.Context,
	ship *navigation.Ship,
	destinationWaypoint string,
	playerID int,
	repositionJumpBound int,
) (*navigation.Ship, error) {
	// sp-8l3o — before ANY movement, ride out a hull that is still IN_TRANSIT. A
	// run re-adopted mid-hop (the arb resume path: a hull mid in-system hop toward
	// the source jump gate) is NOT idle — attempting the jump/navigate now returns
	// API 4214 'Ship is currently in-transit' and the resulting iteration error
	// burns the container's whole restart budget just riding out a routine arrival.
	// An in-transit ship is a WAIT state, not an error: wait out the ETA-aligned
	// arrival via the SAME evented mechanism the RouteExecutor's own idempotency
	// path uses (WaitForShipArrival, sp-7yej), then continue from the freshly
	// arrived state. No subscriber wired → the wait is skipped (fail-open) so the
	// docked-then-travel circuit path is byte-for-byte unchanged.
	arrived, err := h.waitForInTransitArrival(ctx, ship, playerID)
	if err != nil {
		return ship, err
	}
	ship = arrived

	currentSystem := ship.CurrentLocation().SystemSymbol
	destSystem := shared.ExtractSystemSymbol(destinationWaypoint)

	if currentSystem == destSystem {
		if err := h.navigate(ctx, ship, destinationWaypoint, playerID); err != nil {
			return ship, err
		}
		return ship, nil
	}

	// Resolve the ordered jump path over the gate graph (sp-7gr2). travel() used
	// to assume origin→dest was a SINGLE edge and honestly crashed a laden frigate
	// at the home gate when it wasn't — JP61 is THREE jumps from KA42
	// (PA3→UQ16→JP61), and the direct jump 4262'd four times. With a gate graph
	// wired, BFS returns every intermediate system to hop through; an unroutable
	// dest returns the wrapped error (naming both systems) BEFORE any flying.
	// Without one, jumpPath falls back to the legacy single directly-connected
	// jump so existing callers/tests are byte-for-byte unchanged.
	path, err := h.jumpPath(ctx, currentSystem, destSystem, playerID, repositionJumpBound)
	if err != nil {
		return ship, err
	}

	// sp-5nqx departure hop — the SOURCE-side mirror of the sp-vzxu gate->waypoint
	// arrival hop below. The jump verb requires a DRIVELESS hull (which the arb/
	// trade haulers are) to already be sitting ON a jump gate: jump_ship.go rejects
	// "no jump drive module and not at a jump gate" for a driveless hull UP FRONT,
	// before its own find-nearest-gate hop can run (that hop only rescues drive-
	// equipped hulls). So a cross-system leg that starts at a market waypoint (e.g.
	// K79) must fly the waypoint->gate hop HERE first, or the jump fails and the
	// bought tranche strands at the source (the live sp-5nqx incident). GUARDED: a
	// hull already sitting on a jump gate skips the hop entirely, so a gate-origin
	// lane still costs exactly one jump and zero extra navigates.
	if !ship.CurrentLocation().IsJumpGate() {
		gateResp, gerr := h.mediator.Send(ctx, &shipQueries.FindNearestJumpGateQuery{
			ShipSymbol: ship.ShipSymbol(),
			PlayerID:   &playerID,
		})
		if gerr != nil {
			return ship, fmt.Errorf("find source jump gate for %s in %s failed: %w", ship.ShipSymbol(), currentSystem, gerr)
		}
		gate, ok := gateResp.(*shipQueries.FindNearestJumpGateResponse)
		if !ok || gate.JumpGate == nil {
			return ship, fmt.Errorf("no source jump gate resolved for %s in %s (response %T)", ship.ShipSymbol(), currentSystem, gateResp)
		}
		if err := h.navigate(ctx, ship, gate.JumpGate.Symbol, playerID); err != nil {
			return ship, fmt.Errorf("navigate %s from %s to source jump gate %s failed: %w", ship.ShipSymbol(), ship.CurrentLocation().Symbol, gate.JumpGate.Symbol, err)
		}

		// sp-trnp: DO NOT trust the navigate's completion. Its arrival wait can return on a
		// STALE "left transit" resync (arrival_wait.go's pre-ETA safety poll reads a
		// not-yet-propagated pre-departure nav_status — the sp-n7yp/sp-ynuf nav-cache race)
		// BEFORE the hull actually reaches the gate, leaving a DRIVELESS hull off-gate. The
		// hop loop's jump then hits jump_ship.go's hard "not at a jump gate" reject — which,
		// unlike the drive-equipped path, does NOT auto-navigate — and the tour crashes
		// UNRECOVERABLY (the live incident: TORWIND-37's leg-1 departure hop toward gate
		// X1-DP51-B26F "completed" via a 30s-in false-positive while still ~2m in transit,
		// and the jump crashed). Re-confirm on the AUTHORITATIVE live position: resync from
		// the API (defeating the stale cache, exactly as dock does for the mirror race,
		// sp-ynuf), ride out any genuine remaining transit, and only fall through to the jump
		// once the hull is truly ON the gate. Otherwise fail with a resume-safe error so the
		// container re-adopts and rides out the transit (travel()'s own top-of-function
		// waitForInTransitArrival) instead of firing a doomed jump.
		resynced, serr := h.shipRepo.SyncShipFromAPI(ctx, ship.ShipSymbol(), shared.MustNewPlayerID(playerID))
		if serr != nil {
			return ship, fmt.Errorf("resync %s onto source jump gate %s after departure hop failed: %w", ship.ShipSymbol(), gate.JumpGate.Symbol, serr)
		}
		arrived, aerr := h.waitForInTransitArrival(ctx, resynced, playerID)
		if aerr != nil {
			return ship, aerr
		}
		if !arrived.CurrentLocation().IsJumpGate() {
			return ship, fmt.Errorf("departure hop for %s reported navigate-complete but the hull is at %s, not source jump gate %s (nav-cache race, sp-trnp) — resume-safe, retrying", ship.ShipSymbol(), arrived.CurrentLocation().Symbol, gate.JumpGate.Symbol)
		}
		ship = arrived
	}

	// Execute the path hop-by-hop. Each hop is ONE directly-connected jump: the
	// jump verb resolves the next gate from the ORIGIN gate's live connections and
	// lands the hull ON the next system's gate, already positioned for the
	// following jump — so intermediate hops need no waypoint→gate navigate, only
	// the terminal arrival hop below. A cooldown wait follows EVERY jump (the old
	// single-jump path waited too): the wait is precisely what lets the NEXT jump
	// proceed, and after the final jump it is a harmless bounded settle before the
	// arrival hop. SkipClaim: the coordinator already holds this hull claimed for
	// the whole circuit (sp-wlev). path[0] is the current system; jump to each
	// subsequent hop. The jump dispatches by SYMBOL and re-reads the hull's
	// freshly-persisted location each time, so no per-hop reload of `ship` is
	// needed — only the single post-path reload below, for the arrival hop.
	totalHops := len(path) - 1
	for i := 1; i < len(path); i++ {
		nextSystem := path[i]
		jumpResp, jerr := h.jumpHop(ctx, &navCmd.JumpShipCommand{
			ShipSymbol:        ship.ShipSymbol(),
			DestinationSystem: nextSystem,
			PlayerID:          &playerID,
			SkipClaim:         true,
		})
		if jerr != nil {
			return ship, fmt.Errorf("jump %s to %s (hop %d of %d toward %s) failed: %w", ship.ShipSymbol(), nextSystem, i, totalHops, destSystem, jerr)
		}
		if werr := h.waitForJumpCooldown(ctx, jumpResp.CooldownSeconds); werr != nil {
			return ship, werr
		}
	}

	freshShip, err := h.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), shared.MustNewPlayerID(playerID))
	if err != nil {
		return ship, fmt.Errorf("failed to reload ship %s after jump to %s: %w", ship.ShipSymbol(), destSystem, err)
	}

	// The jump lands the hull on destSystem's JUMP GATE, not on
	// destinationWaypoint's market. Fly the final gate->waypoint hop so the
	// caller's dock+sell fire at the market that actually trades the good -
	// without it the sell fired at the gate (which doesn't trade the good) and
	// stranded the whole load (sp-vzxu: observed -510k, 54-72 unsold
	// lab_instruments). GUARDED: when the jump already landed the hull ON
	// destinationWaypoint (the destination IS the gate), the hop is redundant
	// and skipped, so a gate-market lane still costs exactly one jump.
	if freshShip.CurrentLocation().Symbol != destinationWaypoint {
		if err := h.navigate(ctx, freshShip, destinationWaypoint, playerID); err != nil {
			return freshShip, fmt.Errorf("navigate %s from gate to %s after jump to %s failed: %w", freshShip.ShipSymbol(), destinationWaypoint, destSystem, err)
		}
	}
	return freshShip, nil
}

// RepositionToWaypoint moves shipSymbol to destinationWaypoint, crossing system
// boundaries via the coordinator's own multi-jump travel() primitive (sp-7gr2 gate
// BFS + per-hop cooldown waits + the source/arrival gate hops). It is the exported
// seam the scout-post coordinator's reposition worker (sp-s232) rides to jump-route
// an idle satellite to an unmanned frontier post WITHOUT duplicating jump logic
// (RULINGS: reuse the shared travel machinery, do not re-implement it).
//
// The caller (the reposition container) already holds shipSymbol claimed — travel()'s
// jumps set SkipClaim, trusting that claim, exactly as the trade/arb circuits do. Any
// travel error is returned verbatim so the worker fails HONESTLY: the runner then
// releases the claim and the coordinator re-parks the post for a bounded retry rather
// than a silent strand. It is a pure movement primitive: it never buys, sells, or
// touches the posts table — the coordinator owns all reposition bookkeeping.
func (h *RunTradeRouteCoordinatorHandler) RepositionToWaypoint(ctx context.Context, shipSymbol, destinationWaypoint string, playerID int) error {
	ship, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, shared.MustNewPlayerID(playerID))
	if err != nil {
		return fmt.Errorf("failed to load ship %s for reposition to %s: %w", shipSymbol, destinationWaypoint, err)
	}
	if _, err := h.travel(ctx, ship, destinationWaypoint, playerID); err != nil {
		return fmt.Errorf("reposition of %s to %s failed: %w", shipSymbol, destinationWaypoint, err)
	}
	return nil
}

// RepositionToWaypointWithinJumps is the EXPENDABLE probe/scout variant of
// RepositionToWaypoint (sp-8k9m): identical, except the cross-system jump path is resolved
// over the PERSISTED stored adjacency bounded to maxJumps (RepositionPath) rather than the
// strict fetch-through Path. This is the ONE call site that relaxes the sp-qxa4 fail-closed
// unreadable-gate discipline — and only for a scout satellite, whose whole purpose is to
// reach an unreadable frontier and whose arrival re-reads the gate it crossed (the
// relaxation retires itself). Heavies/trade/arb keep RepositionToWaypoint (strict). maxJumps
// <= 0 degrades to the strict resolver, so a mis-wired caller can never accidentally relax.
func (h *RunTradeRouteCoordinatorHandler) RepositionToWaypointWithinJumps(ctx context.Context, shipSymbol, destinationWaypoint string, playerID, maxJumps int) error {
	ship, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, shared.MustNewPlayerID(playerID))
	if err != nil {
		return fmt.Errorf("failed to load ship %s for reposition to %s: %w", shipSymbol, destinationWaypoint, err)
	}
	if _, err := h.travelWithJumpBound(ctx, ship, destinationWaypoint, playerID, maxJumps); err != nil {
		return fmt.Errorf("reposition of %s to %s failed: %w", shipSymbol, destinationWaypoint, err)
	}
	return nil
}

// waitForInTransitArrival rides out a hull that is still IN_TRANSIT before any
// movement leg (sp-8l3o). It is the pre-movement mirror of the RouteExecutor's own
// waitForCurrentTransit idempotency wait: the arb resume path re-adopts a hull mid
// in-system hop (e.g. mid-flight toward the source jump gate), and attempting the
// jump/navigate now returns API 4214 'in-transit' — a routine arrival dressed up as
// an error that burns the container's restart budget. An in-transit ship is a WAIT
// state: this waits out the ETA-aligned arrival via WaitForShipArrival (the shared
// evented wait the whole codebase uses, sp-7yej/sp-pafv — event-driven, with a
// resync/park backstop for a lost event), then reloads so the freshly-persisted
// location (the hull now sitting on the gate/destination) drives the IsJumpGate
// check and the rest of travel().
//
// A non-in-transit ship returns immediately (the common circuit case: the hull just
// docked and bought, so this is a no-op). A nil eventSubscriber (no daemon wiring —
// most tests, any non-circuit caller) also returns immediately, so behavior is
// byte-for-byte unchanged where the lever is not wired. A wait that exhausts its
// budget (a genuinely lost arrival event) surfaces the error so the iteration retries
// rather than jumping blind — the same honest-wait discipline the RouteExecutor keeps.
func (h *RunTradeRouteCoordinatorHandler) waitForInTransitArrival(
	ctx context.Context,
	ship *navigation.Ship,
	playerID int,
) (*navigation.Ship, error) {
	if ship.NavStatus() != navigation.NavStatusInTransit {
		return ship, nil
	}
	if h.eventSubscriber == nil {
		return ship, nil
	}

	logger := common.LoggerFromContext(ctx)
	pid := shared.MustNewPlayerID(playerID)

	// Mirror the RouteExecutor's waitForCurrentTransit ETA seed: the ship's own
	// persisted ArrivalTime, if still in the future, sizes the wait budget (a nil or
	// already-past ETA leaves it 0, which WaitForShipArrival floors with its own
	// min-margin — the lost-event resync backstop still bounds it).
	var waitTimeSeconds int
	if arrival := ship.ArrivalTime(); arrival != nil {
		if remaining := time.Until(*arrival); remaining > 0 {
			waitTimeSeconds = int(remaining.Seconds())
		}
	}

	logger.Log("INFO", fmt.Sprintf(
		"Hull %s re-adopted mid-transit — waiting out arrival before movement (resume-safe: an in-transit ship is a wait, not a 4214 error to retry)",
		ship.ShipSymbol(),
	), map[string]interface{}{
		"action": "arb_resume_transit_wait", "ship_symbol": ship.ShipSymbol(),
		"expected_seconds": waitTimeSeconds, "destination_hint": ship.CurrentLocation().Symbol,
	})

	if werr := shipapp.WaitForShipArrival(ctx, h.shipRepo, h.eventSubscriber, ship, pid, waitTimeSeconds, logger); werr != nil {
		return ship, fmt.Errorf("waiting out in-transit arrival for %s before movement failed: %w", ship.ShipSymbol(), werr)
	}

	// Reload so downstream (the IsJumpGate departure-hop check, and the same-system
	// navigate) routes from the hull's freshly-persisted arrived location, not the
	// stale pre-arrival pointer — the same reload-after-state-change discipline the
	// post-jump path already uses.
	fresh, ferr := h.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), pid)
	if ferr != nil {
		return ship, fmt.Errorf("reloading %s after in-transit arrival failed: %w", ship.ShipSymbol(), ferr)
	}
	return fresh, nil
}

// jumpPath resolves the ordered system hop path from fromSystem to destSystem
// inclusive (the caller has already established they differ). With a gate graph
// wired (sp-7gr2) it BFS-walks the persisted adjacency — the fix for the
// single-edge assumption. Without one it returns the legacy [from, dest]: assume
// dest is one directly-connected jump away, preserving every existing
// caller/test that never wires a graph. A gate-graph error (unroutable, or a
// store/fetch failure) propagates so travel() aborts rather than fly blind.
// repositionJumpBound > 0 (the expendable probe/scout class, sp-8k9m) resolves the path
// over the PERSISTED stored adjacency via RepositionPath — routing PAST unreadable frontier
// gates over a larger bound — instead of the strict fetch-through Path. Bound 0 is every
// other caller (heavies/trade/arb): byte-for-byte the pre-8k9m behavior.
func (h *RunTradeRouteCoordinatorHandler) jumpPath(ctx context.Context, fromSystem, destSystem string, playerID, repositionJumpBound int) ([]string, error) {
	if h.gateGraph == nil {
		return []string{fromSystem, destSystem}, nil
	}
	if repositionJumpBound > 0 {
		return h.gateGraph.RepositionPath(ctx, fromSystem, destSystem, repositionJumpBound)
	}
	return h.gateGraph.Path(ctx, fromSystem, destSystem, playerID)
}

// --- circuit time model (sp-wlev; sp-xwa1; sp-1wp8 rate-ranking) ---

// The lane ranker scores each lane by RATE — hold-fit-weighted per-circuit value
// over estimated circuit wall-clock (rankLanesByCircuitRate) — so the time a
// cross-system circuit spends jumping and cooling down enters the ranking as a
// divisor, not as the retired sp-xwa1 per-unit spread haircut. Ranking-only, as
// ever: no lane's real economics (SpreadPerUnit, CappedSpread, ClearsFloor) are
// mutated. These are the model's parametrized inputs (RULINGS #5 - a named
// constant table, not magic numbers scattered at the call site). Retune here if
// the fleet's real hop time or baseline home-lane earn rate shifts.
const (
	// crossSystemHopSeconds is the observed wall-clock cost of ONE gate hop:
	// the jump execution plus the cooldown wait that must elapse before the
	// hull can act again (waitForJumpCooldown, this file). ~352s is the
	// production-observed per-hop figure (sp-wlev). It is the atomic time unit
	// the round-trip surcharge is built from - derived from the engine's
	// real cooldown behavior, not invented.
	crossSystemHopSeconds = 352.0

	// crossSystemRoundTripHops is how many gate hops a cross-system CIRCUIT pays
	// that an equivalent same-system circuit does not: one outbound (source
	// system -> destination system) and one inbound (back to re-buy), so a round
	// trip crosses the gate twice. The autonomous scan only ranks 1-jump
	// neighbors (scanLanes' neighborSystems is a single hop), so exactly one hop
	// per direction is the right unit here; a rare deeper multi-hop lane is
	// under-penalized, never over-penalized - the conservative direction.
	crossSystemRoundTripHops = 2.0

	// hullOpportunityCreditsPerSecond is what a hull sustainably earns per second
	// running a DECENT same-system home lane. Derived conservatively from the
	// ~250-300k/hr home-lane class the fleet sustains: 270000 credits / 3600s =
	// 75 credits/s. It anchors the circuit time model below (and the reposition
	// floor's cost rationale, run_tour_coordinator_reposition.go); if the fleet's
	// baseline home-lane rate climbs, this is the knob to raise.
	hullOpportunityCreditsPerSecond = 75.0

	// homeLaneClassCircuitValue is the per-circuit value of that same decent
	// home-lane class — the numerator whose sustained rate defines
	// hullOpportunityCreditsPerSecond. Together the two constants IMPLY the
	// in-system circuit's real wall-clock (value / rate, below), keeping the time
	// model and the opportunity-rate anchor mutually calibrated instead of
	// inventing an independent duration guess.
	homeLaneClassCircuitValue = 300000.0

	// circuitInSystemBaselineSeconds is the estimated wall-clock of ONE full
	// same-system circuit — buy leg, transit, tv-chunked sell transactions,
	// dock/refresh/refuel overheads, return transit:
	// homeLaneClassCircuitValue / hullOpportunityCreditsPerSecond = 4000s ≈ 67min.
	// Deliberately the fleet's OBSERVED circuit class, not a travel-only sum: a
	// naive flight-time-only figure (~720s) would overstate every lane's absolute
	// rate ~5x AND — because it makes the gate surcharge look like a ~2x time
	// premium instead of the ~18% it really is against a full circuit — silently
	// flip the captain-ruled DP51 frontier economics (a deep cross-system lane a
	// heavy hull fills must out-rank a saturated home lane; see
	// run_trade_route_coordinator_xsyspenalty_test.go).
	circuitInSystemBaselineSeconds = homeLaneClassCircuitValue / hullOpportunityCreditsPerSecond
)

// estimatedCircuitSeconds prices one full circuit of a lane in wall-clock seconds
// for RATE ranking (sp-1wp8): the in-system baseline plus, for a gate-crossing
// circuit, the observed round-trip jump+cooldown surcharge
// (crossSystemRoundTripHops × crossSystemHopSeconds ≈ 704s). Always > 0 (named
// positive constants), so a lane rate can never divide by zero — the structural
// form of the sp-1wp8 zero-time-estimate regression pin at this surface.
func estimatedCircuitSeconds(crossSystem bool) float64 {
	seconds := float64(circuitInSystemBaselineSeconds)
	if crossSystem {
		seconds += crossSystemRoundTripHops * crossSystemHopSeconds
	}
	return seconds
}

// maxListingAge bounds how old a cached market observation may be and still feed
// UNDIRECTED lane ranking (sp-xwa1). The ranker scores lanes off cached prices; a
// lane priced from an observation this stale can already have moved, so ranking it
// chases a spread that no longer exists (the analyst's arb-board finding: the
// ranker "picks lanes that already moved"). 75 minutes is deliberately generous —
// a frontier market a hull hasn't visited in over an hour is genuinely unreliable,
// while a lane re-observed within the hour (every completed trade refreshes its
// own two markets, see scanLanes' refreshMarketData note) stays eligible. It gates
// only undirected auto-scan: an operator-directed --dest lane is re-verified LIVE
// at execution (staleAskAborts + the per-visit margin re-check), so staleness must
// not silently veto it — see scanLanes.
const maxListingAge = 75 * time.Minute

// partitionListingsByAge splits listings into those observed within maxAge of now
// (fresh) and those older (stale), preserving input order in each. A listing with a
// zero ObservedAt is treated as FRESH — an unknown age is not evidence of staleness,
// and callers that never populate the timestamp (older tests, non-cache sources)
// must rank unchanged. Pure and now-injected so the age gate is unit-testable
// without a clock; scanLanes supplies h.clock.Now().
func partitionListingsByAge(listings []trading.GoodListing, now time.Time, maxAge time.Duration) (fresh, stale []trading.GoodListing) {
	for _, l := range listings {
		if !l.ObservedAt.IsZero() && now.Sub(l.ObservedAt) > maxAge {
			stale = append(stale, l)
			continue
		}
		fresh = append(fresh, l)
	}
	return fresh, stale
}

// staleListingSummary renders up to a few stale listings into a compact,
// message-text one-liner (waypoint:good) so the exclusion is greppable in
// `container logs`, which drops the structured metadata map (the sp-149h/sp-iqyq
// renderer defect). Bounded so a system-wide staleness event doesn't flood one log
// line with every excluded row.
func staleListingSummary(stale []trading.GoodListing) string {
	const sampleLimit = 5
	parts := make([]string, 0, sampleLimit)
	for i, l := range stale {
		if i >= sampleLimit {
			parts = append(parts, fmt.Sprintf("+%d more", len(stale)-sampleLimit))
			break
		}
		parts = append(parts, fmt.Sprintf("%s:%s", l.Waypoint, l.Good))
	}
	return strings.Join(parts, ", ")
}

// laneCircuitValue is the hold-fit-weighted per-circuit value the rate ranking
// prices a lane at: SpreadPerUnit × VolumeCap, weighted by how much of that cap the
// hull can actually absorb (sp-pnx0 hold-fit; shipCapacity <= 0 — no ship context —
// disables the weight, matching trading.RankSpreadsForHold's "zero disables"
// convention). It is also the equal-rate tie-break: at the same $/hr, the bigger
// absolute earner wins (sp-1wp8).
func laneCircuitValue(l trading.ArbitrageLane, shipCapacity int, model laneImpactModel) float64 {
	weight := 1.0
	if shipCapacity > 0 {
		weight = trading.HoldFitWeight(l.VolumeCap, shipCapacity)
	}
	// sp-tl68: rank on the EFFECTIVE spread, not the snapshot. plannedUnits = shipCapacity
	// (the units this hull would move on the lane); effectiveSpreadPerUnit nets out the
	// self-compression that volume would cause plus the live shared cooldown debt, so a
	// lane this hull would compress (high units/tv) or one the fleet has hammered scores
	// below its snapshot spread. An inert model (no coefficients, no ledger) returns the
	// snapshot spread, so this is byte-identical to the pre-sp-tl68 value for every caller
	// that supplies no model.
	return model.effectiveSpreadPerUnit(l, shipCapacity) * float64(l.VolumeCap) * weight
}

// laneCircuitRatePerHour is the sp-1wp8 ranking score: the lane's hold-fit-weighted
// per-circuit value over its estimated circuit hours. A gate-crossing lane pays the
// round-trip jump+cooldown surcharge in its denominator UNLESS it is the operator's
// directed --dest lane (laneMatchesTarget): a directed lane already carries the gate
// time as the operator's explicit choice, so it is ranked at the in-system baseline
// — the same waiver contract the retired subtractive penalty kept (sp-xwa1), narrowed
// to the one lane asked for. Exposed to the selection log so the captain reads the
// exact score a lane won or lost on.
func laneCircuitRatePerHour(l trading.ArbitrageLane, shipCapacity int, targetDest string, model laneImpactModel) float64 {
	crossSystem := shared.ExtractSystemSymbol(l.SourceWaypoint) != shared.ExtractSystemSymbol(l.DestWaypoint)
	charged := crossSystem && !laneMatchesTarget(l, targetDest)
	return laneCircuitValue(l, shipCapacity, model) / (estimatedCircuitSeconds(charged) / 3600)
}

// rankLanesByCircuitRate re-orders lanes already ranked by trading.RankSpreads by
// RATE — hold-fit-weighted per-circuit value over estimated circuit wall-clock
// (sp-1wp8) — one unified score folding the two ranking-only adjustments the pure
// per-unit-spread view can't see:
//
//   - circuit TIME: a gate-crossing circuit spends the round-trip jump+cooldown
//     surcharge (~704s against the ~4000s in-system circuit class, a ~18% time
//     premium) not trading, so its value must clear a proportionally higher bar —
//     expressed honestly as division by hours, replacing the retired sp-xwa1
//     subtractive per-unit haircut. The captain-ruled DP51 economics survive: the
//     surcharge is a bounded premium, so a deep frontier lane a heavy hull fills
//     still out-ranks a saturated home lane (see the xsyspenalty test's ratio pin).
//   - hold-fit weighting (sp-pnx0): a lane's VolumeCap is a market-absorption
//     bound, not a hold-sized one — a hull far bigger than VolumeCap will not
//     clear a single tranche at that depth before moving the price.
//   - price-impact + cooldown (sp-tl68): the score uses each lane's EFFECTIVE
//     per-unit spread (laneImpactModel.effectiveSpreadPerUnit), not its snapshot
//     spread — the snapshot less the self-compression this hull's own volume would
//     cause (era-3 fitted impact, half-terminal average fill) and the live shared
//     cooldown debt from the fleet's recent trades on the lane. So a lane this hull
//     would compress, or one the fleet has hammered, ranks below its snapshot spread
//     and hulls rotate to fresh lanes. An inert model (no coefficients, no ledger)
//     returns the snapshot spread, keeping every model-less caller byte-identical.
//
// These adjustments MUST stay folded into a single score computation rather than
// chained as two sequential re-rankings: this function and
// trading.RankSpreadsForHold/reorderByHoldFit are both "recompute-from-scratch"
// rankers that derive their score purely from each lane's own persistent fields,
// never from the order of the slice passed in. Composing them as funcB(funcA(lanes))
// does not combine their effects — funcB completely overrides funcA's reordering.
// (This is exactly the bug an earlier version of this call site had: scanLanes
// wrapped this function around trading.RankSpreadsForHold's already hold-weighted
// output, but silently discarded that weighting because this function's own score
// ignored input order entirely.)
//
// It returns a NEW slice of the original, unmodified ArbitrageLane values; only
// ordering changes — no lane's real economics (SpreadPerUnit, VolumeCap,
// CappedSpread, ClearsFloor) are mutated. Tie-break chain: rate desc, then absolute
// per-circuit value desc (equal-rate ties go to the bigger absolute earner,
// sp-1wp8), then the lane's REAL SpreadPerUnit desc, then Good asc.
//
// targetDest (sp-xwa1's --dest override) ranks the operator-directed lane at the
// in-system baseline (laneCircuitRatePerHour's waiver); every other cross-system
// lane still pays the surcharge. targetDest=="" (the undirected auto-scan path)
// matches nothing.
func rankLanesByCircuitRate(lanes []trading.ArbitrageLane, shipCapacity int, targetDest string, model laneImpactModel) []trading.ArbitrageLane {
	type scoredLane struct {
		lane  trading.ArbitrageLane
		rate  float64
		value float64
	}

	scored := make([]scoredLane, len(lanes))
	for i, lane := range lanes {
		scored[i] = scoredLane{
			lane:  lane,
			rate:  laneCircuitRatePerHour(lane, shipCapacity, targetDest, model),
			value: laneCircuitValue(lane, shipCapacity, model),
		}
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].rate != scored[j].rate {
			return scored[i].rate > scored[j].rate
		}
		if scored[i].value != scored[j].value {
			return scored[i].value > scored[j].value
		}
		if scored[i].lane.SpreadPerUnit != scored[j].lane.SpreadPerUnit {
			return scored[i].lane.SpreadPerUnit > scored[j].lane.SpreadPerUnit
		}
		return scored[i].lane.Good < scored[j].lane.Good
	})

	result := make([]trading.ArbitrageLane, len(scored))
	for i, s := range scored {
		result[i] = s.lane
	}
	return result
}

// --- lane-targeting override (sp-xwa1) ---

// laneMatchesTarget reports whether lane is the operator-directed destination
// requested via --dest (RunTradeRouteCoordinatorCommand.TargetDest). An empty
// target never matches anything - the zero value means "no directive", not
// "match every lane" - so every caller can treat target=="" as the plain
// undirected path without a separate branch. A non-empty target matches
// either the lane's exact destination waypoint or just its destination
// SYSTEM, so an operator can aim at a whole system ("X1-ABC") without knowing
// which waypoint inside it currently carries the best market, or pin an exact
// waypoint for precision.
func laneMatchesTarget(lane trading.ArbitrageLane, target string) bool {
	if target == "" {
		return false
	}
	return lane.DestWaypoint == target || shared.ExtractSystemSymbol(lane.DestWaypoint) == shared.ExtractSystemSymbol(target)
}

// selectLane is the single lane-selection entry point for both the undirected
// auto-scan and the directed --dest override, so callers never duplicate the
// branch. Undirected (target=="") defers entirely to
// trading.FirstDisciplinedLane's existing ranked-order walk, unchanged.
// Directed (target!="") PINS to the first target-matching lane that clears
// the floor (ClearsFloor - the same discipline FirstDisciplinedLane enforces
// on the undirected path), walking the caller-supplied order rather than
// searching for the single highest-ranked lane overall: an operator who names
// a destination gets that destination if it is flyable at all, never a
// silent substitute the ranker would have preferred instead. If no
// target-matching lane clears the floor, it reports ok=false rather than
// falling back to an auto-picked lane the operator didn't ask for (the same
// "fail rather than silently substitute" contract the batch-purchase
// ship-type guard already established, sp-e7je).
func selectLane(lanes []trading.ArbitrageLane, target string) (trading.ArbitrageLane, bool) {
	if target == "" {
		return trading.FirstDisciplinedLane(lanes)
	}
	for _, l := range lanes {
		if laneMatchesTarget(l, target) && l.ClearsFloor() {
			return l, true
		}
	}
	return trading.ArbitrageLane{}, false
}
