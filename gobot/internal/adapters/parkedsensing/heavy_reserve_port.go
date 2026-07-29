package parkedsensing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// HeavyReservePort computes the credits sensing holds back for the next heavy purchase, so probe
// buying stands down while a heavy accumulates and resumes the moment it lands.
//
// It DERIVES the answer every tick from durable facts and stores nothing — the same three tables
// the fleet autosizer reads, plus the autosizer's own persisted heavy_cap. Reading that cap is
// reading a durable fact, exactly like reading the ledger for heaviesOwned; it is NOT a
// cross-container write protocol, which is the shape spec §3 rejected. Sensing deliberately does
// NOT own a heavy_cap of its own: two dials for one decision is how the reservation ends up
// computed against a cap nobody is enforcing.
//
// The arithmetic is common.HeavyReserve and is never re-derived here.
type HeavyReservePort struct {
	census heavyCensusCounter
	yards  heavyYardPricer
	caps   heavyCapSource
}

// heavyCensusCounter is the broad, tag-independent owned-heavy census.
type heavyCensusCounter interface {
	CountHeavyHulls(ctx context.Context, playerID int) (int, error)
}

// heavyYardPricer reports the heavy yard the PURCHASE PATH would target, and its ask.
//
// It is the SAME implementation the fleet autosizer consumes (shipyardQueries.HeavyTargetFinder),
// not a second query that happens to agree. That is the whole point: the reservation's arithmetic
// already has exactly one definition (common.HeavyReserve), and its price term now has exactly one
// too, so the spender and the withholder cannot end up saving toward different yards.
//
// It is deliberately NOT "the cheapest priced yard" any more (sp-fwk8z). The buy targets the
// NEAREST reachable yard; reserving the cheapest ask on the map under-reserves whenever the two
// differ, and treasury then tops out below what we will actually be asked.
type heavyYardPricer interface {
	HeavyTarget(ctx context.Context, playerID int) (shipyardQueries.HeavyTarget, error)
}

// heavyCapSource resolves the autosizer's heavy_cap. It reports three things separately because
// each maps to a different rung of the fallback ladder: whether the autosizer container exists at
// all, whether the knob is set on it, and its value.
type heavyCapSource interface {
	HeavyCap(ctx context.Context, playerID int) (value int, present bool, containerExists bool, err error)
}

// NewHeavyReservePort wires the reservation's three reads.
func NewHeavyReservePort(census heavyCensusCounter, yards heavyYardPricer, caps heavyCapSource) *HeavyReservePort {
	return &HeavyReservePort{census: census, yards: yards, caps: caps}
}

// Reserve returns the credits to hold back for the next heavy.
//
// THE TWO DATA READS ANSWER ONE RULE: an unreadable input reserves ZERO, loudly. Both the census
// and the yard-target reads follow the same shape, so this function has one policy rather than
// two. They matter together, not separately: they hit the same database and therefore fail in the
// same window, so converting one and not the other would leave the divergence alive in half.
//
// THE CAP IS DIFFERENT, and deliberately so (sp-fwk8z). It is not a datum the reservation is
// computed FROM so much as a bound it is computed WITHIN, and an unreadable bound has a
// well-defined answer that an unreadable price does not: the documented default, which is exactly
// what the autosizer itself falls back to when its own read of the same knob fails. See
// resolveHeavyCap for why reading it as zero was a live divergence rather than a safe default.
//
// It is the rule for three reasons:
//
//   - It is the SAME direction the fleet autosizer takes. The autosizer derives the reserve only
//     when its census read succeeded, so a blind tick leaves the reserve at 0 and light buying
//     proceeds. Failing the other way here made the two consumers of one shared predicate fail in
//     OPPOSITE directions, and the window is reachable rather than theoretical: the census is
//     DB-backed while the autosizer's treasury read is API-backed, so a database-only fault leaves
//     treasury readable and the census unreadable — exactly the state where sensing halted
//     entirely while the autosizer spent the accumulation on light hulls, unreserved.
//   - It is what the shared predicate's own documented rationale asks for: "A reserve that failed
//     'closed' by holding treasury on an unreadable input would starve expansion on a blind
//     signal." Halting the spender instead of holding the reserve produced that same outcome by a
//     different mechanism.
//   - It recovers. A wrongly-HELD reserve has no natural end — it persists for as long as the read
//     keeps failing — while a zero reserve is inert and clears the moment the read works.
//
// None of it is money-unsafe (RULINGS #4): this predicate authorises no spend of its own, it only
// withholds, and the immutable floor, the probe cap and the autosizer's full guard stack all still
// bind independently. The cost of being wrong in this direction is strategic — a probe bought
// where we would rather have saved — never solvency.
//
// THE WARNING IS HALF THE RULE, not decoration. A silent zero is indistinguishable, in every gauge
// and every heartbeat, from "there is nothing to save for" — which is how a fleet never accumulates
// a heavy and nobody ever finds out.
//
// The error return is kept deliberately: it is the HeavyReserveReader contract, and DrainBuyQueue
// still fails CLOSED on any reader that does surface one. This implementation simply never needs
// to, because "cannot tell" has a well-defined answer here, and it is zero.
func (p *HeavyReservePort) Reserve(ctx context.Context, playerID int) (int64, error) {
	cap, capOK := p.resolveHeavyCap(ctx, playerID)
	if !capOK {
		// No autosizer, or one that cannot spend ⇒ no heavy buyer ⇒ nothing to save for.
		return 0, nil
	}

	owned, err := p.census.CountHeavyHulls(ctx, playerID)
	if err != nil {
		warnBlindReserve(ctx, playerID, "census", err)
		return 0, nil
	}

	target, err := p.yards.HeavyTarget(ctx, playerID)
	if err != nil {
		warnBlindReserve(ctx, playerID, "yard prices", err)
		return 0, nil
	}

	return common.HeavyReserve(common.HeavyReserveInputs{
		CapabilityOpen:  target.CapabilityOpen,
		HeaviesOwned:    owned,
		HeavyCap:        cap,
		TargetYardPrice: target.PurchasePrice,
	}), nil
}

