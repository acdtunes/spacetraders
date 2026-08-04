package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
	"github.com/spf13/cobra"
)

// validMinSupplyLevels is the strict --min-supply whitelist. manufacturing.ParseSupplyLevel
// is deliberately lenient (unknown -> MODERATE) for scanned market data; CLI input is not.
var validMinSupplyLevels = []manufacturing.SupplyLevel{
	manufacturing.SupplyLevelAbundant,
	manufacturing.SupplyLevelHigh,
	manufacturing.SupplyLevelModerate,
	manufacturing.SupplyLevelLimited,
	manufacturing.SupplyLevelScarce,
}

// parseMinSupplyFlag strictly validates the --min-supply flag value against
// the real manufacturing.SupplyLevel enum. An empty string means unset and is
// always valid, preserving the default MODERATE sourcing floor unchanged.
func parseMinSupplyFlag(s string) (manufacturing.SupplyLevel, error) {
	if s == "" {
		return "", nil
	}
	for _, lvl := range validMinSupplyLevels {
		if manufacturing.SupplyLevel(s) == lvl {
			return lvl, nil
		}
	}
	return "", fmt.Errorf("invalid --min-supply value %q: must be one of ABUNDANT, HIGH, MODERATE, LIMITED, SCARCE", s)
}

// validGatingStrategies enumerates the acquisition-strategy values a per-good buy-gating override
// accepts. These mirror the services.AcquisitionStrategy constants
// (prefer-buy | prefer-fabricate | smart) documented on manufacturing.GoodGatingOverride; the CLI
// validates against them at the boundary so an unknown strategy is rejected before it can reach the
// persisted override map. Kept as a CLI-local allowlist (like validMinSupplyLevels) rather than
// importing internal/application/manufacturing/services for three string literals.
var validGatingStrategies = []string{"prefer-buy", "prefer-fabricate", "smart"}

// parseStrategyFlag strictly validates a per-good --strategy value against the known acquisition
// strategies. An empty string means unset and is always valid (no strategy override for the good).
func parseStrategyFlag(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	for _, v := range validGatingStrategies {
		if s == v {
			return s, nil
		}
	}
	return "", fmt.Errorf("invalid --strategy value %q: must be one of prefer-buy, prefer-fabricate, smart", s)
}

// clampPriceCeilingMult clamps a per-good price-ceiling multiplier into the guardrail range
// [0, manufacturing.MaxPriceCeilingMultiplier] and reports whether clamping changed it. RULINGS #4:
// the CLI can LOOSEN the ladder-chase ceiling for a stuck good but never DISABLE it, so a
// fat-finger value is pulled down to the domain hard cap at the boundary. The domain re-applies the
// same cap at use time (GoodGatingOverrides.PriceCeilingMultFor); this reuses that single constant
// rather than re-implementing the bound. A negative multiplier is nonsensical and clamps to 0
// (which the domain treats as "no override").
func clampPriceCeilingMult(mult float64) (float64, bool) {
	if mult < 0 {
		return 0, true
	}
	if mult > manufacturing.MaxPriceCeilingMultiplier {
		return manufacturing.MaxPriceCeilingMultiplier, true
	}
	return mult, false
}

// validateAndClampOverride validates one good's override knobs (strategy, min-supply tier) and
// clamps its price-ceiling multiplier to the domain guardrail. Empty strategy / min-supply are
// valid (no override on that dimension). It is the shared boundary check applied to every override
// the CLI feeds into the map, whether parsed from --good-override, --overrides JSON, or the live
// `construction override` verb.
func validateAndClampOverride(ov manufacturing.GoodGatingOverride) (manufacturing.GoodGatingOverride, error) {
	if _, err := parseStrategyFlag(ov.Strategy); err != nil {
		return manufacturing.GoodGatingOverride{}, err
	}
	if _, err := parseMinSupplyFlag(ov.MinSupply); err != nil {
		return manufacturing.GoodGatingOverride{}, err
	}
	clamped, _ := clampPriceCeilingMult(ov.PriceCeilingMult)
	ov.PriceCeilingMult = clamped
	return ov, nil
}

