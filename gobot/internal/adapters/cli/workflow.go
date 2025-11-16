package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// NewWorkflowCommand creates the workflow command with subcommands
func NewWorkflowCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflow",
		Short: "Execute complex multi-step workflows",
		Long: `Execute automated workflows that run as background daemons.

Workflows are multi-step operations that combine navigation, trading, and scouting
into automated tasks. All workflows run in background containers that can be
monitored using the container commands.

Examples:
  spacetraders workflow batch-contract --ship SHIP-1 --iterations 5
  spacetraders workflow scout-markets --ships SCOUT-1,SCOUT-2 --system X1-GZ7 --markets X1-GZ7-A1,X1-GZ7-B2`,
	}

	// Add subcommands
	cmd.AddCommand(newWorkflowBatchContractCommand())
	cmd.AddCommand(newWorkflowScoutMarketsCommand())
	cmd.AddCommand(newWorkflowScoutAllMarketsCommand())

	return cmd
}

// newWorkflowBatchContractCommand creates the workflow batch-contract subcommand
func newWorkflowBatchContractCommand() *cobra.Command {
	var (
		shipSymbol string
		iterations int
	)

	cmd := &cobra.Command{
		Use:   "batch-contract",
		Short: "Execute batch contract workflow",
		Long: `Execute automated contract workflow that negotiates, accepts, purchases goods,
delivers cargo, and fulfills contracts. Runs in background as a daemon.

The daemon will automatically:
- Negotiate new contracts or resume existing ones
- Evaluate contract profitability
- Accept contracts
- Purchase required goods from cheapest markets
- Deliver cargo (handles multi-trip delivery)
- Fulfill contracts
- Return a container ID for tracking progress

Examples:
  spacetraders workflow batch-contract --ship SHIP-1 --iterations 5 --player-id 1
  spacetraders workflow batch-contract --ship SHIP-1 --iterations 10 --agent ENDURANCE
  spacetraders workflow batch-contract --ship CARGO-1 --iterations -1  # Infinite loop`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate flags
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}
			if iterations == 0 {
				return fmt.Errorf("--iterations cannot be 0 (use -1 for infinite)")
			}

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

			// Execute batch contract workflow command
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			result, err := client.BatchContractWorkflow(ctx, shipSymbol, iterations, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("batch contract workflow failed: %w", err)
			}

			// Display result
			fmt.Println("✓ Batch contract workflow started successfully")
			fmt.Printf("  Container ID:     %s\n", result.ContainerID)
			fmt.Printf("  Ship:             %s\n", result.ShipSymbol)
			fmt.Printf("  Agent:            %s (player %d)\n", playerIdent.AgentSymbol, playerIdent.PlayerID)
			fmt.Printf("  Iterations:       %d", result.Iterations)
			if result.Iterations == -1 {
				fmt.Print(" (infinite)")
			}
			fmt.Println()
			fmt.Printf("  Status:           %s\n", result.Status)
			fmt.Printf("\nTrack progress with: spacetraders container logs %s\n", result.ContainerID)

			return nil
		},
	}

	// Command-specific flags
	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol to use for contracts (required)")
	cmd.Flags().IntVar(&iterations, "iterations", 1, "Number of contracts to process (default: 1, use -1 for infinite)")

	return cmd
}

