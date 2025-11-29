package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// NewOperationsCommand creates the operations command with subcommands
func NewOperationsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "operations",
		Short: "Manage resource extraction and manufacturing operations",
		Long: `Unified management for resource operations including gas extraction and manufacturing.

This command provides a single entry point for starting, monitoring, and stopping
both gas extraction and manufacturing operations.

Examples:
  # Start both gas and manufacturing operations
  spacetraders operations start --system X1-AU21 --gas --manufacturing

  # Start only gas extraction
  spacetraders operations start --system X1-AU21 --gas --siphons SIPHON-1,SIPHON-2 --storage STORAGE-1

  # Start only manufacturing
  spacetraders operations start --system X1-AU21 --manufacturing --min-price 2000

  # View status of all operations
  spacetraders operations status

  # Stop operations by type
  spacetraders operations stop --gas
  spacetraders operations stop --manufacturing`,
	}

	cmd.AddCommand(newOperationsStartCommand())
	cmd.AddCommand(newOperationsStatusCommand())
	cmd.AddCommand(newOperationsStopCommand())

	return cmd
}

// newOperationsStartCommand creates the operations start subcommand
func newOperationsStartCommand() *cobra.Command {
	var (
		// Common flags
		systemSymbol string
		dryRun       bool

		// Operation type flags
		enableGas           bool
		enableManufacturing bool

		// Gas-specific flags
		siphonsCsv  string
		storageCsv  string
		gasGiant    string
		force       bool
		maxLegTime  int

		// Manufacturing-specific flags
		minPrice     int
		maxWorkers   int
		maxPipelines int
		minBalance   int
		strategy     string
	)

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start resource operations in a system",
		Long: `Start gas extraction and/or manufacturing operations in a system.

At least one of --gas or --manufacturing must be specified.

Gas Extraction:
  Deploys siphon ships to extract resources from gas giants and storage ships
  to buffer the extracted resources. Manufacturing haulers will automatically
  pick up buffered resources via STORAGE_ACQUIRE_DELIVER tasks.

Manufacturing:
  Discovers high-demand goods, manufactures them using the supply chain,
  and sells them for profit using a task-based pipeline architecture.

Examples:
  # Start both operations
  spacetraders operations start --system X1-AU21 --gas --manufacturing \
    --siphons SIPHON-1 --storage STORAGE-1 --min-price 2000

  # Gas only with auto-selected gas giant
  spacetraders operations start --system X1-AU21 --gas \
    --siphons SIPHON-1,SIPHON-2 --storage STORAGE-1

  # Manufacturing only with custom strategy
  spacetraders operations start --system X1-AU21 --manufacturing \
    --strategy prefer-fabricate --max-workers 5

  # Dry run to preview operations
  spacetraders operations start --system X1-AU21 --gas --manufacturing --dry-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate at least one operation type is specified
			if !enableGas && !enableManufacturing {
				return fmt.Errorf("at least one of --gas or --manufacturing must be specified")
			}

			// Validate system symbol
			if systemSymbol == "" {
				return fmt.Errorf("--system flag is required")
			}

			// Validate gas-specific requirements
			if enableGas {
				if siphonsCsv == "" {
					return fmt.Errorf("--siphons flag is required when --gas is enabled")
				}
				if storageCsv == "" {
					return fmt.Errorf("--storage flag is required when --gas is enabled")
				}
			}

			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			// Connect to daemon
			client, err := NewDaemonClient(socketPath)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer client.Close()

			playerID := playerIdent.PlayerID

			fmt.Println("\nStarting Resource Operations")
			fmt.Println("════════════════════════════")
			fmt.Printf("System:  %s\n", systemSymbol)
			fmt.Printf("Player:  %s (ID: %d)\n", playerIdent.AgentSymbol, playerID)
			if dryRun {
				fmt.Println("Mode:    DRY RUN (preview only)")
			}
			fmt.Println()

			var results []operationResult

			// Start gas extraction if enabled
			if enableGas {
				result := startGasOperation(client, playerID, gasGiant, siphonsCsv, storageCsv, force, dryRun, maxLegTime)
				results = append(results, result)
			}

			// Start manufacturing if enabled
			if enableManufacturing {
				result := startManufacturingOperation(client, playerID, systemSymbol, minPrice, maxWorkers, maxPipelines, minBalance, strategy, dryRun)
				results = append(results, result)
			}

			// Display summary
			fmt.Println("\nOperation Summary")
			fmt.Println("─────────────────")
			for _, r := range results {
				statusIcon := "✓"
				if r.err != nil {
					statusIcon = "✗"
				}
				fmt.Printf("%s %s: ", statusIcon, r.operationType)
				if r.err != nil {
					fmt.Printf("FAILED - %v\n", r.err)
				} else {
					fmt.Printf("%s\n", r.containerID)
				}
			}

			// Display tracking info
			fmt.Println("\nTracking:")
			for _, r := range results {
				if r.err == nil && r.containerID != "" {
					fmt.Printf("  spacetraders container logs %s\n", r.containerID)
				}
			}

			return nil
		},
	}

	// Common flags
	cmd.Flags().StringVar(&systemSymbol, "system", "", "System symbol (required)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview operations without executing")

	// Operation type flags
	cmd.Flags().BoolVar(&enableGas, "gas", false, "Enable gas extraction operation")
	cmd.Flags().BoolVar(&enableManufacturing, "manufacturing", false, "Enable manufacturing operation")

	// Gas-specific flags
	cmd.Flags().StringVar(&siphonsCsv, "siphons", "", "Comma-separated siphon ship symbols (required for gas)")
	cmd.Flags().StringVar(&storageCsv, "storage", "", "Comma-separated storage ship symbols (required for gas)")
	cmd.Flags().StringVar(&gasGiant, "gas-giant", "", "Gas giant waypoint (optional, auto-selects if not provided)")
	cmd.Flags().BoolVar(&force, "force", false, "Override fuel validation warnings (gas)")
	cmd.Flags().IntVar(&maxLegTime, "max-leg-time", 0, "Max time per leg in minutes (gas, 0 = no limit)")

	// Manufacturing-specific flags
	cmd.Flags().IntVar(&minPrice, "min-price", 1000, "Minimum purchase price threshold (manufacturing)")
	cmd.Flags().IntVar(&maxWorkers, "max-workers", 5, "Maximum parallel workers (manufacturing)")
	cmd.Flags().IntVar(&maxPipelines, "max-pipelines", 3, "Maximum concurrent pipelines (manufacturing)")
	cmd.Flags().IntVar(&minBalance, "min-balance", 0, "Minimum credit balance to maintain (manufacturing)")
	cmd.Flags().StringVar(&strategy, "strategy", "prefer-fabricate", "Acquisition strategy: prefer-buy, prefer-fabricate, smart")

	cmd.MarkFlagRequired("system")

	return cmd
}

// operationResult holds the result of starting an operation
type operationResult struct {
	operationType string
	containerID   string
	err           error
}

// startGasOperation starts a gas extraction operation
func startGasOperation(client *DaemonClient, playerID int, gasGiant, siphonsCsv, storageCsv string, force, dryRun bool, maxLegTime int) operationResult {
	siphons := parseCsvList(siphonsCsv)
	storage := parseCsvList(storageCsv)

	fmt.Printf("Gas Extraction:\n")
	fmt.Printf("  Siphon Ships:  %s\n", strings.Join(siphons, ", "))
	fmt.Printf("  Storage Ships: %s\n", strings.Join(storage, ", "))
	if gasGiant != "" {
		fmt.Printf("  Gas Giant:     %s\n", gasGiant)
	} else {
		fmt.Printf("  Gas Giant:     (auto-select)\n")
	}
	fmt.Println()

	timeout := 30 * time.Second
	if dryRun {
		timeout = 300 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	result, err := client.GasExtractionOperation(ctx, gasGiant, siphons, storage, force, dryRun, maxLegTime, playerID)
	if err != nil {
		return operationResult{operationType: "Gas Extraction", err: err}
	}

	if dryRun {
		fmt.Printf("  [DRY RUN] Gas Giant: %s\n", result.GasGiant)
	}

	return operationResult{
		operationType: "Gas Extraction",
		containerID:   result.ContainerID,
	}
}

// startManufacturingOperation starts a manufacturing operation
func startManufacturingOperation(client *DaemonClient, playerID int, systemSymbol string, minPrice, maxWorkers, maxPipelines, minBalance int, strategy string, dryRun bool) operationResult {
	fmt.Printf("Manufacturing:\n")
	fmt.Printf("  Min Price:    %d\n", minPrice)
	fmt.Printf("  Max Workers:  %d\n", maxWorkers)
	fmt.Printf("  Max Pipelines: %d\n", maxPipelines)
	fmt.Printf("  Strategy:     %s\n", strategy)
	if minBalance > 0 {
		fmt.Printf("  Min Balance:  %d\n", minBalance)
	}
	fmt.Println()

	if dryRun {
		// For dry run, we just show the configuration
		fmt.Println("  [DRY RUN] Would start manufacturing coordinator with above settings")
		return operationResult{
			operationType: "Manufacturing",
			containerID:   "(dry-run)",
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := client.StartParallelManufacturingCoordinator(ctx, systemSymbol, playerID, minPrice, maxWorkers, maxPipelines, minBalance, strategy)
	if err != nil {
		return operationResult{operationType: "Manufacturing", err: err}
	}

	return operationResult{
		operationType: "Manufacturing",
		containerID:   result.ContainerID,
	}
}

// newOperationsStatusCommand creates the operations status subcommand
func newOperationsStatusCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show status of all running operations",
		Long: `Display the status of all running gas extraction and manufacturing operations.

Shows a unified view of:
  - Gas coordinators (gas_coordinator containers)
  - Manufacturing coordinators (manufacturing_coordinator containers)
  - Siphon workers (gas_siphon_worker containers)
  - Manufacturing task workers (manufacturing_task_worker containers)

Examples:
  spacetraders operations status
  spacetraders operations status --player-id 1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := NewDaemonClient(socketPath)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			var playerIDPtr *int
			if playerID > 0 {
				playerIDPtr = &playerID
			}

			// Only show running/interrupted containers
			status := "RUNNING,INTERRUPTED"
			containers, err := client.ListContainers(ctx, playerIDPtr, &status)
			if err != nil {
				return fmt.Errorf("failed to list containers: %w", err)
			}

			// Filter and group by operation type
			var gasCoordinators, gasWorkers []*ContainerInfo
			var mfgCoordinators, mfgWorkers []*ContainerInfo
			var other []*ContainerInfo

			for _, c := range containers {
				switch {
				case strings.Contains(c.ContainerType, "gas_coordinator"):
					gasCoordinators = append(gasCoordinators, c)
				case strings.Contains(c.ContainerType, "gas_siphon"):
					gasWorkers = append(gasWorkers, c)
				case strings.Contains(c.ContainerType, "manufacturing_coordinator"):
					mfgCoordinators = append(mfgCoordinators, c)
				case strings.Contains(c.ContainerType, "manufacturing_task"):
					mfgWorkers = append(mfgWorkers, c)
				default:
					other = append(other, c)
				}
			}

			// Display results
			fmt.Println("\nResource Operations Status")
			fmt.Println("══════════════════════════")

			// Gas operations
			fmt.Println("\nGas Extraction:")
			if len(gasCoordinators) == 0 && len(gasWorkers) == 0 {
				fmt.Println("  No active gas operations")
			} else {
				displayOperationGroup("  Coordinators:", gasCoordinators)
				displayOperationGroup("  Workers:", gasWorkers)
			}

			// Manufacturing operations
			fmt.Println("\nManufacturing:")
			if len(mfgCoordinators) == 0 && len(mfgWorkers) == 0 {
				fmt.Println("  No active manufacturing operations")
			} else {
				displayOperationGroup("  Coordinators:", mfgCoordinators)
				displayOperationGroup("  Workers:", mfgWorkers)
			}

			// Other containers (for completeness)
			if len(other) > 0 {
				fmt.Println("\nOther Containers:")
				displayOperationGroup("  ", other)
			}

			total := len(gasCoordinators) + len(gasWorkers) + len(mfgCoordinators) + len(mfgWorkers) + len(other)
			fmt.Printf("\nTotal: %d active containers\n", total)

			return nil
		},
	}

	return cmd
}

