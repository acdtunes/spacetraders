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

// validMinSupplyLevels enumerates the actual manufacturing.SupplyLevel values
// accepted by --min-supply (sp-ezz9). Kept separate from
// manufacturing.ParseSupplyLevel, which is intentionally lenient (it defaults
// unrecognized strings to MODERATE for parsing scanned market data) - CLI
// input instead gets strict validation with a clear rejection error.
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
// accepts (sp-pdb3 / sp-sdyo). These mirror the services.AcquisitionStrategy constants
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
// like the global --min-supply floor (sp-ezz9). The JSON blob is applied first (bulk load), then
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

// NewConstructionCommand creates the construction command with subcommands
func NewConstructionCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "construction",
		Short: "Manage construction site supply operations",
		Long: `Manage construction site supply operations.

The construction pipeline system delivers materials to construction sites
(e.g., jump gates under construction). It automatically discovers required
materials and creates tasks to produce/acquire and deliver them.

Examples:
  spacetraders construction start X1-FB5-I61 --player-id 1
  spacetraders construction status X1-FB5-I61 --player-id 1`,
	}

	// Add subcommands
	cmd.AddCommand(newConstructionStartCommand())
	cmd.AddCommand(newConstructionStatusCommand())
	cmd.AddCommand(newConstructionStopCommand())
	cmd.AddCommand(newConstructionOverrideCommand())

	return cmd
}

