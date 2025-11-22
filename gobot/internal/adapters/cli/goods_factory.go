package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/spf13/cobra"
)

// NewGoodsCommand creates the goods command with subcommands
func NewGoodsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "goods",
		Short: "Manage automated goods production",
		Long: `Manage automated goods production using supply chain fabrication.

The goods factory system recursively produces any good in the SpaceTraders economy
by building complete dependency trees, acquiring raw materials, and coordinating
multi-ship production operations.

Examples:
  spacetraders goods produce ADVANCED_CIRCUITRY --system X1-GZ7
  spacetraders goods status <factory-id>
  spacetraders goods stop <factory-id>`,
	}

	// Add subcommands
	cmd.AddCommand(newGoodsProduceCommand())
	cmd.AddCommand(newGoodsStatusCommand())
	cmd.AddCommand(newGoodsStopCommand())

	return cmd
}

// newGoodsProduceCommand creates the goods produce subcommand
func newGoodsProduceCommand() *cobra.Command {
	var systemSymbol string

	cmd := &cobra.Command{
		Use:   "produce <good>",
		Short: "Produce a good using automated supply chain fabrication",
		Long: `Produce a good using automated supply chain fabrication.

The goods factory will:
- Build a complete dependency tree to raw materials
- Identify what can be bought vs what must be fabricated
- Use idle hauler ships to execute production
- Acquire whatever quantity is available at markets
- Poll for production completion at manufacturing waypoints

The factory operates with a market-driven model - it acquires whatever quantity
is available rather than producing fixed amounts.

Examples:
  spacetraders goods produce ADVANCED_CIRCUITRY --system X1-GZ7 --player-id 1
  spacetraders goods produce MACHINERY --system X1-GZ7 --agent ENDURANCE`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			targetGood := args[0]

			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			// Require system symbol for now (could auto-detect from ship location later)
			if systemSymbol == "" {
				return fmt.Errorf("--system flag is required")
			}

			// Create gRPC client
			client, err := NewDaemonClient(socketPath)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer client.Close()

			// Start goods factory
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			result, err := client.StartGoodsFactory(ctx, targetGood, &systemSymbol, playerIdent.PlayerID, &playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("failed to start goods factory: %w", err)
			}

			// Display result
			fmt.Println("✓ Goods factory started successfully")
			fmt.Printf("  Factory ID:       %s\n", result.FactoryID)
			fmt.Printf("  Target Good:      %s\n", result.TargetGood)
			fmt.Printf("  System:           %s\n", systemSymbol)
			fmt.Printf("  Agent:            %s (player %d)\n", playerIdent.AgentSymbol, playerIdent.PlayerID)
			fmt.Printf("  Status:           %s\n", result.Status)
			if result.Message != "" {
				fmt.Printf("  Message:          %s\n", result.Message)
			}
			fmt.Println("\n  The factory is building the dependency tree and coordinating production.")
			fmt.Println("  Use 'spacetraders goods status " + result.FactoryID + "' to check progress.")
			fmt.Println("  Use 'spacetraders goods stop " + result.FactoryID + "' to stop the factory.")

			return nil
		},
	}

	cmd.Flags().StringVar(&systemSymbol, "system", "", "System symbol where production will occur (required)")

	return cmd
}

// newGoodsStatusCommand creates the goods status subcommand
func newGoodsStatusCommand() *cobra.Command {
	var showTree bool

	cmd := &cobra.Command{
		Use:   "status <factory-id>",
		Short: "Check the status of a goods factory",
		Long: `Check the status and progress of a goods factory.

Displays detailed information about the factory including:
- Current status (PENDING, ACTIVE, COMPLETED, FAILED, STOPPED)
- Target good and system
- Production progress (nodes completed vs total)
- Quantity acquired and total cost
- Dependency tree (with --tree flag)

Examples:
  spacetraders goods status factory_12345
  spacetraders goods status factory_12345 --tree`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			factoryID := args[0]

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

			// Get factory status
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			status, err := client.GetFactoryStatus(ctx, factoryID, playerIdent.PlayerID)
			if err != nil {
				return fmt.Errorf("failed to get factory status: %w", err)
			}

			// Display status
			fmt.Printf("Factory Status: %s\n", status.FactoryID)
			fmt.Printf("  Target Good:      %s\n", status.TargetGood)
			fmt.Printf("  System:           %s\n", status.SystemSymbol)
			fmt.Printf("  Status:           %s\n", status.Status)
			fmt.Printf("  Progress:         %d/%d nodes completed\n", status.NodesCompleted, status.NodesTotal)

			if status.QuantityAcquired > 0 {
				fmt.Printf("  Quantity:         %d units acquired\n", status.QuantityAcquired)
			}
			if status.TotalCost > 0 {
				fmt.Printf("  Total Cost:       %d credits\n", status.TotalCost)
			}

			// Display dependency tree if requested
			if showTree && status.DependencyTree != "" {
				fmt.Println("\nDependency Tree:")

				// Parse the tree JSON
				var tree *goods.SupplyChainNode
				if err := json.Unmarshal([]byte(status.DependencyTree), &tree); err != nil {
					// Fallback to raw JSON
					var rawTree interface{}
					if err := json.Unmarshal([]byte(status.DependencyTree), &rawTree); err != nil {
						fmt.Printf("  (raw) %s\n", status.DependencyTree)
					} else {
						prettyJSON, _ := json.MarshalIndent(rawTree, "  ", "  ")
						fmt.Printf("  %s\n", prettyJSON)
					}
				} else {
					// Use rich tree formatter
					formatter := NewTreeFormatter(true, true) // colors and emojis
					fmt.Println(formatter.FormatTree(tree))
					fmt.Println("\n" + formatter.FormatTreeSummary(tree))
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&showTree, "tree", false, "Display the full dependency tree with visual indicators")

	return cmd
}

// newGoodsStopCommand creates the goods stop subcommand
func newGoodsStopCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop <factory-id>",
		Short: "Stop a running goods factory",
		Long: `Stop a running goods factory.

This will gracefully stop the factory coordinator and release any assigned ships
back to the idle pool.

Examples:
  spacetraders goods stop factory_12345`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			factoryID := args[0]

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

			// Stop factory
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			result, err := client.StopGoodsFactory(ctx, factoryID, playerIdent.PlayerID)
			if err != nil {
				return fmt.Errorf("failed to stop goods factory: %w", err)
			}

			// Display result
			fmt.Println("✓ Goods factory stopped successfully")
			fmt.Printf("  Factory ID:       %s\n", result.FactoryID)
			fmt.Printf("  Status:           %s\n", result.Status)
			if result.Message != "" {
				fmt.Printf("  Message:          %s\n", result.Message)
			}

			return nil
		},
	}

	return cmd
}