// parseGoodOverrideSpec parses one repeatable `--good-override GOOD:key=val[,key=val]` spec into a
// good symbol and a validated+clamped GoodGatingOverride. Keys are matched case-insensitively:
// minSupply, strategy, priceCeilingMult. Example:
//
//	FAB_MATS:minSupply=LIMITED,strategy=prefer-buy
func parseGoodOverrideSpec(spec string) (string, manufacturing.GoodGatingOverride, error) {
	good, kvList, found := strings.Cut(spec, ":")
	good = strings.TrimSpace(good)
	if !found || good == "" {
		return "", manufacturing.GoodGatingOverride{}, fmt.Errorf(
			"invalid --good-override %q: expected GOOD:key=val[,key=val] (e.g. FAB_MATS:minSupply=LIMITED,strategy=prefer-buy)", spec)
	}

	var ov manufacturing.GoodGatingOverride
	for _, pair := range strings.Split(kvList, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		key, val, ok := strings.Cut(pair, "=")
		if !ok {
			return "", manufacturing.GoodGatingOverride{}, fmt.Errorf("invalid --good-override %q: %q is not key=val", spec, pair)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch strings.ToLower(key) {
		case "minsupply":
			ov.MinSupply = val
		case "strategy":
			ov.Strategy = val
		case "priceceilingmult":
			f, err := strconv.ParseFloat(val, 64)
			if err != nil {
				return "", manufacturing.GoodGatingOverride{}, fmt.Errorf("invalid --good-override %q: priceCeilingMult %q is not a number: %w", spec, val, err)
			}
			ov.PriceCeilingMult = f
		default:
			return "", manufacturing.GoodGatingOverride{}, fmt.Errorf("invalid --good-override %q: unknown key %q (valid: minSupply, strategy, priceCeilingMult)", spec, key)
		}
	}

	validated, err := validateAndClampOverride(ov)
	if err != nil {
		return "", manufacturing.GoodGatingOverride{}, fmt.Errorf("invalid --good-override %q: %w", spec, err)
	}
	return good, validated, nil
}

// buildLaunchGoodOverrides merges repeatable --good-override specs and an optional --overrides JSON
// blob into a single validated GoodGatingOverrides map, ready to persist on the pipeline exactly
// like the global --min-supply floor. The JSON blob is applied first (bulk load), then
// each --good-override spec overrides its good (the explicit command-line form wins). Every entry
// is validated (strategy/tier rejected if unknown) and its price-ceiling multiplier clamped to the
// domain cap at the boundary. Returns nil when both inputs are empty, preserving today's
// global-default behaviour for every good.
func buildLaunchGoodOverrides(specs []string, jsonBlob string) (manufacturing.GoodGatingOverrides, error) {
	result := manufacturing.GoodGatingOverrides{}

	if strings.TrimSpace(jsonBlob) != "" {
		decoded, err := manufacturing.DecodeGoodGatingOverrides(jsonBlob)
		if err != nil {
			return nil, fmt.Errorf("invalid --overrides JSON: %w", err)
		}
		for good, ov := range decoded {
			validated, err := validateAndClampOverride(ov)
			if err != nil {
				return nil, fmt.Errorf("invalid --overrides entry for %q: %w", good, err)
			}
			result[good] = validated
		}
	}

	for _, spec := range specs {
		if strings.TrimSpace(spec) == "" {
			continue
		}
		good, ov, err := parseGoodOverrideSpec(spec)
		if err != nil {
			return nil, err
		}
		result[good] = ov
	}

	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}

// constructionOverrideMutator is the narrow daemon surface the `construction override` verb needs.
// By construction it exposes ONLY the ConstructionGoodOverride RPC — no pipeline restart/stop — so
// "no restart" is guaranteed by the surface this verb can reach, exactly as the goods-factory
// worker-cap verb guarantees it for the factory fan-out.
type constructionOverrideMutator interface {
	ConstructionGoodOverride(ctx context.Context, req *pb.ConstructionGoodOverrideRequest) (*pb.ConstructionGoodOverrideResponse, error)
}

// constructionOverrideFlags is the raw CLI flag state for a `construction override` call.
type constructionOverrideFlags struct {
	site             string
	good             string
	clear            bool
	minSupply        string
	priceCeilingMult float64
	multProvided     bool // whether --price-ceiling-mult was set on the command line

	// buyFloor/resumeFloor are the PIPELINE-WIDE gate DELIVERY fleet thresholds. They take no
	// --good and route to their own RPC; see anyFloorSet.
	buyFloor    string
	resumeFloor string
}

// anyKnobSet reports whether at least one tunable knob (--min-supply or
// --price-ceiling-mult) was provided on the command line.
func (f constructionOverrideFlags) anyKnobSet() bool {
	return f.minSupply != "" || f.multProvided
}

// anyFloorSet reports whether a PIPELINE-WIDE delivery floor was provided. It is
// deliberately separate from anyKnobSet: the floors and the per-good override are two
// different decisions on one verb, routed to two different RPCs (the per-good request
// requires a `good`, which pipeline-wide floors do not have).
func (f constructionOverrideFlags) anyFloorSet() bool {
	return f.buyFloor != "" || f.resumeFloor != ""
}

// buildConstructionOverrideRequest validates the `construction override` flags at the boundary and
// assembles the gRPC request. It enforces that --clear is exclusive of the knob flags and that a
// non-clear call sets at least one knob, validates the tier (rejecting unknown values), and clamps
// the price-ceiling multiplier to the domain hard cap (RULINGS #4 — the CLI never bypasses the
// guardrail). Only provided knobs become non-nil request fields, so an unset knob leaves that
// dimension of the good's override unchanged (tune one at a time). The bool return reports whether
// the multiplier was clamped, for an operator notice.
func buildConstructionOverrideRequest(f constructionOverrideFlags, playerIdent *PlayerIdentifier) (*pb.ConstructionGoodOverrideRequest, bool, error) {
	if f.site == "" {
		return nil, false, fmt.Errorf("--site is required (the construction site whose pipeline to tune)")
	}
	if f.good == "" {
		return nil, false, fmt.Errorf("--good is required (the material symbol to override)")
	}

	playerID, agentSymbol := playerPointers(playerIdent)
	var pid int32
	if playerID != nil {
		pid = *playerID
	}

	req := &pb.ConstructionGoodOverrideRequest{
		ConstructionSite: f.site,
		Good:             f.good,
		PlayerId:         pid,
		AgentSymbol:      agentSymbol,
	}

	if f.clear {
		if f.anyKnobSet() {
			return nil, false, fmt.Errorf("--clear removes the whole override for %s; it cannot be combined with --min-supply/--price-ceiling-mult", f.good)
		}
		req.Clear = true
		return req, false, nil
	}

	if !f.anyKnobSet() {
		return nil, false, fmt.Errorf("nothing to set for %s: pass at least one of --min-supply, --price-ceiling-mult (or --clear to remove the override)", f.good)
	}

	if f.minSupply != "" {
		if _, err := parseMinSupplyFlag(f.minSupply); err != nil {
			return nil, false, err
		}
		req.MinSupply = &f.minSupply
	}
	multClamped := false
	if f.multProvided {
		clamped, wasClamped := clampPriceCeilingMult(f.priceCeilingMult)
		req.PriceCeilingMult = &clamped
		multClamped = wasClamped
	}

	return req, multClamped, nil
}

// runConstructionOverride sends the override mutation to the daemon and formats the operator-facing
// result. The construction coordinator re-reads the persisted overrides on its next discovery pass,
// so the change is honored with no restart. A no-op (the value already matched) and a --clear are
// each reported honestly. multClamped triggers a guardrail notice.
func runConstructionOverride(ctx context.Context, client constructionOverrideMutator, req *pb.ConstructionGoodOverrideRequest, multClamped bool) (string, error) {
	resp, err := client.ConstructionGoodOverride(ctx, req)
	if err != nil {
		return "", fmt.Errorf("failed to set override for %s on %s: %w", req.Good, req.ConstructionSite, err)
	}

	var b strings.Builder
	switch {
	case resp.Cleared:
		fmt.Fprintf(&b, "✓ cleared the %s override on %s — it reverts to the pipeline's global default. The coordinator re-reads it live on its next discovery pass; no restart.\n", resp.Good, resp.ConstructionSite)
	case !resp.Changed:
		fmt.Fprintf(&b, "• %s override on %s is already set to that value — unchanged.\n", resp.Good, resp.ConstructionSite)
	default:
		fmt.Fprintf(&b, "✓ set the %s override on %s to {%s}. The coordinator re-reads it live on its next discovery pass; no restart.\n",
			resp.Good, resp.ConstructionSite, formatOverrideKnobs(resp))
	}
	if multClamped {
		fmt.Fprintf(&b, "  note: --price-ceiling-mult was clamped to the %.1fx domain cap (RULINGS #4 — the ceiling can be loosened but never disabled).\n", manufacturing.MaxPriceCeilingMultiplier)
	}
	return b.String(), nil
}

// formatOverrideKnobs renders the non-empty override dimensions of a response for the confirmation
// line, e.g. "minSupply=LIMITED, priceCeilingMult=2.00".
func formatOverrideKnobs(resp *pb.ConstructionGoodOverrideResponse) string {
	parts := make([]string, 0, 2)
	if resp.MinSupply != "" {
		parts = append(parts, "minSupply="+resp.MinSupply)
	}
	if resp.PriceCeilingMult > 0 {
		parts = append(parts, fmt.Sprintf("priceCeilingMult=%.2f", resp.PriceCeilingMult))
	}
	if len(parts) == 0 {
		return "global default"
	}
	return strings.Join(parts, ", ")
}

// --- pipeline-wide gate DELIVERY floors (the second RPC behind the same verb) --------------------

// constructionDeliveryFloorsMutator is the narrow daemon surface the delivery-floor half of
// `construction override` needs. By construction it exposes ONLY the floors RPC — no
// pipeline restart/stop — so "no restart" is guaranteed by the surface this verb can reach.
type constructionDeliveryFloorsMutator interface {
	ConstructionDeliveryFloors(ctx context.Context, req *pb.ConstructionDeliveryFloorsRequest) (*pb.ConstructionDeliveryFloorsResponse, error)
}

// validBuyFloors and validResumeFloors are the PER-FLAG accepted sets, and they are
// deliberately NOT the same set. Each floor excludes the ladder end that makes a strictly
// positive hysteresis gap inexpressible: nothing can sit strictly above ABUNDANT (so it is not
// a buy floor) and nothing can sit strictly below SCARCE (so it is not a resume floor). Each
// flag therefore advertises only what it actually accepts — listing all five on both would
// answer a rejected value with a menu whose obvious next pick is rejected too. Both are carved
// out of validMinSupplyLevels, the full ladder, and a test pins them to it so neither can drift.
var (
	validBuyFloors = []manufacturing.SupplyLevel{
		manufacturing.SupplyLevelHigh,
		manufacturing.SupplyLevelModerate,
		manufacturing.SupplyLevelLimited,
		manufacturing.SupplyLevelScarce,
	}
	validResumeFloors = []manufacturing.SupplyLevel{
		manufacturing.SupplyLevelAbundant,
		manufacturing.SupplyLevelHigh,
		manufacturing.SupplyLevelModerate,
		manufacturing.SupplyLevelLimited,
	}
)

// supplyLevelList renders a vocabulary for an error message, e.g. "HIGH, MODERATE, LIMITED, SCARCE".
func supplyLevelList(levels []manufacturing.SupplyLevel) string {
	parts := make([]string, 0, len(levels))
	for _, level := range levels {
		parts = append(parts, string(level))
	}
	return strings.Join(parts, ", ")
}

// validateDeliveryFloorFlag strictly validates one pipeline-wide floor value against the set
// that FLAG accepts, naming the flag and listing only its own vocabulary. An empty value is
// unset and always valid — it leaves that dimension unchanged.
func validateDeliveryFloorFlag(flagName, value string, allowed []manufacturing.SupplyLevel) error {
	if value == "" {
		return nil
	}
	for _, level := range allowed {
		if manufacturing.SupplyLevel(value) == level {
			return nil
		}
	}
	return fmt.Errorf("invalid --%s value %q: must be one of %s", flagName, value, supplyLevelList(allowed))
}

// buildConstructionDeliveryFloorsRequest validates the pipeline-wide floor flags at the
// boundary and assembles the gRPC request.
//
// --good is REJECTED here rather than ignored. These floors are pipeline-wide, so a
// supplied good does nothing — and an operator who typed it believes it did something,
// which is exactly the class of silent no-op this design exists to remove.
//
// Tiers are validated strictly. shared.ParseSupplyLevel is deliberately lenient (unknown ->
// MODERATE) because it parses scanned market data; operator input is not, or a typo becomes
// a floor nobody chose.
//
// A resume floor that is not STRICTLY ABOVE the buy floor is rejected, naming both values.
// The domain would silently raise it, but silently correcting an operator leaves them with
// a wrong mental model of a knob they are actively tuning.
func buildConstructionDeliveryFloorsRequest(f constructionOverrideFlags, playerIdent *PlayerIdentifier) (*pb.ConstructionDeliveryFloorsRequest, error) {
	if f.site == "" {
		return nil, fmt.Errorf("--site is required (the construction site whose pipeline to tune)")
	}
	if f.good != "" {
		return nil, fmt.Errorf("--good cannot be combined with --buy-floor/--resume-floor: the delivery floors are PIPELINE-WIDE, not per-good (use --min-supply/--price-ceiling-mult for a per-good override)")
	}
	if f.clear {
		return nil, fmt.Errorf("--clear removes a per-good override; to reset the delivery floors, set them explicitly (--buy-floor MODERATE --resume-floor HIGH, the armed defaults)")
	}
	// The two LADDER-END exclusions are checked before the vocabulary check so a real supply
	// level that simply cannot work on this flag gets an explanation rather than a bare menu:
	// that is a different operator mistake from a typo, and telling them apart is the
	// difference between one retry and two. Neither is reachable by the pairwise check below,
	// which needs BOTH flags — one-sided, each would otherwise die at the daemon with an error
	// naming the flag the operator never typed.
	//
	// ABUNDANT is the top of the ladder: no resume floor can sit strictly above it, so the
	// hysteresis gap collapses to zero (the domain's nextLevelAbove(ABUNDANT) returns ABUNDANT).
	if manufacturing.SupplyLevel(f.buyFloor) == manufacturing.SupplyLevelAbundant {
		return nil, fmt.Errorf("--buy-floor ABUNDANT is not settable: ABUNDANT is the top of the supply ladder, so no --resume-floor can sit strictly above it and the hysteresis gap would collapse to zero (HIGH is the strictest usable buy floor)")
	}
	// SCARCE is the bottom: no buy floor can sit strictly below it, so every buy floor
	// collapses the gap against it.
	if manufacturing.SupplyLevel(f.resumeFloor) == manufacturing.SupplyLevelScarce {
		return nil, fmt.Errorf("--resume-floor SCARCE is not settable: SCARCE is the bottom of the supply ladder, so no --buy-floor can sit strictly below it and the hysteresis gap would collapse to zero (LIMITED is the most permissive usable resume floor)")
	}

	if err := validateDeliveryFloorFlag("buy-floor", f.buyFloor, validBuyFloors); err != nil {
		return nil, err
	}
	if err := validateDeliveryFloorFlag("resume-floor", f.resumeFloor, validResumeFloors); err != nil {
		return nil, err
	}

	if f.buyFloor != "" && f.resumeFloor != "" {
		if manufacturing.SupplyLevel(f.resumeFloor).Order() <= manufacturing.SupplyLevel(f.buyFloor).Order() {
			return nil, fmt.Errorf("--resume-floor %s must be strictly above --buy-floor %s: an equal or lower resume floor collapses the hysteresis into the single threshold that chatters at the supply boundary", f.resumeFloor, f.buyFloor)
		}
	}

	playerID, agentSymbol := playerPointers(playerIdent)
	req := &pb.ConstructionDeliveryFloorsRequest{
		ConstructionSite: f.site,
		PlayerId:         playerID,
		AgentSymbol:      agentSymbol,
	}
	// Only PROVIDED floors become non-nil, so an unset one leaves that dimension unchanged
	// and the gap can be tuned from either end.
	if f.buyFloor != "" {
		req.BuyFloor = &f.buyFloor
	}
	if f.resumeFloor != "" {
		req.ResumeFloor = &f.resumeFloor
	}
	return req, nil
}

// annotateFloorProvenance renders one RESOLVED floor and marks it when the daemon resolved it
// from the ARMED DEFAULT rather than reading a value explicitly set on the row. The value alone
// cannot carry that: a bare "resume=HIGH" is indistinguishable from a HIGH a predecessor pinned,
// so after the most likely first command — a one-sided tune of a fresh pipeline — the operator
// cannot tell which half of the pair they actually control. The annotation is the only thing
// that says so, and it is deliberately about PROVENANCE, not blankness: the resolved value is
// always present (the daemon never returns the row's raw unset value), so this never stands in
// for a missing floor.
func annotateFloorProvenance(floor string, isDefault bool) string {
	if isDefault {
		return floor + " (default)"
	}
	return floor
}

// runConstructionDeliveryFloors sends the floor tune and formats the operator-facing result.
// It reports the RESOLVED floors now in force — what the knob became, not what was sent — marks
// whichever side came from the armed default, and states the liveness, which is the operator's
// only evidence the tune took. BOTH message paths annotate: a no-op report of an untouched pair
// is read for exactly the same "which of these did I set?" question as a successful tune.
func runConstructionDeliveryFloors(ctx context.Context, client constructionDeliveryFloorsMutator, req *pb.ConstructionDeliveryFloorsRequest) (string, error) {
	resp, err := client.ConstructionDeliveryFloors(ctx, req)
	if err != nil {
		return "", fmt.Errorf("failed to set the delivery floors on %s: %w", req.ConstructionSite, err)
	}
	buy := annotateFloorProvenance(resp.BuyFloor, resp.BuyFloorIsDefault)
	resume := annotateFloorProvenance(resp.ResumeFloor, resp.ResumeFloorIsDefault)
	if !resp.Changed {
		return fmt.Sprintf("• %s delivery floors are already buy=%s / resume=%s — unchanged.\n",
			resp.ConstructionSite, buy, resume), nil
	}
	return fmt.Sprintf("✓ set the %s delivery floors to buy=%s / resume=%s. The drain re-reads them off the pipeline row on every leg, so this takes effect on the next leg; no restart, and it survives a daemon bounce.\n",
		resp.ConstructionSite, buy, resume), nil
}

// newConstructionOverrideCommand creates the `construction override` subcommand — live per-good
// tuning of the buy-gating override map on a RUNNING construction pipeline, the construction
// analogue of `goods factory workers`. No restart: the coordinator re-reads the persisted
// overrides on its next discovery pass, and the value survives a daemon bounce (RULINGS #2).
func newConstructionOverrideCommand() *cobra.Command {
	var f constructionOverrideFlags

	cmd := &cobra.Command{
		Use:   "override",
		Short: "Set or clear a per-good buy-gating override on a running construction pipeline (no restart)",
		Long: `Set or clear a PER-GOOD buy-gating override on a RUNNING construction pipeline, live.

This tunes the sp-sdyo override map for ONE material without restarting the pipeline:
the construction coordinator re-reads the persisted overrides on its next discovery
pass and converges. The override is persisted on the pipeline, so it also survives a
daemon restart and applies to deferred-material recovery.

Knobs (set only the ones you want to change; the rest stay as they are):
  --min-supply         EXPORT sourcing floor for this good (ABUNDANT|HIGH|MODERATE|LIMITED|SCARCE)
  --price-ceiling-mult ladder-chase input-price ceiling multiplier (clamped to the domain cap)

--clear removes the good's override entirely, reverting it to the pipeline's global default.
A non-overridden good is always byte-identical to the global default.

Pipeline-wide gate DELIVERY fleet knobs (no --good; these apply to the whole pipeline):
  --buy-floor          buy while the terminal factory's supply is AT OR ABOVE this level, default
                       MODERATE (HIGH|MODERATE|LIMITED|SCARCE — not ABUNDANT: nothing sits strictly above it)
  --resume-floor       once paused, resume only when supply recovers to this level, default
                       HIGH (ABUNDANT|HIGH|MODERATE|LIMITED — not SCARCE: nothing sits strictly below it)

The two floors take DIFFERENT vocabularies, and they are not the full --min-supply ladder:
each excludes the ladder end that makes a strictly positive gap inexpressible.

Two thresholds, not one: a single threshold chatters at the boundary — pause, one unit
regenerates, resume, immediately deplete. --resume-floor must be strictly above --buy-floor.
These are TUNABLES, not feature flags: they ship armed at MODERATE/HIGH and adjust a value
in a path that always runs. They are distinct from --min-supply, which is the pipeline's
READY-admission floor — a different decision at a different stage.

Examples:
  spacetraders construction override --site X1-VB74-I55 --good FAB_MATS --min-supply LIMITED
  spacetraders construction override --site X1-VB74-I55 --good FAB_MATS --price-ceiling-mult 2.0
  spacetraders construction override --site X1-VB74-I55 --good FAB_MATS --clear
  spacetraders construction override --site X1-VB74-I55 --buy-floor LIMITED --resume-floor MODERATE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			f.multProvided = cmd.Flags().Changed("price-ceiling-mult")

			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			// ONE VERB, TWO RPCs. The pipeline-wide floors and the per-good override are
			// different decisions on different scopes — the per-good request requires a `good`,
			// which the floors do not have — so the verb dispatches on which flags were set
			// rather than forcing them into one request shape.
			if f.anyFloorSet() {
				return runFloorsDispatch(f, playerIdent)
			}

			req, multClamped, err := buildConstructionOverrideRequest(f, playerIdent)
			if err != nil {
				return err
			}

			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			msg, err := runConstructionOverride(ctx, client, req, multClamped)
			if err != nil {
				return err
			}
			fmt.Print(msg)
			return nil
		},
	}

	cmd.Flags().StringVar(&f.site, "site", "", "Construction site whose running pipeline to tune (required)")
	cmd.Flags().StringVar(&f.good, "good", "", "Material symbol to override (required)")
	cmd.Flags().BoolVar(&f.clear, "clear", false, "Remove the good's override, reverting it to the pipeline's global default")
	cmd.Flags().StringVar(&f.minSupply, "min-supply", "", "Per-good EXPORT sourcing floor (ABUNDANT, HIGH, MODERATE, LIMITED, SCARCE)")
	cmd.Flags().Float64Var(&f.priceCeilingMult, "price-ceiling-mult", 0, "Per-good ladder-chase input-price ceiling multiplier (clamped to the domain cap)")
	// Each floor advertises ONLY its own vocabulary, and that vocabulary is DERIVED from the
	// same list the validator rejects against — not retyped beside it. A hardcoded help string
	// next to a list-driven validator is the very drift this fixed: it would be correct today
	// and silently wrong the first time a level moves, leaving the operator's only route to the
	// truth a refusal they had to trigger. The two sets already differ, so the invariant is live
	// from day one rather than latent. The exclusion CLAUSES stay prose: they explain WHY a
	// ladder end is absent, and there is nothing to derive an explanation from.
	cmd.Flags().StringVar(&f.buyFloor, "buy-floor", "", fmt.Sprintf(
		"Gate DELIVERY fleet: buy while the terminal factory's supply is at or above this level (%s — not ABUNDANT: nothing sits strictly above it; pipeline-wide; default MODERATE)",
		supplyLevelList(validBuyFloors)))
	cmd.Flags().StringVar(&f.resumeFloor, "resume-floor", "", fmt.Sprintf(
		"Gate DELIVERY fleet: once paused, resume only when supply recovers to this level (%s — not SCARCE: nothing sits strictly below it; pipeline-wide; default HIGH, must be above --buy-floor)",
		supplyLevelList(validResumeFloors)))

	return cmd
}

// runFloorsDispatch is the delivery-floor half of the `construction override` verb: validate at
// the boundary, dial, send, print. Split out of RunE so the two RPCs the verb fronts read as two
// paths rather than one nested branch.
func runFloorsDispatch(f constructionOverrideFlags, playerIdent *PlayerIdentifier) error {
	req, err := buildConstructionDeliveryFloorsRequest(f, playerIdent)
	if err != nil {
		return err
	}

	client, err := connectDaemon()
	if err != nil {
		return err
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	msg, err := runConstructionDeliveryFloors(ctx, client, req)
	if err != nil {
		return err
	}
	fmt.Print(msg)
	return nil
}

// --- live `construction workers` verb (sp-duljg) -------------------------------------------------