// newConstructionStartCommand creates the construction start subcommand
func newConstructionStartCommand() *cobra.Command {
	var supplyChainDepth int
	var maxWorkers int
	var systemSymbol string
	var minSupply string
	var goodOverrideSpecs []string
	var overridesJSON string

	cmd := &cobra.Command{
		Use:   "start <construction-site>",
		Short: "Start a pipeline to supply materials to a construction site",
		Long: `Start a pipeline to supply materials to a construction site.

The pipeline will:
- Fetch construction site requirements from the API
- Create tasks for each required material
- Produce/acquire materials based on supply chain depth
- Deliver materials to the construction site

Supply chain depth controls how much to produce:
  0 - Full production (mine/produce everything from scratch)
  1 - Buy raw materials only (produce intermediates)
  2 - Buy intermediate goods (only final assembly)
  3 - Buy final product (no production, just delivery)

--min-supply lowers the floor the sourcing locator will buy EXPORT
materials down to (default floor: MODERATE). For example, --min-supply
SCARCE lets the pipeline source from a market even when its supply has
dropped all the way to SCARCE, instead of waiting for it to recover to
MODERATE or better. Only ABUNDANT, HIGH, MODERATE, LIMITED, and SCARCE
are accepted. Left unset, behavior is unchanged from the MODERATE default.
The floor is persisted on the pipeline, so it also applies when resuming
an existing, in-progress pipeline and when recovering materials that were
deferred because no market met the floor at the time.

--good-override sets a PER-GOOD buy-gating override (sp-sdyo) so ONE
bottleneck good can be loosened while every other material keeps the
global floor above. It is repeatable and takes GOOD:key=val[,key=val]
with keys minSupply, strategy (prefer-buy|prefer-fabricate|smart) and
priceCeilingMult. --overrides takes the same map as a JSON blob. The
overrides are persisted on the pipeline exactly like --min-supply, so
they survive a restart and a resume. An unknown strategy/tier is
rejected and priceCeilingMult is clamped to the domain cap.

The pipeline is IDEMPOTENT - running this command again will resume
an existing pipeline instead of creating a new one.

Examples:
  spacetraders construction start X1-FB5-I61 --player-id 1
  spacetraders construction start X1-FB5-I61 --system X1-FB5 --depth 3 --player-id 1
  spacetraders construction start X1-FB5-I61 --min-supply SCARCE --player-id 1
  spacetraders construction start X1-VB74-I55 --good-override FAB_MATS:minSupply=LIMITED,strategy=prefer-buy --player-id 1`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate --min-supply before touching any infrastructure (mirrors
			// newShipBuyCommand's flag-validation-first pattern).
			minSupplyLevel, err := parseMinSupplyFlag(minSupply)
			if err != nil {
				return err
			}

			// Build + validate the optional per-good buy-gating overrides (sp-sdyo values,
			// sp-pdb3 launch surface) before touching infrastructure — same flag-validation-first
			// pattern. Empty inputs yield a nil map, preserving the global default for every good.
			// Each entry's strategy/tier is validated and its price-ceiling multiplier clamped to
			// the domain cap here at the boundary (RULINGS #4).
			goodOverrides, err := buildLaunchGoodOverrides(goodOverrideSpecs, overridesJSON)
			if err != nil {
				return err
			}

			constructionSite := args[0]

			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			// Create gRPC client
			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			// Start construction pipeline
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			// Convert systemSymbol to pointer (nil if empty)
			var systemSymbolPtr *string
			if systemSymbol != "" {
				systemSymbolPtr = &systemSymbol
			}

			// Convert minSupply to pointer (nil if unset)
			var minSupplyPtr *string
			if minSupplyLevel != "" {
				s := string(minSupplyLevel)
				minSupplyPtr = &s
			}

			// Encode the per-good overrides for the wire; nil when there are none so the pipeline
			// keeps today's global-default behaviour for every good.
			var goodOverridesPtr *string
			if len(goodOverrides) > 0 {
				encoded := goodOverrides.Encode()
				goodOverridesPtr = &encoded
			}

			result, err := client.StartConstructionPipeline(
				ctx,
				constructionSite,
				int32(playerIdent.PlayerID),
				&playerIdent.AgentSymbol,
				int32(supplyChainDepth),
				int32(maxWorkers),
				systemSymbolPtr,
				minSupplyPtr,
				goodOverridesPtr,
			)
			if err != nil {
				return fmt.Errorf("failed to start construction pipeline: %w", err)
			}

			// Display result
			if result.IsResumed {
				fmt.Println("Resumed existing construction pipeline")
			} else {
				fmt.Println("Started new construction pipeline")
			}
			fmt.Printf("  Pipeline ID: %s\n", result.PipelineID)
			fmt.Printf("  Construction Site: %s\n", result.ConstructionSite)
			fmt.Printf("  Task Count: %d\n", result.TaskCount)
			fmt.Printf("  Status: %s\n", result.Status)

			if len(result.Materials) > 0 {
				fmt.Println("\nMaterials to deliver:")
				for _, mat := range result.Materials {
					fmt.Printf("  - %s: %d/%d (%.1f%% complete)\n",
						mat.TradeSymbol,
						mat.Fulfilled,
						mat.Required,
						mat.Progress,
					)
				}
			}

			// sp-560b: name every material that couldn't be sourced this pass,
			// instead of a generic "no market with good supply" message. sp-ooba:
			// planning is never all-or-nothing, so this can be non-empty even
			// though the pipeline above started successfully - it's the gap the
			// captain needs to go source manually.
			if len(result.DeferredMaterials) > 0 {
				fmt.Println("\nDeferred (no source found yet):")
				for _, mat := range result.DeferredMaterials {
					fmt.Printf("  - %s\n", mat)
				}
			}

			if result.Message != "" {
				fmt.Printf("\n%s\n", result.Message)
			}

			return nil
		},
	}

	cmd.Flags().IntVar(&supplyChainDepth, "depth", 3, "Supply chain depth (0=full, 1=raw, 2=intermediate, 3=buy final)")
	cmd.Flags().IntVar(&maxWorkers, "max-workers", 5, "Maximum parallel workers")
	cmd.Flags().StringVar(&systemSymbol, "system", "", "System symbol for market lookups (defaults to deriving from construction site)")
	cmd.Flags().StringVar(&minSupply, "min-supply", "", "Lower the EXPORT sourcing floor below the default MODERATE (one of ABUNDANT, HIGH, MODERATE, LIMITED, SCARCE)")
	cmd.Flags().StringArrayVar(&goodOverrideSpecs, "good-override", nil, "Per-good buy-gating override (repeatable), e.g. FAB_MATS:minSupply=LIMITED,strategy=prefer-buy,priceCeilingMult=2.0 — loosens ONE good; others keep the global floor (sp-sdyo)")
	cmd.Flags().StringVar(&overridesJSON, "overrides", "", `Per-good buy-gating overrides as a JSON map, e.g. '{"FAB_MATS":{"minSupply":"LIMITED","strategy":"prefer-buy"}}' (alternative to repeated --good-override)`)

	return cmd
}