// displayOperationGroup displays a group of containers
func displayOperationGroup(label string, containers []*ContainerInfo) {
	if len(containers) == 0 {
		return
	}
	fmt.Println(label)
	for _, c := range containers {
		iteration := fmt.Sprintf("%d/%d", c.CurrentIteration, c.MaxIterations)
		if c.MaxIterations == -1 {
			iteration = fmt.Sprintf("%d/∞", c.CurrentIteration)
		}
		fmt.Printf("    %-50s %-12s %s\n", truncateStr(c.ContainerID, 50), c.Status, iteration)
	}
}

// truncateStr truncates a string to maxLen characters
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// newOperationsStopCommand creates the operations stop subcommand
func newOperationsStopCommand() *cobra.Command {
	var (
		stopGas           bool
		stopManufacturing bool
		systemSymbol      string
	)

	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop running operations",
		Long: `Stop running gas extraction and/or manufacturing operations.

Without flags, stops ALL coordinators (both gas and manufacturing).
Use --gas or --manufacturing to stop only specific operation types.

Examples:
  # Stop all operations
  spacetraders operations stop

  # Stop only gas operations
  spacetraders operations stop --gas

  # Stop only manufacturing operations
  spacetraders operations stop --manufacturing

  # Stop operations in a specific system
  spacetraders operations stop --system X1-AU21`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := NewDaemonClient(socketPath)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			// If neither specified, stop both
			if !stopGas && !stopManufacturing {
				stopGas = true
				stopManufacturing = true
			}

			var playerIDPtr *int
			if playerID > 0 {
				playerIDPtr = &playerID
			}

			// Get running containers
			status := "RUNNING,INTERRUPTED"
			containers, err := client.ListContainers(ctx, playerIDPtr, &status)
			if err != nil {
				return fmt.Errorf("failed to list containers: %w", err)
			}

			// Filter to coordinators only (workers will be stopped by their coordinators)
			var toStop []*ContainerInfo
			for _, c := range containers {
				// Only stop coordinators, not workers
				isGasCoordinator := c.ContainerType == "gas_coordinator"
				isMfgCoordinator := c.ContainerType == "manufacturing_coordinator"

				if (stopGas && isGasCoordinator) || (stopManufacturing && isMfgCoordinator) {
					// Optional system filter
					if systemSymbol != "" {
						// Check if container is for the specified system (from metadata)
						if !strings.Contains(c.Metadata, systemSymbol) {
							continue
						}
					}
					toStop = append(toStop, c)
				}
			}

			if len(toStop) == 0 {
				fmt.Println("No matching operations to stop")
				return nil
			}

			fmt.Printf("Stopping %d coordinator(s)...\n\n", len(toStop))

			// Stop each coordinator
			for _, c := range toStop {
				result, err := client.StopContainer(ctx, c.ContainerID)
				if err != nil {
					fmt.Printf("✗ %s: %v\n", c.ContainerID, err)
				} else {
					fmt.Printf("✓ %s: %s\n", c.ContainerID, result.Status)
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&stopGas, "gas", false, "Stop gas extraction operations")
	cmd.Flags().BoolVar(&stopManufacturing, "manufacturing", false, "Stop manufacturing operations")
	cmd.Flags().StringVar(&systemSymbol, "system", "", "Only stop operations in this system")

	return cmd
}
