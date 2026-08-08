package parkedsensing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/hullbuy"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// HeavyReservePort resolves the ASK sensing is saving toward for the next heavy purchase, so probe
// buying can stand down while a heavy accumulates and resume the moment it lands.
//
// IT RESOLVES THE TARGET, NOT THE HOLD. The credits actually withheld are
// common.HeavyReserveTarget.HoldAt(treasury), applied by the drain at the point the live balance
// is known. The split is deliberate and load-bearing here: every read behind this port is a LOCAL
// DB query, which is what lets DrainBuyQueue call it ahead of every network gate so a tick with
// nothing to buy costs no API call. A treasury read inside this port would put a (ledger-first,
// live-fallback) credits call on every sensing tick, including the ones that buy nothing.
//
// It DERIVES the answer every tick from durable facts and stores nothing — the same three tables
// the heavy buyer reads, plus the buyer's own persisted heavy_cap. Reading that cap is reading a
// durable fact, exactly like reading the ledger for heaviesOwned; it is NOT a
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
// It is deliberately NOT "the cheapest priced yard". The buy targets the NEAREST reachable yard;
// reserving the cheapest ask on the map under-reserves whenever the two differ, and treasury then
// tops out below what we will actually be asked.
type heavyYardPricer interface {
	HeavyTarget(ctx context.Context, playerID int) (shipyardQueries.HeavyTarget, error)
}

// heavyCapSource resolves the heavy buyer's heavy_cap. It reports three things separately because
// each maps to a different rung of the fallback ladder: whether a heavy buyer exists at all,
// whether the knob is set on it, and its value.
type heavyCapSource interface {
	HeavyCap(ctx context.Context, playerID int) (value int, present bool, containerExists bool, err error)
}

// NewHeavyReservePort wires the reservation's three reads.
func NewHeavyReservePort(census heavyCensusCounter, yards heavyYardPricer, caps heavyCapSource) *HeavyReservePort {
	return &HeavyReservePort{census: census, yards: yards, caps: caps}
}