// newConstructionStatusCommand creates the construction status subcommand
func newConstructionStatusCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status <construction-site>",
		Short: "Show status of a construction site and any active pipeline",
		Long: `Show status of a construction site and any active pipeline.

This command shows:
- Construction site completion status
- Required materials and their delivery progress
- Active pipeline status (if any)

Examples:
  spacetraders construction status X1-FB5-I61 --player-id 1`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			constructionSite := args[0]

			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			// Create gRPC client
			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			// Get construction status
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			result, err := client.GetConstructionStatus(
				ctx,
				constructionSite,
				int32(playerIdent.PlayerID),
				&playerIdent.AgentSymbol,
			)
			if err != nil {
				return fmt.Errorf("failed to get construction status: %w", err)
			}

			// Display result
			fmt.Printf("Construction Site: %s\n", result.ConstructionSite)
			if result.IsComplete {
				fmt.Println("Status: COMPLETE")
			} else {
				fmt.Printf("Progress: %.1f%%\n", result.Progress)
			}

			if len(result.Materials) > 0 {
				fmt.Println("\nMaterials:")
				for _, mat := range result.Materials {
					status := ""
					if mat.Remaining == 0 {
						status = " [COMPLETE]"
					}
					fmt.Printf("  - %s: %d/%d (%.1f%%)%s\n",
						mat.TradeSymbol,
						mat.Fulfilled,
						mat.Required,
						mat.Progress,
						status,
					)
				}
			}

			// Pipeline info (if any)
			if result.PipelineID != nil && *result.PipelineID != "" {
				fmt.Println("\nActive Pipeline:")
				fmt.Printf("  ID: %s\n", *result.PipelineID)
				if result.PipelineStatus != nil {
					fmt.Printf("  Status: %s\n", *result.PipelineStatus)
				}
				if result.PipelineProgress != nil {
					fmt.Printf("  Progress: %.1f%%\n", *result.PipelineProgress)
				}
			}

			return nil
		},
	}

	return cmd
}

// newConstructionStopCommand creates the construction stop subcommand
func newConstructionStopCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop <construction-site>",
		Short: "Stop the active construction pipeline for a site",
		Long: `Stop the active construction pipeline for a construction site.

This command cancels the pipeline (so it stops spawning new tasks) and
cancels any not-yet-started tasks (PENDING/READY/ASSIGNED). Tasks already
EXECUTING are left to finish or fail naturally. Ships claimed by a
now-cancelled task are released so they re-enter fleet discovery.

Returns a clear error if there is no active construction pipeline for the
site (never started, or already stopped).

Examples:
  spacetraders construction stop X1-FB5-I61 --player-id 1`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			constructionSite := args[0]

			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			// Create gRPC client
			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			// Stop construction pipeline
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			result, err := client.StopConstructionPipeline(
				ctx,
				constructionSite,
				int32(playerIdent.PlayerID),
				&playerIdent.AgentSymbol,
			)
			if err != nil {
				return fmt.Errorf("failed to stop construction pipeline: %w", err)
			}

			fmt.Println("Stopped construction pipeline")
			fmt.Printf("  Pipeline ID: %s\n", result.PipelineID)
			fmt.Printf("  Construction Site: %s\n", result.ConstructionSite)
			fmt.Printf("  Status: %s\n", result.Status)
			fmt.Printf("  Tasks Cancelled: %d\n", result.TasksCancelled)

			if result.Message != "" {
				fmt.Printf("\n%s\n", result.Message)
			}

			return nil
		},
	}

	return cmd
}

// --- sp-pdb3: live `construction override` verb --------------------------------------------------

// constructionOverrideMutator is the narrow daemon surface the `construction override` verb needs.
// By construction it exposes ONLY the ConstructionGoodOverride RPC — no pipeline restart/stop — so
// "no restart" is guaranteed by the surface this verb can reach, exactly as the goods-factory
// worker-cap verb (sp-ev0n) guarantees it for the factory fan-out.
type constructionOverrideMutator interface {
	ConstructionGoodOverride(ctx context.Context, req *pb.ConstructionGoodOverrideRequest) (*pb.ConstructionGoodOverrideResponse, error)
}