// warnBlindReserve announces that a durable input went unreadable, so nothing is being held back.
//
// LOUD, and not optional — for the same reason the cap-resolution warning beside it is. An un-held
// reserve is invisible: the fleet looks healthy and simply never accumulates a heavy. This line is
// the only thing that tells "not saving because we are blind" apart from "not saving because there
// is nothing to save for".
//
// SILENT on an abandoned tick, and only there. A daemon shutdown cancels every read at once, so
// without this the one alarm that means "this feature is broken" would fire on every restart until
// nobody read it any more. The reserve is zero either way; only the noise is suppressed.
func warnBlindReserve(ctx context.Context, playerID int, input string, err error) {
	if abandoned(ctx, err) {
		return
	}
	logging.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
		"Sensing heavy reserve: could not read the heavy %s (%v) — reserving NOTHING this tick, so probe buying is NOT standing down for a heavy. Treasury will not accumulate toward one while this persists.",
		input, err,
	), map[string]interface{}{
		"action":    "sensing_heavy_reserve_blind",
		"player_id": playerID,
		"input":     input,
		"error":     err.Error(),
	})
}

// abandoned reports that a read failed because the WORK WAS CALLED OFF, not because anything is
// broken — the tick's context is done, or the read came back cancelled.
//
// THIS IS THE sp-fwk8z FALSE-ALARM FIX, and it is not cosmetic. Every occurrence of the live
// "could not resolve the autosizer's heavy_cap (... context canceled)" warning landed within two
// seconds of a whole-process shutdown, in the same millisecond as every other container's "Context
// canceled, stopping container" — daemon restarts, roughly one an hour, never a healthy tick. On
// such a tick nothing works and nothing is bought, so reserving zero is not merely harmless, it is
// correct; the warning was pure noise.
//
// Noise here is expensive in one specific way. This warning is the ONLY signal that distinguishes
// "not saving because we are blind" from "not saving because there is nothing to save for", and a
// line that cries wolf once per restart is a line operators learn to scroll past — which is exactly
// how the genuine failure would go unnoticed. Suppressing the abandoned-work case is what keeps the
// remaining warnings meaningful.
//
// It suppresses only the LOG. The reserve still resolves to zero on an abandoned tick, so no money
// guard is touched (RULINGS #4) and nothing is spent that would not otherwise have been.
//
// Both the context and the error are consulted. ctx.Err() is authoritative for the tick, and a
// cancelled error carried up from an inner context means the same thing to an operator either way:
// the work was called off, and there is nothing here to act on.
func abandoned(ctx context.Context, err error) bool {
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// resolveHeavyCap walks the fallback ladder. ok=false means "reserve nothing".
func (p *HeavyReservePort) resolveHeavyCap(ctx context.Context, playerID int) (int, bool) {
	value, present, containerExists, err := p.caps.HeavyCap(ctx, playerID)
	switch {
	case err != nil:
		// AN UNREADABLE CAP IS NOT A CAP OF ZERO — it resolves to the SAME documented default an
		// UNSET cap resolves to, one rung below.
		//
		// This used to reserve nothing, and that was a real divergence rather than a stylistic
		// choice: the fleet autosizer, reading THE SAME KNOB from THE SAME TABLE, explicitly
		// refuses to read a failed read as zero — liveHeavyCap falls back to its launch value on a
		// snapshot error, documented as "NEVER 0, which would read as an operator hold and
		// silently stop all heavy buying". Sensing doing the opposite meant the two consumers of
		// one shared predicate disagreed about the cap in precisely the window where they are
		// otherwise guaranteed to agree.
		//
		// It cannot wrongly hold treasury for long, which was the original objection. The census
		// and the yard reads that follow hit the SAME database, so a persistent database fault
		// still reserves zero through them; the only window this rung changes is the narrow one
		// where the containers table alone is unreadable — and in that window the autosizer is
		// itself running on its compiled default, so agreeing with it is the correct answer.
		//
		// No money guard is touched (RULINGS #4): this predicate authorises no spend, it only
		// withholds, and the reserve still requires a positive target price and an under-cap
		// census before a single credit is held back.
		//
		// AN ABANDONED TICK IS NOT AN UNREADABLE CAP. When the work was called off (see abandoned)
		// the honest answer is not a default but "we did not look": the tick is over, every other
		// read in it is failing too, and nothing will be bought either way. It reserves nothing,
		// silently — the inert answer, and the one that cannot leave a stale number on a heartbeat
		// gauge for a tick that never completed.
		if abandoned(ctx, err) {
			return 0, false
		}
		// LOUD, and not optional. An un-held or wrongly-held reserve is invisible: the fleet looks
		// healthy either way.
		logging.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"Sensing heavy reserve: could not resolve the autosizer's heavy_cap (%v) — falling back to the documented default %d, the same value the autosizer itself resolves to when its own read fails, so the two cannot disagree about the cap. Investigate the container config read.",
			err, fleetCmd.FleetAutosizerTunableDefaults()[heavyCapKey],
		), map[string]interface{}{
			"action":    "sensing_heavy_cap_unresolved",
			"player_id": playerID,
			"error":     err.Error(),
		})
		return fleetCmd.FleetAutosizerTunableDefaults()[heavyCapKey], true
	case !containerExists:
		// No autosizer, or one that cannot spend (terminal) ⇒ no heavy buyer ⇒ nothing to save
		// for. Both are expected configurations rather than faults, so this rung is deliberately
		// SILENT: every probe-only deployment would otherwise warn every tick.
		return 0, false
	case !present:
		// The knob is unset on a live autosizer, so both sides resolve the SAME documented
		// default and cannot disagree about a cap.
		return fleetCmd.FleetAutosizerTunableDefaults()[heavyCapKey], true
	default:
		return value, true
	}
}