// Reserve returns the ask to hold back for the next heavy and, SEPARATELY, whether the operator's
// cap is what bars it (common.HeavyCapBinding) — NOT derivable from the first, since a zero target
// is every "cannot" at once and they call for opposite responses downstream. Both come off ONE set
// of reads, so they cannot disagree, and EVERY BLIND PATH BELOW REPORTS false.
//
// THE TWO DATA READS ANSWER ONE RULE: an unreadable input reserves ZERO, loudly. Both the census
// and the yard-target reads follow the same shape, so this function has one policy rather than
// two. They matter together, not separately: they hit the same database and therefore fail in the
// same window, so converting one and not the other would leave the divergence alive in half.
//
// THE CAP IS DIFFERENT, and deliberately so. It is not a datum the reservation is computed FROM
// so much as a bound it is computed WITHIN, and an unreadable bound has a well-defined answer that
// an unreadable price does not: the documented default, which is exactly what the autosizer itself
// falls back to when its own read of the same knob fails. See resolveHeavyCap for why reading it as
// zero diverges from the buyer rather than defaulting safely.
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
// The error return is kept deliberately: the wave port propagates it and the drain fails the tick
// CLOSED on any reserve that does surface one. This implementation simply never needs to, because
// "cannot tell" has a well-defined answer here, and it is zero.
func (p *HeavyReservePort) Reserve(ctx context.Context, playerID int) (common.HeavyReserveTarget, bool, error) {
	heavyCap, capOK := p.resolveHeavyCap(ctx, playerID)
	if !capOK {
		// Nothing owns heavy buying, or the owner cannot spend ⇒ nothing to save for.
		return 0, false, nil
	}

	owned, err := p.census.CountHeavyHulls(ctx, playerID)
	if err != nil {
		warnBlindReserve(ctx, playerID, "census", err)
		return 0, false, nil
	}

	target, err := p.yards.HeavyTarget(ctx, playerID)
	if err != nil {
		warnBlindReserve(ctx, playerID, "yard prices", err)
		return 0, false, nil
	}

	// ONE inputs value, read by both predicates. Assembling it twice is how the reservation and the
	// cap verdict would end up describing different fleets.
	in := common.HeavyReserveInputs{
		CapabilityOpen:  target.CapabilityOpen,
		HeaviesOwned:    owned,
		HeavyCap:        heavyCap,
		TargetYardPrice: target.PurchasePrice,
	}
	return common.HeavyReserve(in), common.HeavyCapBinding(in), nil
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
// A CANCELLED TICK IS A SHUTDOWN, NOT A FAULT. The "could not resolve the autosizer's heavy_cap
// (... context canceled)" warning fires alongside every other container's "Context canceled,
// stopping container" — a daemon restart, never a healthy tick. On such a tick nothing works and
// nothing is bought, so reserving zero is not merely harmless, it is correct, and the warning
// would be pure noise.
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
	// Named once, because two separate rungs below fall back to it and the whole point of both is
	// that they land on the SAME number the buyer itself resolves to — hence the SHARED constant.
	documentedCap := hullbuy.DefaultHeavyCap

	value, present, containerExists, err := p.caps.HeavyCap(ctx, playerID)
	switch {
	case err != nil:
		// AN UNREADABLE CAP IS NOT A CAP OF ZERO — it resolves to the SAME documented default an
		// UNSET cap resolves to, one rung below.
		//
		// Reserving nothing here would be a real divergence rather than a stylistic
		// choice: the heavy buyer, reading THE SAME KNOB from THE SAME TABLE, explicitly
		// refuses to read a failed read as zero — liveHeavyCap falls back to its launch value on a
		// snapshot error, documented as "NEVER 0, which would read as an operator hold and
		// silently stop all heavy buying". Sensing doing the opposite would leave the two consumers
		// of one shared predicate disagreeing about the cap in precisely the window where they are
		// otherwise guaranteed to agree.
		//
		// It cannot wrongly hold treasury for long. The census and the yard reads that
		// follow hit the SAME database, so a persistent database fault
		// still reserves zero through them; the only window this rung changes is the narrow one
		// where the containers table alone is unreadable — and in that window the buyer is
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
			"Sensing heavy reserve: could not resolve the heavy buyer's heavy_cap (%v) — falling back to the documented default %d, the same value the buyer itself resolves to when its own read fails, so the two cannot disagree about the cap. Investigate the container config read.",
			err, documentedCap,
		), map[string]interface{}{
			"action":    "sensing_heavy_cap_unresolved",
			"player_id": playerID,
			"error":     err.Error(),
		})
		return documentedCap, true
	case !containerExists:
		// Nothing owns heavy buying, or the owner cannot spend (terminal) ⇒ nothing to save for.
		// Both are expected configurations rather than faults, so this rung is deliberately
		// SILENT: every probe-only deployment would otherwise warn every tick. A buyer whose type
		// nothing declares does NOT land here — the cap read resolves it off its knob, loudly.
		return 0, false
	case !present:
		// The knob is unset on a live buyer, so both sides resolve the SAME documented
		// default and cannot disagree about a cap.
		return documentedCap, true
	default:
		return value, true
	}
}

// HeavyBuyerCapPort resolves heavy_cap from the persisted container config of WHOEVER OWNS HEAVY
// BUYING — the SINGLE source, so sensing's reservation can never be computed against a cap the
// buyer is not enforcing.
//
// It names no coordinator, which is what lets the cap survive heavy buying changing hands. The
// owner is found by DECLARATION (hullbuy.HeavyBuyerContainers) and, failing that, by the KNOB: a
// live container carrying a heavy cap key is a heavy buyer whose type nothing declares, and that
// resolves loudly rather than reading as "no buyer exists".
//
// It reads BOTH of the buyer's keys, in the buyer's own precedence. Reading only one is a live bug,
// not a simplification: config.yaml writes ONLY the prefixed key and `tune` writes ONLY the bare
// one, so either half alone makes an operator's cap invisible. The exact ladder, and why each rung
// is shaped the way it is, is on the HeavyCap method below — read it before changing anything here.
//
// IT ALSO SERVES THE BUYER'S MASTER SWITCH, off the SAME row. The cap and the switch answer one
// question between them — "is anyone buying heavies, and up to what ceiling" — so resolving them
// from two containers is the split this port exists to close, one knob later. buyerConfig is the
// shared half; neither public method re-issues the query.
type HeavyBuyerCapPort struct {
	db *gorm.DB
}

// NewHeavyBuyerCapPort wires the container-config-backed heavy_cap read.
func NewHeavyBuyerCapPort(db *gorm.DB) *HeavyBuyerCapPort { return &HeavyBuyerCapPort{db: db} }

