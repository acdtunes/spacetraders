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
}

// anyKnobSet reports whether at least one tunable knob (--min-supply or
// --price-ceiling-mult) was provided on the command line.
func (f constructionOverrideFlags) anyKnobSet() bool {
	return f.minSupply != "" || f.multProvided
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

Examples:
  spacetraders construction override --site X1-VB74-I55 --good FAB_MATS --min-supply LIMITED
  spacetraders construction override --site X1-VB74-I55 --good FAB_MATS --price-ceiling-mult 2.0
  spacetraders construction override --site X1-VB74-I55 --good FAB_MATS --clear`,
		RunE: func(cmd *cobra.Command, args []string) error {
			f.multProvided = cmd.Flags().Changed("price-ceiling-mult")

			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
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

	return cmd
}

// --- live `construction workers` verb (sp-duljg) -------------------------------------------------