// AutosizerCapPort resolves heavy_cap from the fleet autosizer's own persisted container config —
// the SINGLE source, so sensing's reservation can never be computed against a cap the autosizer is
// not enforcing.
//
// It reads BOTH of the autosizer's keys, in the autosizer's own precedence. Reading only one is a
// live bug, not a simplification: config.yaml writes ONLY the prefixed key and `tune` writes ONLY
// the bare one, so either half alone makes an operator's cap invisible. The exact ladder, and why
// each rung is shaped the way it is, is on the HeavyCap method below — read it before changing
// anything here.
type AutosizerCapPort struct {
	db *gorm.DB
}

// NewAutosizerCapPort wires the autosizer-config-backed heavy_cap read.
func NewAutosizerCapPort(db *gorm.DB) *AutosizerCapPort { return &AutosizerCapPort{db: db} }

// HeavyCap reports the autosizer's effective cap, whether the knob is set, and whether an
// autosizer exists at all — three answers because each maps to a different rung of the reserve's
// fallback ladder.
//
// It MIRRORS the autosizer's own two-key ladder exactly, and must continue to:
//
//  1. the BARE "heavy_cap" key when POSITIVE — the live-tuned override (liveHeavyCap), which
//     survives a rebuild because it is not in the cleared autosizer_* set;
//  2. otherwise the PREFIXED "autosizer_heavy_cap" key with present-vs-absent semantics — the
//     operator's config.yaml value (presentIntPtr → resolveHeavyCap). An explicit 0 here is a
//     HOLD, not an absence, and must be reported as present so it propagates;
//  3. otherwise present=false, so the caller applies the same compiled default the autosizer
//     resolves to.
//
// Reading only the bare key is the bug this ladder exists to prevent: config.yaml writes ONLY the
// prefixed key, so an operator setting heavy_cap: 0 (the documented hold) would leave the autosizer
// refusing every heavy while sensing reserved for one forever — permanent expansion starvation
// from the conservative-seeming action.
//
// A NEGATIVE value resolves to the default (present=false), matching resolveHeavyCap: a typo must
// not read as an intentional hold.
//
// Only a container that CAN BUY counts: an autosizer that buys nothing is not a heavy buyer, so
// holding treasury for it would starve expansion for a purchase that cannot happen. A TERMINAL
// autosizer is the one shape that takes — excluded by the status filter in the query. A live
// autosizer always spends when its guard stack clears; there is no observe-only mode to exclude.
//
// Selection is DETERMINISTIC — RUNNING before PENDING, then by id — because during a restart a
// PENDING replacement can sit beside the RUNNING autosizer and an arbitrary winner would make the
// cap flap between ticks.
func (p *AutosizerCapPort) HeavyCap(ctx context.Context, playerID int) (int, bool, bool, error) {
	if p.db == nil {
		return 0, false, false, fmt.Errorf("database not configured")
	}
	var models []persistence.ContainerModel
	err := p.db.WithContext(ctx).
		Where("player_id = ? AND container_type = ? AND status IN ?",
			playerID,
			string(container.ContainerTypeFleetAutosizer),
			[]string{string(container.ContainerStatusRunning), string(container.ContainerStatusPending)},
		).
		// EXPLICIT precedence, not an alphabetical accident: "PENDING" actually sorts BEFORE
		// "RUNNING", so ordering by status would pick the replacement over the live autosizer.
		// RUNNING wins; id breaks any remaining tie so the same container wins every tick.
		Order("CASE status WHEN 'RUNNING' THEN 0 ELSE 1 END, id ASC").
		Find(&models).Error
	if err != nil {
		return 0, false, false, fmt.Errorf("read autosizer container for heavy_cap: %w", err)
	}
	if len(models) == 0 {
		return 0, false, false, nil // no autosizer ⇒ no heavy buyer
	}

	// ONE authoritative container, read through the whole ladder. Scanning several and taking
	// the first that happens to carry a key would let a container WITHOUT the knob shadow one
	// that has it, purely on ordering.
	authoritative := models[0]
	if authoritative.Config == "" {
		return 0, false, true, nil
	}
	cfg := map[string]interface{}{}
	if err := json.Unmarshal([]byte(authoritative.Config), &cfg); err != nil {
		return 0, false, true, fmt.Errorf("parse autosizer container %s config: %w", authoritative.ID, err)
	}
	// Rung 1: the live-tuned bare key, positive only (its own revert semantics).
	if v, ok := configInt(cfg[heavyCapKey]); ok && v > 0 {
		return v, true, true, nil
	}
	// Rung 2: the operator's config.yaml value. Present-and-zero is a HOLD and must survive;
	// present-and-negative is a typo and defers to the default.
	if v, ok := configInt(cfg[prefixedHeavyCapKey]); ok {
		if v < 0 {
			return 0, false, true, nil
		}
		return v, true, true, nil
	}
	return 0, false, true, nil // autosizer present, knob unset ⇒ the documented default applies
}