// HeavyCap reports the heavy buyer's effective cap, whether the knob is set, and whether a heavy
// buyer exists at all — three answers because each maps to a different rung of the reserve's
// fallback ladder.
//
// It MIRRORS the buyer's own two-key ladder exactly, and must continue to:
//
//  1. the BARE "heavy_cap" key when POSITIVE — the live-tuned override (liveHeavyCap), which
//     survives a rebuild because it is not in the cleared launch-key set;
//  2. otherwise the coordinator-prefixed LAUNCH key, matched on its shared "_heavy_cap" suffix so
//     the prefix travels with its owner, with present-vs-absent semantics — the operator's
//     config.yaml value. An explicit 0 here is a HOLD, not an absence, and must be reported as
//     present so it propagates;
//  3. otherwise present=false, so the caller applies the same compiled default the buyer
//     resolves to.
//
// Reading only the bare key is the bug this ladder exists to prevent: config.yaml writes ONLY the
// prefixed key, so an operator setting heavy_cap: 0 (the documented hold) would leave the buyer
// refusing every heavy while sensing reserved for one forever — permanent expansion starvation
// from the conservative-seeming action.
//
// A NEGATIVE value resolves to the default (present=false), matching resolveHeavyCap: a typo must
// not read as an intentional hold.
//
// Only a container that CAN BUY counts: a buyer that buys nothing is not a heavy buyer, so holding
// treasury for it would starve expansion for a purchase that cannot happen. A TERMINAL container is
// the one shape that takes — excluded by the status filter in the query. A live buyer always spends
// when its guard stack clears; there is no observe-only mode to exclude.
//
// Selection is DETERMINISTIC — RUNNING before PENDING, then by id — because during a restart a
// PENDING replacement can sit beside the RUNNING buyer and an arbitrary winner would make the cap
// flap between ticks. A DECLARED owner outranks any knob-carrying stranger, so a tune landing on
// the wrong container cannot capture the cap while a real owner is live.
func (p *HeavyBuyerCapPort) HeavyCap(ctx context.Context, playerID int) (int, bool, bool, error) {
	cfg, buyerExists, undeclared, err := p.buyerConfig(ctx, playerID)
	if err != nil || !buyerExists {
		return 0, false, buyerExists, err
	}
	if undeclared != nil {
		warnUndeclaredHeavyBuyer(ctx, playerID, undeclared)
	}
	value, present := heavyCapFromConfig(cfg)
	return value, present, true, nil
}

// GrowthEnabled reports the heavy buyer's master switch, read off the SAME container row the cap
// resolves from — through the same query and the same selection, so the two knobs can never come
// off different containers. buyerExists=false means no heavy buyer container is deployed at all,
// which is a quiet, expected configuration (a probe-only deployment) rather than a fault.
//
// IT MUST REACH THE DRAIN. A switched-off buyer with a priced target would otherwise leave the
// drain pausing probe buying toward a purchase nothing can make — a deadlock no spender can clear.
// That is why the switch is the wave predicate's FIRST clause rather than a detail of the
// coordinator that owns it.
//
// 1=on, 2=off. Anything else — including the absent-key 0 — is the documented default, ON:
// `tune <key> 0` DELETES the key, so absence IS the revert. The key and the off sentinel are the
// buyer's own (hullbuy.HeavyBuyerSwitchKey), never a second spelling.
//
// THE SWITCH NEEDS A DECLARED OWNER; THE CAP DOES NOT — the one asymmetry between the two reads,
// and the reason they are separate methods over one row rather than one method returning both.
// The cap is a BOUND: resolved off a knob-carrying stranger it sizes the reservation against a
// ceiling that may be nobody's, which is a strategic error, and the WARN beside it announces
// exactly that. The switch answers a different question — CAN A HEAVY PURCHASE HAPPEN AT ALL — and
// only a DECLARATION answers it, because a declaration names a coordinator whose code buys heavies
// while a cap knob is just a number any container's config blob may carry. A coordinator that has
// stopped buying leaves its launch keys behind for as long as its row lives, so "carries a cap
// knob" and "is running a heavy buy path" are genuinely different facts.
//
// A STRANGER THEREFORE CANNOT VOUCH FOR A SWITCH IT NEVER HAD. Reading its ABSENT key as the
// documented ON would let a leftover row derive HEAVY, and HEAVY pauses probe buying outright —
// for a buyer that does not exist, with no spender able to reach the ask and clear it. That is the
// deadlock this switch was added to prevent, one layer up. So a stranger reports the switch as NOT
// ON while still EXISTING, and both halves of that matter: the drain then reads PROBE and keeps
// buying (the recoverable direction, and the predicate's own "every cannot answer is PROBE"),
// while the reserve read behind it still runs, so the undeclared-buyer WARN still fires and a
// stale declaration stays loud rather than becoming the silent state.
//
// No money guard is touched (RULINGS #4): this authorises no spend. It only declines to PAUSE
// probe buying, and every probe bought still passes the buy floor, the immutable reserve and the
// probe cap. The cost of being wrong here is a deferred heavy while an operator declares the
// container type the WARN names; the cost of being wrong the other way has no end.
//
// IT DOES NOT REPEAT THE UNDECLARED-BUYER WARN. Both public methods resolve the same row on the
// same tick, so warning here would double a line whose whole value is that it is rare; the cap read
// beside it carries it.
func (p *HeavyBuyerCapPort) GrowthEnabled(ctx context.Context, playerID int) (bool, bool, error) {
	cfg, buyerExists, undeclared, err := p.buyerConfig(ctx, playerID)
	if err != nil || !buyerExists {
		return false, false, err
	}
	if undeclared != nil {
		return false, true, nil
	}
	return growthSwitchOn(cfg), true, nil
}