// constructionOverrideFlags is the raw CLI flag state for a `construction override` call.
type constructionOverrideFlags struct {
	site             string
	good             string
	clear            bool
	minSupply        string
	strategy         string
	priceCeilingMult float64
	multProvided     bool // whether --price-ceiling-mult was set on the command line
}

// buildConstructionOverrideRequest validates the `construction override` flags at the boundary and
// assembles the gRPC request. It enforces that --clear is exclusive of the knob flags and that a
// non-clear call sets at least one knob, validates the strategy/tier (rejecting unknown values),
// and clamps the price-ceiling multiplier to the domain hard cap (RULINGS #4 — the CLI never
// bypasses the guardrail). Only provided knobs become non-nil request fields, so an unset knob
// leaves that dimension of the good's override unchanged (tune one at a time). The bool return
// reports whether the multiplier was clamped, for an operator notice.
func buildConstructionOverrideRequest(f constructionOverrideFlags, playerID int32, agentSymbol *string) (*pb.ConstructionGoodOverrideRequest, bool, error) {
	if f.site == "" {
		return nil, false, fmt.Errorf("--site is required (the construction site whose pipeline to tune)")
	}
	if f.good == "" {
		return nil, false, fmt.Errorf("--good is required (the material symbol to override)")
	}

	req := &pb.ConstructionGoodOverrideRequest{
		ConstructionSite: f.site,
		Good:             f.good,
		PlayerId:         playerID,
		AgentSymbol:      agentSymbol,
	}

	if f.clear {
		if f.minSupply != "" || f.strategy != "" || f.multProvided {
			return nil, false, fmt.Errorf("--clear removes the whole override for %s; it cannot be combined with --min-supply/--strategy/--price-ceiling-mult", f.good)
		}
		req.Clear = true
		return req, false, nil
	}

	if f.minSupply == "" && f.strategy == "" && !f.multProvided {
		return nil, false, fmt.Errorf("nothing to set for %s: pass at least one of --min-supply, --strategy, --price-ceiling-mult (or --clear to remove the override)", f.good)
	}

	if f.strategy != "" {
		if _, err := parseStrategyFlag(f.strategy); err != nil {
			return nil, false, err
		}
		req.Strategy = &f.strategy
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
// line, e.g. "minSupply=LIMITED, strategy=prefer-buy".
func formatOverrideKnobs(resp *pb.ConstructionGoodOverrideResponse) string {
	parts := make([]string, 0, 3)
	if resp.MinSupply != "" {
		parts = append(parts, "minSupply="+resp.MinSupply)
	}
	if resp.Strategy != "" {
		parts = append(parts, "strategy="+resp.Strategy)
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
// tuning of the sp-sdyo buy-gating override map on a RUNNING construction pipeline (sp-pdb3), the
// construction analogue of `goods factory workers`. No restart: the coordinator re-reads the
// persisted overrides on its next discovery pass, and the value survives a daemon bounce (RULINGS #2).
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
  --strategy           acquisition strategy (prefer-buy|prefer-fabricate|smart)
  --price-ceiling-mult ladder-chase input-price ceiling multiplier (clamped to the domain cap)

--clear removes the good's override entirely, reverting it to the pipeline's global default.
A non-overridden good is always byte-identical to the global default.

Examples:
  spacetraders construction override --site X1-VB74-I55 --good FAB_MATS --min-supply LIMITED --strategy prefer-buy
  spacetraders construction override --site X1-VB74-I55 --good FAB_MATS --price-ceiling-mult 2.0
  spacetraders construction override --site X1-VB74-I55 --good FAB_MATS --clear`,
		RunE: func(cmd *cobra.Command, args []string) error {
			f.multProvided = cmd.Flags().Changed("price-ceiling-mult")

			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}
			playerID, agentSymbol := playerPointers(playerIdent)
			var pid int32
			if playerID != nil {
				pid = *playerID
			}

			req, multClamped, err := buildConstructionOverrideRequest(f, pid, agentSymbol)
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
	cmd.Flags().StringVar(&f.strategy, "strategy", "", "Per-good acquisition strategy (prefer-buy, prefer-fabricate, smart)")
	cmd.Flags().Float64Var(&f.priceCeilingMult, "price-ceiling-mult", 0, "Per-good ladder-chase input-price ceiling multiplier (clamped to the domain cap)")

	return cmd
}
