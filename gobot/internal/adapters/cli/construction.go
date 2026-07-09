package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
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

	return cmd
}

// newConstructionStartCommand creates the construction start subcommand
func newConstructionStartCommand() *cobra.Command {
	var supplyChainDepth int
	var maxWorkers int
	var systemSymbol string
	var minSupply string

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

The pipeline is IDEMPOTENT - running this command again will resume
an existing pipeline instead of creating a new one.

Examples:
  spacetraders construction start X1-FB5-I61 --player-id 1
  spacetraders construction start X1-FB5-I61 --system X1-FB5 --depth 3 --player-id 1
  spacetraders construction start X1-FB5-I61 --min-supply SCARCE --player-id 1`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate --min-supply before touching any infrastructure (mirrors
			// newShipBuyCommand's flag-validation-first pattern).
			minSupplyLevel, err := parseMinSupplyFlag(minSupply)
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

			result, err := client.StartConstructionPipeline(
				ctx,
				constructionSite,
				int32(playerIdent.PlayerID),
				&playerIdent.AgentSymbol,
				int32(supplyChainDepth),
				int32(maxWorkers),
				systemSymbolPtr,
				minSupplyPtr,
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