// growthSwitchOn reads the master switch out of a DECLARED buyer's config. An unreadable or absent
// value is ON, which is the documented default rather than a guess — but only for a container the
// declaration says owns the switch. A stranger never reaches here; see GrowthEnabled.
func growthSwitchOn(cfg map[string]interface{}) bool {
	value, ok := configInt(cfg[hullbuy.HeavyBuyerSwitchKey])
	return !ok || value != hullbuy.HeavyBuyerSwitchOff
}

// buyerConfig finds the authoritative heavy buyer and returns its parsed config — the half both
// public reads share, factored so the cap and the switch cannot be resolved off different rows.
//
// It reports the undeclared stranger it fell back to (nil when a declared owner won) rather than
// warning here, so exactly one caller owns that line.
//
// A nil config with buyerExists=true is a live buyer carrying no config at all: every knob then
// resolves to its documented default, which is what an untuned coordinator must read as.
func (p *HeavyBuyerCapPort) buyerConfig(ctx context.Context, playerID int) (map[string]interface{}, bool, *persistence.ContainerModel, error) {
	if p.db == nil {
		return nil, false, nil, fmt.Errorf("database not configured")
	}
	declared := hullbuy.HeavyBuyerContainers()
	declaredTypes := make([]string, 0, len(declared))
	for _, t := range declared {
		declaredTypes = append(declaredTypes, string(t))
	}
	var models []persistence.ContainerModel
	err := p.db.WithContext(ctx).
		Where("player_id = ? AND status IN ? AND (container_type IN ? OR config LIKE ?)",
			playerID,
			[]string{string(container.ContainerStatusRunning), string(container.ContainerStatusPending)},
			declaredTypes,
			"%"+heavyCapKey+"%",
		).
		// EXPLICIT precedence, not an alphabetical accident: "PENDING" actually sorts BEFORE
		// "RUNNING", so ordering by status would pick the replacement over the live buyer.
		// RUNNING wins; id breaks any remaining tie so the same container wins every tick.
		Order("CASE status WHEN 'RUNNING' THEN 0 ELSE 1 END, id ASC").
		Find(&models).Error
	if err != nil {
		return nil, false, nil, fmt.Errorf("read heavy buyer container for heavy_cap: %w", err)
	}

	// ONE authoritative container, read through the whole ladder. Scanning several and taking the
	// first that happens to carry a key would let a container WITHOUT the knob shadow one that has
	// it, purely on ordering.
	authoritative, knobbed, knobbedCfg := selectHeavyBuyer(models, declaredTypes)
	if authoritative == nil {
		if knobbed == nil {
			return nil, false, nil, nil // nothing owns heavy buying ⇒ nothing to save for
		}
		return knobbedCfg, true, knobbed, nil
	}
	if authoritative.Config == "" {
		return nil, true, nil, nil
	}
	cfg := map[string]interface{}{}
	if err := json.Unmarshal([]byte(authoritative.Config), &cfg); err != nil {
		return nil, true, nil, fmt.Errorf("parse heavy buyer container %s config: %w", authoritative.ID, err)
	}
	return cfg, true, nil, nil
}

