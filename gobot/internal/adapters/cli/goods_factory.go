package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
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
	cmd.AddCommand(newGoodsFactoryCommand())

	return cmd
}

// factoryWorkerCapMutator is the subset of daemon operations the `goods factory
// workers` verb needs (sp-ev0n). By construction it exposes ONLY the
// FactoryWorkerCap RPC — no container-restart method — so "the coordinator is never
// restarted" is guaranteed by the surface this verb can reach, exactly as the
// `fleet hub` verbs guarantee it for the standby set.
type factoryWorkerCapMutator interface {
	FactoryWorkerCap(ctx context.Context, containerID string, count int, playerID *int32, agentSymbol *string) (*pb.FactoryWorkerCapResponse, error)
}

// runGoodsFactoryWorkers sets a RUNNING goods factory's concurrent-hull cap live via
// the daemon, then formats the operator-facing result. The coordinator re-reads the
// cap each production pass and converges its fan-out to count on the next tick — no
// container restart. A no-op (the cap already equalled count) is reported honestly.
func runGoodsFactoryWorkers(ctx context.Context, client factoryWorkerCapMutator, containerID string, count int, playerID *int32, agentSymbol *string) (string, error) {
	resp, err := client.FactoryWorkerCap(ctx, containerID, count, playerID, agentSymbol)
	if err != nil {
		return "", fmt.Errorf("failed to set worker cap %d on factory %s: %w", count, containerID, err)
	}
	if !resp.Changed {
		return fmt.Sprintf("• factory %s worker cap is already %d — unchanged\n", containerID, resp.WorkerCap), nil
	}
	return fmt.Sprintf("✓ factory %s worker cap set to %d — the coordinator re-reads it live and converges to at most %d concurrent hull(s) next pass; no container restart.\n", containerID, resp.WorkerCap, resp.WorkerCap), nil
}

// newGoodsFactoryCommand creates the `goods factory` subcommand group (sp-ev0n) —
// live tuning of a RUNNING goods factory operation, the factory analogue of the
// live coordinator knobs (`fleet hub`, `fleet add`/`remove`).
func newGoodsFactoryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "factory",
		Short: "Tune a running goods factory operation live (no restart)",
		Long: `Tune a RUNNING goods factory operation without restarting it.

Examples:
  spacetraders goods factory workers --container goods_factory-FAB_MATS-abcd --count 2`,
	}
	cmd.AddCommand(newGoodsFactoryWorkersCommand())
	return cmd
}

// newGoodsFactoryWorkersCommand creates the `goods factory workers` subcommand — the
// live per-op concurrent-hull cap (sp-ev0n).
func newGoodsFactoryWorkersCommand() *cobra.Command {
	var (
		containerID string
		count       int
	)

	cmd := &cobra.Command{
		Use:   "workers",
		Short: "Set a running goods factory's concurrent-hull cap live (no restart)",
		Long: `Set the maximum number of hulls a RUNNING goods factory runs concurrently,
without a restart. The coordinator re-reads its cap from its own container config
every production pass, so it converges the fan-out to the new count on the next
tick — a hull already mid-node finishes first, never force-killed. The cap is
per-operation (each goods factory has its own) and persists across daemon restarts.

Examples:
  spacetraders goods factory workers --container goods_factory-FAB_MATS-abcd --count 2
  spacetraders goods factory workers --container goods_factory-FAB_MATS-abcd --count 4`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if containerID == "" {
				return fmt.Errorf("--container flag is required (the goods factory operation to cap)")
			}
			if count < 1 {
				return fmt.Errorf("--count must be at least 1 (got %d) — raise it to widen the fan-out", count)
			}

			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			playerID, agentSymbol := playerPointers(playerIdent)

			msg, err := runGoodsFactoryWorkers(ctx, client, containerID, count, playerID, agentSymbol)
			if err != nil {
				return err
			}
			fmt.Print(msg)
			return nil
		},
	}

	cmd.Flags().StringVar(&containerID, "container", "", "Goods factory container/operation ID to cap (required)")
	cmd.Flags().IntVar(&count, "count", 0, "Maximum number of hulls the factory runs concurrently (required, >= 1)")

	return cmd
}

// newGoodsProduceCommand creates the goods produce subcommand
func newGoodsProduceCommand() *cobra.Command {
	var systemSymbol string
	var iterations int
	var inputsOnly bool

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
			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			// Start goods factory
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			// Convert iterations to *int32 for protobuf (nil if default)
			var maxIterations *int32
			if iterations != 0 {
				iter := int32(iterations)
				maxIterations = &iter
			}

			result, err := client.StartGoodsFactory(ctx, targetGood, &systemSymbol, playerIdent.PlayerID, &playerIdent.AgentSymbol, maxIterations, inputsOnly)
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
	cmd.Flags().IntVar(&iterations, "iterations", 1, "Number of production iterations (-1 for infinite, 0 or 1 for single run, >1 for specific count)")
	cmd.Flags().BoolVar(&inputsOnly, "inputs-only", false, "Construction-support mode: feed the dependency tree but do NOT harvest the fabricated output — leave it in factory stock for a construction pipeline to source")

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
			client, err := connectDaemon()
			if err != nil {
				return err
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
			client, err := connectDaemon()
			if err != nil {
				return err
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
