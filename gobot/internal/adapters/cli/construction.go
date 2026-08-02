package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/spf13/cobra"
)

// Launch-time pipeline shape, fixed rather than flagged: workers are tuned live via
// `construction workers --count`, and "smart" acquisition never varies the depth.
const (
	// 3 = buy the final product, produce nothing.
	constructionSupplyChainDepth = 3
	// 0 means "not provided": a new pipeline takes the domain default, a resumed one
	// keeps its live-tuned cap.
	constructionMaxWorkers = 0
)

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

	cmd.AddCommand(newConstructionStartCommand())
	cmd.AddCommand(newConstructionStatusCommand())
	cmd.AddCommand(newConstructionStopCommand())
	cmd.AddCommand(newConstructionOverrideCommand())
	cmd.AddCommand(newConstructionWorkersCommand())

	return cmd
}

// newConstructionStartCommand creates the construction start subcommand
func newConstructionStartCommand() *cobra.Command {
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
- Buy the final product (no production) and deliver it to the site

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
  spacetraders construction start X1-FB5-I61 --system X1-FB5 --player-id 1
  spacetraders construction start X1-FB5-I61 --min-supply SCARCE --player-id 1
  spacetraders construction start X1-VB74-I55 --good-override FAB_MATS:minSupply=LIMITED,strategy=prefer-buy --player-id 1`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validated before any infrastructure is touched; each override's price-ceiling
			// multiplier is clamped to the domain cap here at the boundary (RULINGS #4).
			minSupplyLevel, err := parseMinSupplyFlag(minSupply)
			if err != nil {
				return err
			}
			goodOverrides, err := buildLaunchGoodOverrides(goodOverrideSpecs, overridesJSON)
			if err != nil {
				return err
			}

			return runConstructionStart(args[0], systemSymbol, minSupplyLevel, goodOverrides)
		},
	}

	cmd.Flags().StringVar(&systemSymbol, "system", "", "System symbol for market lookups (defaults to deriving from construction site)")
	cmd.Flags().StringVar(&minSupply, "min-supply", "", "Lower the EXPORT sourcing floor below the default MODERATE (one of ABUNDANT, HIGH, MODERATE, LIMITED, SCARCE)")
	cmd.Flags().StringArrayVar(&goodOverrideSpecs, "good-override", nil, "Per-good buy-gating override (repeatable), e.g. FAB_MATS:minSupply=LIMITED,strategy=prefer-buy,priceCeilingMult=2.0 — loosens ONE good; others keep the global floor (sp-sdyo)")
	cmd.Flags().StringVar(&overridesJSON, "overrides", "", `Per-good buy-gating overrides as a JSON map, e.g. '{"FAB_MATS":{"minSupply":"LIMITED","strategy":"prefer-buy"}}' (alternative to repeated --good-override)`)

	return cmd
}

func runConstructionStart(constructionSite, systemSymbol string, minSupplyLevel manufacturing.SupplyLevel, goodOverrides manufacturing.GoodGatingOverrides) error {
	playerIdent, err := resolvePlayerIdentifier()
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

	var systemSymbolPtr *string
	if systemSymbol != "" {
		systemSymbolPtr = &systemSymbol
	}

	var minSupplyPtr *string
	if minSupplyLevel != "" {
		s := string(minSupplyLevel)
		minSupplyPtr = &s
	}

	// nil when there are none so the pipeline keeps today's global-default
	// behaviour for every good.
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
		int32(constructionSupplyChainDepth),
		int32(constructionMaxWorkers),
		systemSymbolPtr,
		minSupplyPtr,
		goodOverridesPtr,
	)
	if err != nil {
		return fmt.Errorf("failed to start construction pipeline: %w", err)
	}

	printConstructionStartResult(result)
	return nil
}

func printConstructionStartResult(result *StartConstructionPipelineResponse) {
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

	// Planning is never all-or-nothing, so this can be non-empty even though the pipeline
	// started successfully — it is the gap the captain has to go source manually.
	if len(result.DeferredMaterials) > 0 {
		fmt.Println("\nDeferred (no source found yet):")
		for _, mat := range result.DeferredMaterials {
			fmt.Printf("  - %s\n", mat)
		}
	}

	if result.Message != "" {
		fmt.Printf("\n%s\n", result.Message)
	}
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

			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

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

			playerIdent, err := resolvePlayerIdentifier()
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

// --- live `construction override` verb -----------------------------------------------------------