// selectHeavyBuyer picks the authoritative DECLARED owner, and separately the best knob-carrying
// stranger, from rows already in precedence order.
//
// The stranger is a FALLBACK only, so the scan stops at the first declared owner: while one is
// live it is the authority, whatever else carries a key. A stranger's config is returned with it
// because the LIKE filter that fetched it is a substring test over the whole config blob and only
// a parse can tell a real cap key from a value that merely contains the words.
func selectHeavyBuyer(models []persistence.ContainerModel, declaredTypes []string) (authoritative, knobbed *persistence.ContainerModel, knobbedCfg map[string]interface{}) {
	for i := range models {
		m := &models[i]
		if slices.Contains(declaredTypes, m.ContainerType) {
			return m, nil, nil
		}
		if knobbed == nil {
			if cfg, ok := capCarryingConfig(m.Config); ok {
				knobbed, knobbedCfg = m, cfg
			}
		}
	}
	return nil, knobbed, knobbedCfg
}

// capCarryingConfig parses a container config and reports whether it actually carries a heavy cap
// key under either rung of the ladder.
func capCarryingConfig(raw string) (map[string]interface{}, bool) {
	if raw == "" {
		return nil, false
	}
	cfg := map[string]interface{}{}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, false
	}
	if _, ok := cfg[heavyCapKey]; ok {
		return cfg, true
	}
	for key := range cfg {
		if strings.HasSuffix(key, launchHeavyCapSuffix) {
			return cfg, true
		}
	}
	return nil, false
}

// heavyCapFromConfig walks the two-key ladder over one container's config.
func heavyCapFromConfig(cfg map[string]interface{}) (int, bool) {
	// Rung 1: the live-tuned bare key, positive only (its own revert semantics).
	if v, ok := configInt(cfg[heavyCapKey]); ok && v > 0 {
		return v, true
	}
	// Rung 2: the operator's config.yaml value. Present-and-zero is a HOLD and must survive;
	// present-and-negative is a typo and defers to the default.
	if v, ok := launchCapValue(cfg); ok {
		if v < 0 {
			return 0, false
		}
		return v, true
	}
	return 0, false // knob unset ⇒ the documented default applies
}

// launchCapValue reads the coordinator-prefixed launch key. It matches on the suffix rather than on
// one coordinator's prefix so the key travels with whoever owns heavy buying; the keys are sorted
// so that a container carrying two prefixes resolves the same way on every tick.
func launchCapValue(cfg map[string]interface{}) (int, bool) {
	keys := make([]string, 0, 1)
	for key := range cfg {
		if strings.HasSuffix(key, launchHeavyCapSuffix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if v, ok := configInt(cfg[key]); ok {
			return v, true
		}
	}
	return 0, false
}

// warnUndeclaredHeavyBuyer announces that the cap was resolved off a container no declaration
// claims — heavy buying has changed hands and hullbuy.HeavyBuyerContainers has not.
//
// LOUD, and only on this rung. A declared owner and an empty deployment are both expected
// configurations that must stay silent; this shape is the one that means the buyer and the
// withholder can drift apart with nothing in any gauge or heartbeat to say so.
func warnUndeclaredHeavyBuyer(ctx context.Context, playerID int, m *persistence.ContainerModel) {
	logging.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
		"Sensing heavy reserve: heavy_cap resolved from container %s of type %s, which no heavy-buyer declaration claims — the reservation is being sized against a cap nothing is known to enforce. Declare this container type as a heavy buyer.",
		m.ID, m.ContainerType,
	), map[string]interface{}{
		"action":         "sensing_heavy_cap_undeclared_buyer",
		"player_id":      playerID,
		"container_id":   m.ID,
		"container_type": m.ContainerType,
	})
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

// heavyCapKey is the BARE tune key a LIVE cap is stored under (survives a rebuild).
// launchHeavyCapSuffix ends the coordinator-prefixed LAUNCH key config.yaml is injected into
// (cleared and re-injected on every rebuild). A heavy buyer reads BOTH; so must this.
const (
	heavyCapKey          = "heavy_cap"
	launchHeavyCapSuffix = "_heavy_cap"
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