// configInt decodes every numeric shape a persisted container config carries: native ints on the
// fresh-launch path and float64 on the JSON-recovery path. Omitting a shape would silently drop an
// operator's cap and read as "unset".
func configInt(raw interface{}) (int, bool) {
	switch v := raw.(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	}
	return 0, false
}

// heavyCapKey is the BARE tune key the autosizer's LIVE cap is stored under (survives a rebuild).
// prefixedHeavyCapKey is the LAUNCH key config.yaml is injected into (cleared and re-injected on
// every rebuild). The autosizer reads BOTH; so must this.
const (
	heavyCapKey         = "heavy_cap"
	prefixedHeavyCapKey = "autosizer_heavy_cap"
)

// heavyHullRepo is the concrete ship-repository capability the census needs. It is asserted
// rather than added to navigation.ShipRepository so that broad interface (and every fake
// implementing it) stays untouched — the same choice the autosizer's census wiring made.
type heavyHullRepo interface {
	CountHeavyHulls(ctx context.Context, playerID shared.PlayerID) (int, error)
}

// ShipRepoCensus adapts the ship repository's PlayerID-typed heavy census to the reserve's
// int-typed port.
type ShipRepoCensus struct {
	repo heavyHullRepo
}

// NewShipRepoCensus asserts the census capability off a ship repository. A repository that does
// NOT implement it yields a census whose reads ERROR rather than silently returning zero: a zero
// census reads as "no heavies owned" and would size the reservation against a fleet we cannot see.
func NewShipRepoCensus(repo any) *ShipRepoCensus {
	if r, ok := repo.(heavyHullRepo); ok {
		return &ShipRepoCensus{repo: r}
	}
	log.Printf("WARNING: sensing heavy census UNWIRED — the ship repository does not implement CountHeavyHulls, so the heavy reservation cannot be computed and probe buying will not stand down for a heavy")
	return &ShipRepoCensus{}
}

// CountHeavyHulls counts owned heavy hulls, erroring when the capability was never wired.
func (c *ShipRepoCensus) CountHeavyHulls(ctx context.Context, playerID int) (int, error) {
	if c.repo == nil {
		return 0, fmt.Errorf("heavy census capability not wired on the ship repository")
	}
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, err
	}
	return c.repo.CountHeavyHulls(ctx, pid)
}
