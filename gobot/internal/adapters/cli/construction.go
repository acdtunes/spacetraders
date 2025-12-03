package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
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

	// Add subcommands
	cmd.AddCommand(newConstructionStartCommand())
	cmd.AddCommand(newConstructionStatusCommand())

	return cmd
}

// newConstructionStartCommand creates the construction start subcommand
func newConstructionStartCommand() *cobra.Command {
	var supplyChainDepth int
	var maxWorkers int
	var systemSymbol string

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

The pipeline is IDEMPOTENT - running this command again will resume
an existing pipeline instead of creating a new one.

Examples:
  spacetraders construction start X1-FB5-I61 --player-id 1
  spacetraders construction start X1-FB5-I61 --system X1-FB5 --depth 3 --player-id 1`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			constructionSite := args[0]

			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			// Create gRPC client
			client, err := NewDaemonClient(socketPath)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
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

			result, err := client.StartConstructionPipeline(
				ctx,
				constructionSite,
				int32(playerIdent.PlayerID),
				&playerIdent.AgentSymbol,
				int32(supplyChainDepth),
				int32(maxWorkers),
				systemSymbolPtr,
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

			if result.Message != "" {
				fmt.Printf("\n%s\n", result.Message)
			}

			return nil
		},
	}

	cmd.Flags().IntVar(&supplyChainDepth, "depth", 3, "Supply chain depth (0=full, 1=raw, 2=intermediate, 3=buy final)")
	cmd.Flags().IntVar(&maxWorkers, "max-workers", 5, "Maximum parallel workers")
	cmd.Flags().StringVar(&systemSymbol, "system", "", "System symbol for market lookups (defaults to deriving from construction site)")

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
			client, err := NewDaemonClient(socketPath)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
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