// newWorkflowScoutMarketsCommand creates the workflow scout-markets subcommand
func newWorkflowScoutMarketsCommand() *cobra.Command {
	var (
		shipsCsv   string
		system     string
		marketsCsv string
		iterations int
	)

	cmd := &cobra.Command{
		Use:   "scout-markets",
		Short: "Deploy fleet to scout markets with VRP optimization",
		Long: `Distribute markets across multiple ships using Vehicle Routing Problem optimization.

The daemon will:
- Check for existing scout-tour containers for each ship (reuses if found)
- For ships needing containers:
  - Optimize market distribution using VRP solver
  - Create scout-tour containers with assigned markets
- Return combined results (new + reused containers)

This command is idempotent: ships with existing containers are reused automatically.

Examples:
  # Deploy 2 scouts to 5 markets
  spacetraders workflow scout-markets --ships SCOUT-1,SCOUT-2 --system X1-TEST --markets X1-TEST-A1,X1-TEST-B2,X1-TEST-C3,X1-TEST-D4,X1-TEST-E5 --agent ENDURANCE

  # Single ship (no VRP optimization needed)
  spacetraders workflow scout-markets --ships SCOUT-1 --system X1-GZ7 --markets X1-GZ7-A1,X1-GZ7-B2 --agent ENDURANCE

  # Infinite loop
  spacetraders workflow scout-markets --ships SCOUT-1,SCOUT-2,SCOUT-3 --system X1-TEST --markets X1-TEST-A1,X1-TEST-B2,X1-TEST-C3 --iterations -1 --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate flags
			if shipsCsv == "" {
				return fmt.Errorf("--ships flag is required")
			}
			if system == "" {
				return fmt.Errorf("--system flag is required")
			}
			if marketsCsv == "" {
				return fmt.Errorf("--markets flag is required")
			}

			// Parse CSV inputs
			ships := parseCsvList(shipsCsv)
			markets := parseCsvList(marketsCsv)

			if len(ships) == 0 {
				return fmt.Errorf("at least one ship is required")
			}
			if len(markets) == 0 {
				return fmt.Errorf("at least one market is required")
			}

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

			// Execute scout markets command
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			fmt.Printf("Deploying %d ship(s) to scout %d market(s) in %s...\n\n", len(ships), len(markets), system)

			result, err := client.ScoutMarkets(ctx, ships, system, markets, iterations, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("scout markets deployment failed: %w", err)
			}

			// Display results
			fmt.Println("=== Fleet Deployment Complete ===")
			fmt.Printf("\nTotal containers: %d\n", len(result.ContainerIDs))
			fmt.Printf("New containers: %d\n", len(result.ContainerIDs)-len(result.ReusedContainers))
			fmt.Printf("Reused containers: %d\n\n", len(result.ReusedContainers))

			// Display market assignments
			if len(result.Assignments) > 0 {
				fmt.Println("Market Assignments:")
				fmt.Println("Ship             Markets")
				fmt.Println("---------------  -------")
				for shipSymbol, assignment := range result.Assignments {
					fmt.Printf("%-15s  %s\n", shipSymbol, strings.Join(assignment.Markets, ", "))
				}
				fmt.Println()
			}

			// Display container IDs
			if len(result.ContainerIDs) > 0 {
				fmt.Println("Container IDs:")
				for _, cid := range result.ContainerIDs {
					fmt.Printf("  - %s", cid)
					// Mark reused containers
					for _, reused := range result.ReusedContainers {
						if cid == reused {
							fmt.Print(" (reused)")
							break
						}
					}
					fmt.Println()
				}
				fmt.Println()
			}

			fmt.Println("Track progress with: spacetraders container logs <container-id>")
			fmt.Println("View all containers: spacetraders container list")

			return nil
		},
	}

	// Command-specific flags
	cmd.Flags().StringVar(&shipsCsv, "ships", "", "Comma-separated list of ship symbols (required)")
	cmd.Flags().StringVar(&system, "system", "", "System symbol (required)")
	cmd.Flags().StringVar(&marketsCsv, "markets", "", "Comma-separated list of market waypoints (required)")
	cmd.Flags().IntVar(&iterations, "iterations", 1, "Number of complete tours (default: 1, use -1 for infinite)")

	return cmd
}

// newWorkflowScoutAllMarketsCommand creates the workflow scout-all-markets subcommand
func newWorkflowScoutAllMarketsCommand() *cobra.Command {
	var (
		system string
	)

	cmd := &cobra.Command{
		Use:   "scout-all-markets",
		Short: "Automatically assign all probe/satellite ships to scout all non-fuel-station markets",
		Long: `Automatically discovers and assigns all probe/satellite ships in a system to scout
all marketplaces (excluding fuel stations). Uses VRP optimization to distribute markets
efficiently across the fleet.

The command will:
- Find all probe/satellite ships in the specified system
- Find all marketplaces with the MARKETPLACE trait (excluding FUEL_STATION)
- Optimize market distribution using VRP solver
- Create scout-tour containers with infinite iterations

This command is idempotent: ships with existing containers are reused automatically.

Examples:
  # Scout all markets in system X1-GZ7
  spacetraders workflow scout-all-markets --system X1-GZ7 --agent ENDURANCE

  # Scout all markets in system X1-TEST
  spacetraders workflow scout-all-markets --system X1-TEST --player-id 1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate flags
			if system == "" {
				return fmt.Errorf("--system flag is required")
			}

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

			// Create fleet-assignment container via daemon
			// Timeout for container creation (30 seconds to handle database contention)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			fmt.Printf("Starting scout fleet assignment for system %s...\n\n", system)

			result, err := client.AssignScoutingFleet(ctx, system, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("failed to create fleet assignment container: %w", err)
			}

			// Display result
			fmt.Println("✓ Scout fleet assignment started successfully")
			fmt.Printf("\n  Container ID: %s\n", result.ContainerID)
			fmt.Printf("  System:       %s\n", system)
			fmt.Printf("  Agent:        %s (player %d)\n\n", playerIdent.AgentSymbol, playerIdent.PlayerID)
			fmt.Println("The fleet assignment is running in the background.")
			fmt.Println("VRP optimization will distribute markets across probe/satellite ships.")
			fmt.Println()
			fmt.Println("Track progress with:")
			fmt.Printf("  spacetraders container logs %s\n", result.ContainerID)
			fmt.Println()
			fmt.Println("View created scout-tour containers with:")
			fmt.Printf("  spacetraders container list --player-id %d\n", playerIdent.PlayerID)

			return nil
		},
	}

	// Command-specific flags
	cmd.Flags().StringVar(&system, "system", "", "System symbol (required)")

	return cmd
}

// parseCsvList parses a CSV string into a slice of trimmed strings
func parseCsvList(csv string) []string {
	parts := strings.Split(csv, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
