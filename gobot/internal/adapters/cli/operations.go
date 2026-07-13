package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// NewOperationsCommand creates the operations command with subcommands
func NewOperationsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "operations",
		Short: "Manage resource extraction operations",
		Long: `Management for resource operations (gas extraction).

This command provides a single entry point for starting, monitoring, and stopping
gas extraction operations. (sp-jav2: the parallel manufacturing coordinator was
retired; goods manufacturing runs as the goods_factory_coordinator, launched
elsewhere. The stop verb still targets any lingering legacy manufacturing
containers for cleanup.)

Examples:
  # Start gas extraction
  spacetraders operations start --system X1-AU21 --gas --siphons SIPHON-1,SIPHON-2 --storage STORAGE-1

  # View status of all operations
  spacetraders operations status

  # Stop operations by type
  spacetraders operations stop --gas
  spacetraders operations stop --manufacturing  # legacy container cleanup`,
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

		// Operation type flag
		enableGas bool

		// Gas-specific flags
		siphonsCsv string
		storageCsv string
		gasGiant   string
		force      bool
		maxLegTime int
	)

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start gas extraction operations in a system",
		Long: `Start gas extraction operations in a system.

Gas Extraction:
  Deploys siphon ships to extract resources from gas giants and storage ships
  to buffer the extracted resources for downstream haulers.

Examples:
  # Gas extraction with auto-selected gas giant
  spacetraders operations start --system X1-AU21 --gas \
    --siphons SIPHON-1,SIPHON-2 --storage STORAGE-1

  # Dry run to preview the operation
  spacetraders operations start --system X1-AU21 --gas --dry-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate the operation type is specified
			if !enableGas {
				return fmt.Errorf("--gas must be specified")
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
			client, err := connectDaemon()
			if err != nil {
				return err
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

	// Operation type flag
	cmd.Flags().BoolVar(&enableGas, "gas", false, "Enable gas extraction operation")

	// Gas-specific flags
	cmd.Flags().StringVar(&siphonsCsv, "siphons", "", "Comma-separated siphon ship symbols (required for gas)")
	cmd.Flags().StringVar(&storageCsv, "storage", "", "Comma-separated storage ship symbols (required for gas)")
	cmd.Flags().StringVar(&gasGiant, "gas-giant", "", "Gas giant waypoint (optional, auto-selects if not provided)")
	cmd.Flags().BoolVar(&force, "force", false, "Override fuel validation warnings (gas)")
	cmd.Flags().IntVar(&maxLegTime, "max-leg-time", 0, "Max time per leg in minutes (gas, 0 = no limit)")

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
			client, err := connectDaemon()
			if err != nil {
				return err
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

			// Partition running containers into operation categories.
			groups := classifyOperationContainers(containers)
			gasCoordinators := groups.gasCoordinators
			gasWorkers := groups.gasWorkers
			mfgCoordinators := groups.mfgCoordinators
			mfgWorkers := groups.mfgWorkers
			other := groups.other

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

// operationGroups partitions running containers into the categories that
// `operations status` renders. Construction supply work runs through the
// manufacturing coordinator and its task workers, so it surfaces under the
// manufacturing groups rather than as a category of its own.
type operationGroups struct {
	gasCoordinators []*ContainerInfo
	gasWorkers      []*ContainerInfo
	mfgCoordinators []*ContainerInfo
	mfgWorkers      []*ContainerInfo
	other           []*ContainerInfo
}

// classifyOperationContainers partitions containers by their registered
// container type. Types that belong to no known operation land in `other`.
func classifyOperationContainers(containers []*ContainerInfo) operationGroups {
	var g operationGroups
	for _, c := range containers {
		switch {
		case isGasCoordinatorType(c.ContainerType):
			g.gasCoordinators = append(g.gasCoordinators, c)
		case isGasWorkerType(c.ContainerType):
			g.gasWorkers = append(g.gasWorkers, c)
		case isManufacturingCoordinatorType(c.ContainerType):
			g.mfgCoordinators = append(g.mfgCoordinators, c)
		case isManufacturingWorkerType(c.ContainerType):
			g.mfgWorkers = append(g.mfgWorkers, c)
		default:
			g.other = append(g.other, c)
		}
	}
	return g
}

// selectCoordinatorsToStop picks the coordinator containers an `operations stop`
// invocation should halt; workers are omitted because their coordinator stops
// them. A non-empty systemSymbol restricts to containers whose metadata
// references that system. Status and stop share the same type predicates so the
// two verbs can never disagree about what is a coordinator.
func selectCoordinatorsToStop(containers []*ContainerInfo, stopGas, stopManufacturing bool, systemSymbol string) []*ContainerInfo {
	var toStop []*ContainerInfo
	for _, c := range containers {
		isGas := isGasCoordinatorType(c.ContainerType)
		isMfg := isManufacturingCoordinatorType(c.ContainerType)
		if (stopGas && isGas) || (stopManufacturing && isMfg) {
			if systemSymbol != "" && !strings.Contains(c.Metadata, systemSymbol) {
				continue
			}
			toStop = append(toStop, c)
		}
	}
	return toStop
}

// The type predicates below compare against the canonical domain container
// types (the single source of truth the daemon persists) using a
// case-insensitive match. The daemon stores UPPERCASE type strings
// (e.g. "MANUFACTURING_COORDINATOR"); matching those exact registered types is
// what keeps `operations status`/`stop` in sync with what is actually running.

func isGasCoordinatorType(containerType string) bool {
	return strings.EqualFold(containerType, string(container.ContainerTypeGasCoordinator))
}

func isGasWorkerType(containerType string) bool {
	return strings.EqualFold(containerType, string(container.ContainerTypeGasSiphonWorker))
}

// isManufacturingCoordinatorType matches the standard and parallel task-based manufacturing
// coordinators, plus the dedicated construction-supply drain (sp-382j). The parallel coordinator
// (container IDs prefixed "parallel_manufacturing-") registers as ContainerTypeManufacturingCoordinator
// today; ContainerTypeParallelManufacturing is matched too so the verb stays correct if a coordinator
// is ever registered under that sibling type. Construction supply now runs through its OWN drain
// (ContainerTypeConstructionCoordinator, no longer the vestigial manufacturing coordinator), so it is
// matched here to keep the operations verbs seeing construction activity.
func isManufacturingCoordinatorType(containerType string) bool {
	return strings.EqualFold(containerType, string(container.ContainerTypeManufacturingCoordinator)) ||
		strings.EqualFold(containerType, string(container.ContainerTypeParallelManufacturing)) ||
		strings.EqualFold(containerType, string(container.ContainerTypeConstructionCoordinator))
}

func isManufacturingWorkerType(containerType string) bool {
	return strings.EqualFold(containerType, string(container.ContainerTypeManufacturingTaskWorker))
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
			client, err := connectDaemon()
			if err != nil {
				return err
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

			// Select coordinator containers to stop (workers stop with their coordinator).
			toStop := selectCoordinatorsToStop(containers, stopGas, stopManufacturing, systemSymbol)

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
