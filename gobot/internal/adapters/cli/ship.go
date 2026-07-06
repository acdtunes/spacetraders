package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/graph"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/application/player"
	"github.com/andrescamacho/spacetraders-go/internal/application/setup"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// NewShipCommand creates the ship command with subcommands
func NewShipCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ship",
		Short: "Manage ships",
		Long: `Manage ships and view ship information.

Ships are your vessels in the SpaceTraders universe. Use these commands to
view your fleet, check ship details, monitor status, and perform ship operations.

Examples:
  spacetraders ship list --agent ENDURANCE
  spacetraders ship info --ship ENDURANCE-1 --agent ENDURANCE
  spacetraders ship navigate --ship ENDURANCE-1 --destination X1-GZ7-B1 --agent ENDURANCE
  spacetraders ship dock --ship ENDURANCE-1 --agent ENDURANCE
  spacetraders ship orbit --ship ENDURANCE-1 --agent ENDURANCE
  spacetraders ship refuel --ship ENDURANCE-1 --agent ENDURANCE`,
	}

	// Add subcommands
	cmd.AddCommand(newShipListCommand())
	cmd.AddCommand(newShipInfoCommand())
	cmd.AddCommand(newShipRefreshCommand())
	cmd.AddCommand(newShipNavigateCommand())
	cmd.AddCommand(newShipDockCommand())
	cmd.AddCommand(newShipOrbitCommand())
	cmd.AddCommand(newShipRefuelCommand())
	cmd.AddCommand(newShipJumpCommand())
	cmd.AddCommand(newShipSellCommand())
	// cmd.AddCommand(newShipJettisonCommand()) // TODO: implement jettison command

	return cmd
}

// shipAssignmentLister is the subset of the ship assignment repository the
// `ship list` CLI needs: a bulk read of role/assignment/cache-age info for
// every ship owned by a player.
type shipAssignmentLister interface {
	ListActive(ctx context.Context, playerID int) ([]persistence.ShipAssignmentInfo, error)
}

// shipListRow is a single rendered row of `ship list`, merging live daemon
// data with the persisted role/assignment/cache-age columns.
type shipListRow struct {
	Symbol        string `json:"symbol"`
	Location      string `json:"location"`
	NavStatus     string `json:"navStatus"`
	FuelCurrent   int32  `json:"fuelCurrent"`
	FuelCapacity  int32  `json:"fuelCapacity"`
	CargoUnits    int32  `json:"cargoUnits"`
	CargoCapacity int32  `json:"cargoCapacity"`
	EngineSpeed   int32  `json:"engineSpeed"`
	Role          string `json:"role"`
	Assignment    string `json:"assignment"`
	CacheAge      string `json:"cacheAge"`
}

// humanizeDuration renders a duration the way `ship list` shows cache age:
// seconds below a minute, minutes below an hour, hours+minutes beyond that.
func humanizeDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		hours := int(d.Hours())
		mins := int(d.Minutes()) % 60
		if mins == 0 {
			return fmt.Sprintf("%dh", hours)
		}
		return fmt.Sprintf("%dh%dm", hours, mins)
	}
}

// buildShipRows merges live ship data from the daemon with the persisted
// per-ship assignment info, defaulting role/assignment/cache age to "-" for
// ships that have no assignment row.
func buildShipRows(ships []*pb.ShipInfo, infos map[string]persistence.ShipAssignmentInfo, now time.Time) []shipListRow {
	rows := make([]shipListRow, 0, len(ships))

	for _, s := range ships {
		row := shipListRow{
			Symbol:        s.Symbol,
			Location:      s.Location,
			NavStatus:     s.NavStatus,
			FuelCurrent:   s.FuelCurrent,
			FuelCapacity:  s.FuelCapacity,
			CargoUnits:    s.CargoUnits,
			CargoCapacity: s.CargoCapacity,
			EngineSpeed:   s.EngineSpeed,
			Role:          "-",
			Assignment:    "-",
			CacheAge:      "-",
		}

		if info, ok := infos[s.Symbol]; ok {
			if info.Role != "" {
				row.Role = info.Role
			}
			if info.ContainerID != "" {
				row.Assignment = info.ContainerID
			}
			if !info.SyncedAt.IsZero() {
				row.CacheAge = humanizeDuration(now.Sub(info.SyncedAt))
			}
		}

		rows = append(rows, row)
	}

	return rows
}

// renderShipList prints the merged ship rows as a table or as JSON.
func renderShipList(rows []shipListRow, jsonOut bool) error {
	if jsonOut {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(rows)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SHIP SYMBOL\tLOCATION\tSTATUS\tFUEL\tCARGO\tSPEED\tROLE\tASSIGNMENT\tCACHE AGE")
	fmt.Fprintln(w, "-----------\t--------\t------\t----\t-----\t-----\t----\t----------\t---------")

	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d/%d\t%d/%d\t%d\t%s\t%s\t%s\n",
			r.Symbol,
			r.Location,
			r.NavStatus,
			r.FuelCurrent,
			r.FuelCapacity,
			r.CargoUnits,
			r.CargoCapacity,
			r.EngineSpeed,
			r.Role,
			r.Assignment,
			r.CacheAge,
		)
	}

	return w.Flush()
}

// runShipList merges live daemon ship data with the persisted per-ship
// assignment info and renders the result. The assignment repository is only
// queried when there is at least one ship to enrich.
func runShipList(ctx context.Context, ships []*pb.ShipInfo, lister shipAssignmentLister, playerID int, now time.Time, jsonOut bool) error {
	if len(ships) == 0 {
		fmt.Println("No ships found.")
		return nil
	}

	infos, err := lister.ListActive(ctx, playerID)
	if err != nil {
		return fmt.Errorf("failed to list ship assignments: %w", err)
	}

	infoMap := make(map[string]persistence.ShipAssignmentInfo, len(infos))
	for _, info := range infos {
		infoMap[info.ShipSymbol] = info
	}

	rows := buildShipRows(ships, infoMap, now)

	return renderShipList(rows, jsonOut)
}

// newShipAssignmentStore bootstraps a DB-backed assignment lister and player
// repository for resolving a numeric player ID from CLI flags.
func newShipAssignmentStore() (shipAssignmentLister, *persistence.GormPlayerRepository, error) {
	cfg, err := config.LoadConfig("")
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load config: %w", err)
	}

	db, err := database.NewConnection(&cfg.Database)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	return persistence.NewShipAssignmentRepository(db), persistence.NewGormPlayerRepository(db), nil
}

// resolveShipListPlayerID resolves a numeric player ID from the CLI's
// identifier, looking it up by agent symbol when only that was supplied.
func resolveShipListPlayerID(ctx context.Context, playerRepo *persistence.GormPlayerRepository, playerIdent *PlayerIdentifier) (int, error) {
	if playerIdent.PlayerID > 0 {
		return playerIdent.PlayerID, nil
	}

	p, err := playerRepo.FindByAgentSymbol(ctx, playerIdent.AgentSymbol)
	if err != nil {
		return 0, fmt.Errorf("failed to resolve player from agent symbol: %w", err)
	}

	return p.ID.Value(), nil
}

// newShipListCommand creates the ship list subcommand
func newShipListCommand() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all ships for a player",
		Long: `List all ships owned by a player/agent.

Shows ship symbol, location, navigation status, fuel, cargo levels, role,
owning assignment (container id or "-"), and cache age.

Examples:
  spacetraders ship list --player-id 1
  spacetraders ship list --agent ENDURANCE
  spacetraders ship list --player-id 1 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Get daemon client
			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			// Call daemon via gRPC
			ctx := context.Background()
			playerIDPtr, agentSymbol := playerPointers(playerIdent)

			response, err := client.ListShips(ctx, playerIDPtr, agentSymbol)
			if err != nil {
				return fmt.Errorf("failed to list ships: %w", err)
			}

			if len(response.Ships) == 0 {
				fmt.Println("No ships found.")
				return nil
			}

			lister, playerRepo, err := newShipAssignmentStore()
			if err != nil {
				return err
			}

			resolvedPlayerID, err := resolveShipListPlayerID(ctx, playerRepo, playerIdent)
			if err != nil {
				return err
			}

			return runShipList(ctx, response.Ships, lister, resolvedPlayerID, time.Now(), jsonOut)
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")

	return cmd
}

// newShipInfoCommand creates the ship info subcommand
func newShipInfoCommand() *cobra.Command {
	var shipSymbol string

	cmd := &cobra.Command{
		Use:   "info",
		Short: "Show detailed ship information",
		Long: `Show detailed information about a specific ship.

Displays ship location, navigation status, fuel levels, cargo capacity,
cargo contents, and engine specifications.

Examples:
  spacetraders ship info --ship ENDURANCE-1 --player-id 1
  spacetraders ship info --ship ENDURANCE-1 --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}

			// Get daemon client
			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			// Call daemon via gRPC
			ctx := context.Background()
			playerID, agentSymbol := playerPointers(playerIdent)

			response, err := client.GetShip(ctx, shipSymbol, playerID, agentSymbol)
			if err != nil {
				return fmt.Errorf("failed to get ship: %w", err)
			}

			s := response.Ship

			// Display ship info
			fmt.Printf("Ship Information\n")
			fmt.Printf("================\n\n")
			fmt.Printf("Ship Symbol:    %s\n", s.Symbol)
			fmt.Printf("Role:           %s\n", s.Role)
			fmt.Printf("Location:       %s\n", s.Location)
			fmt.Printf("Nav Status:     %s\n", s.NavStatus)
			fmt.Printf("Fuel:           %d / %d\n", s.FuelCurrent, s.FuelCapacity)
			fmt.Printf("Cargo:          %d / %d units\n", s.CargoUnits, s.CargoCapacity)
			fmt.Printf("Engine Speed:   %d\n", s.EngineSpeed)

			// Show cargo contents if any
			if s.CargoUnits > 0 {
				fmt.Printf("\nCargo Contents:\n")
				for _, item := range s.CargoInventory {
					fmt.Printf("  - %s: %d units (%s)\n", item.Name, item.Units, item.Symbol)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol (required)")

	return cmd
}

// newShipRefreshCommand creates the ship refresh subcommand
func newShipRefreshCommand() *cobra.Command {
	var shipSymbol string

	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Force-resync a ship's cached state from the server",
		Long: `Force a fresh GET /my/ships/<symbol> against the SpaceTraders API and
overwrite the daemon's local cargo + nav cache with the server response.

Use this to reconcile a desynced ship cache (e.g. phantom cargo or a stale
position) without restarting the daemon and without moving the ship. The
reconciled state is printed on success.

Examples:
  spacetraders ship refresh --ship ENDURANCE-1 --player-id 1
  spacetraders ship refresh --ship ENDURANCE-1 --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}

			// Get daemon client
			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			playerID, agentSymbol := playerPointers(playerIdent)

			response, err := client.RefreshShip(ctx, shipSymbol, playerID, agentSymbol)
			if err != nil {
				return fmt.Errorf("failed to refresh ship: %w", err)
			}

			s := response.Ship

			fmt.Printf("✓ Ship state reconciled from server\n")
			fmt.Printf("================================\n\n")
			fmt.Printf("Ship Symbol:    %s\n", s.Symbol)
			fmt.Printf("Role:           %s\n", s.Role)
			fmt.Printf("Location:       %s\n", s.Location)
			fmt.Printf("Nav Status:     %s\n", s.NavStatus)
			fmt.Printf("Fuel:           %d / %d\n", s.FuelCurrent, s.FuelCapacity)
			fmt.Printf("Cargo:          %d / %d units\n", s.CargoUnits, s.CargoCapacity)
			fmt.Printf("Engine Speed:   %d\n", s.EngineSpeed)

			if s.CargoUnits > 0 {
				fmt.Printf("\nCargo Contents:\n")
				for _, item := range s.CargoInventory {
					fmt.Printf("  - %s: %d units (%s)\n", item.Name, item.Units, item.Symbol)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol (required)")

	return cmd
}

// newShipNavigateCommand creates the ship navigate subcommand
func newShipNavigateCommand() *cobra.Command {
	var (
		shipSymbol  string
		destination string
	)

	cmd := &cobra.Command{
		Use:   "navigate",
		Short: "Navigate a ship to a destination waypoint",
		Long: `Navigate a ship to a destination waypoint within the same system.

The daemon will automatically:
- Orbit the ship if docked
- Plan the optimal route (including refuel stops if needed)
- Navigate to the destination
- Return a container ID for tracking progress

Examples:
  spacetraders ship navigate --ship AGENT-1 --destination X1-GZ7-B1 --player-id 1
  spacetraders ship navigate --ship SCOUT-2 --destination X1-GZ7-A1 --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate flags
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}
			if destination == "" {
				return fmt.Errorf("--destination flag is required")
			}

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

			// Execute navigate command
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			result, err := client.NavigateShip(ctx, shipSymbol, destination, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("navigation failed: %w", err)
			}

			// Display result
			fmt.Println("✓ Navigation started successfully")
			fmt.Printf("  Container ID:     %s\n", result.ContainerID)
			fmt.Printf("  Ship:             %s\n", result.ShipSymbol)
			fmt.Printf("  Destination:      %s\n", result.Destination)
			fmt.Printf("  Status:           %s\n", result.Status)
			fmt.Printf("  Estimated Time:   %d seconds\n", result.EstimatedTime)
			fmt.Printf("\nTrack progress with: spacetraders container logs %s\n", result.ContainerID)

			return nil
		},
	}

	// Command-specific flags
	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol to navigate (required)")
	cmd.Flags().StringVar(&destination, "destination", "", "Destination waypoint symbol (required)")

	return cmd
}

// newShipDockCommand creates the ship dock subcommand
func newShipDockCommand() *cobra.Command {
	var shipSymbol string

	cmd := &cobra.Command{
		Use:   "dock",
		Short: "Dock a ship at its current location",
		Long: `Dock a ship at its current location.
Ship must be in orbit to dock.

Examples:
  spacetraders ship dock --ship AGENT-1 --player-id 1
  spacetraders ship dock --ship ENDURANCE-1 --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}

			// Resolve player from flags or defaults
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

			result, err := client.DockShip(ctx, shipSymbol, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("dock failed: %w", err)
			}

			fmt.Println("✓ Dock operation started")
			fmt.Printf("  Container ID: %s\n", result.ContainerID)
			fmt.Printf("  Ship:         %s\n", result.ShipSymbol)
			fmt.Printf("  Status:       %s\n", result.Status)

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol to dock (required)")

	return cmd
}

// newShipOrbitCommand creates the ship orbit subcommand
func newShipOrbitCommand() *cobra.Command {
	var shipSymbol string

	cmd := &cobra.Command{
		Use:   "orbit",
		Short: "Put a ship into orbit from docked position",
		Long: `Put a ship into orbit from its current docked position.
Ship must be docked to orbit.

Examples:
  spacetraders ship orbit --ship AGENT-1 --player-id 1
  spacetraders ship orbit --ship ENDURANCE-1 --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}

			// Resolve player from flags or defaults
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

			result, err := client.OrbitShip(ctx, shipSymbol, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("orbit failed: %w", err)
			}

			fmt.Println("✓ Orbit operation started")
			fmt.Printf("  Container ID: %s\n", result.ContainerID)
			fmt.Printf("  Ship:         %s\n", result.ShipSymbol)
			fmt.Printf("  Status:       %s\n", result.Status)

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol to orbit (required)")

	return cmd
}

// newShipRefuelCommand creates the ship refuel subcommand
func newShipRefuelCommand() *cobra.Command {
	var (
		shipSymbol string
		units      int
	)

	cmd := &cobra.Command{
		Use:   "refuel",
		Short: "Refuel a ship at its current location",
		Long: `Refuel a ship at its current location.
Ship must be docked at a waypoint with fuel available.

Examples:
  spacetraders ship refuel --ship AGENT-1 --player-id 1
  spacetraders ship refuel --ship AGENT-1 --units 100 --player-id 1
  spacetraders ship refuel --ship ENDURANCE-1 --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}

			// Resolve player from flags or defaults
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

			var unitsPtr *int
			if units > 0 {
				unitsPtr = &units
			}

			result, err := client.RefuelShip(ctx, shipSymbol, playerIdent.PlayerID, playerIdent.AgentSymbol, unitsPtr)
			if err != nil {
				return fmt.Errorf("refuel failed: %w", err)
			}

			fmt.Println("✓ Refuel operation started")
			fmt.Printf("  Container ID:  %s\n", result.ContainerID)
			fmt.Printf("  Ship:          %s\n", result.ShipSymbol)
			fmt.Printf("  Fuel Added:    %d\n", result.FuelAdded)
			fmt.Printf("  Credits Cost:  %d\n", result.CreditsCost)
			fmt.Printf("  Status:        %s\n", result.Status)

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol to refuel (required)")
	cmd.Flags().IntVar(&units, "units", 0, "Specific fuel units to purchase (omit for full tank)")

	return cmd
}

// newShipJumpCommand creates the ship jump subcommand
func newShipJumpCommand() *cobra.Command {
	var (
		shipSymbol        string
		destinationSystem string
	)

	cmd := &cobra.Command{
		Use:   "jump",
		Short: "Jump a ship to a different star system via jump gate",
		Long: `Jump a ship to a different star system using a jump gate.

If the ship is not currently at a jump gate, it will automatically navigate to
the nearest jump gate in the current system before jumping.

The ship must have a jump drive module installed to use this command.

Examples:
  spacetraders ship jump --ship PROBE-1 --system X1-ALPHA --player-id 1
  spacetraders ship jump --ship PROBE-1 --system X1-ALPHA --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}
			if destinationSystem == "" {
				return fmt.Errorf("--system flag is required")
			}

			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			fmt.Printf("Initiating jump for ship %s to system %s...\n", shipSymbol, destinationSystem)

			result, err := client.JumpShip(ctx, shipSymbol, destinationSystem, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("jump failed: %w", err)
			}

			if !result.Success {
				return fmt.Errorf("jump failed: %s", result.Error)
			}

			// Display results
			if result.NavigatedToGate {
				fmt.Printf("✓ Navigated to jump gate: %s\n", result.JumpGateSymbol)
			}

			fmt.Printf("✓ %s\n", result.Message)

			if result.CooldownSeconds > 0 {
				fmt.Printf("⏱  Jump cooldown: %d seconds\n", result.CooldownSeconds)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol to jump (required)")
	cmd.Flags().StringVar(&destinationSystem, "system", "", "Destination system symbol (e.g., X1-ALPHA) (required)")

	return cmd
}

// newShipSellCommand creates the ship sell subcommand
func newShipSellCommand() *cobra.Command {
	var (
		shipSymbol string
		goodSymbol string
		units      int
	)

	cmd := &cobra.Command{
		Use:   "sell",
		Short: "Sell cargo from a ship",
		Long: `Sell cargo from a ship at its current location.
Ship must be docked at a marketplace.

Examples:
  spacetraders ship sell --ship AGENT-1 --good IRON_ORE --units 50 --player-id 1
  spacetraders ship sell --ship ENDURANCE-1 --good IRON_ORE --units 100 --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}
			if goodSymbol == "" {
				return fmt.Errorf("--good flag is required")
			}
			if units <= 0 {
				return fmt.Errorf("--units must be greater than 0")
			}

			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			// Load config and connect to database
			cfg, err := config.LoadConfig("")
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			db, err := database.NewConnection(&cfg.Database)
			if err != nil {
				return fmt.Errorf("failed to connect to database: %w", err)
			}

			// Create dependencies
			playerRepo := persistence.NewGormPlayerRepository(db)
			apiClient := api.NewSpaceTradersClient()
			waypointRepo := persistence.NewGormWaypointRepository(db)
			systemGraphRepo := persistence.NewGormSystemGraphRepository(db)
			graphBuilder := api.NewGraphBuilder(apiClient, playerRepo, waypointRepo)
			graphService := graph.NewGraphService(systemGraphRepo, waypointRepo, graphBuilder)
			shipRepo := api.NewShipRepository(apiClient, playerRepo, waypointRepo, graphService, db, nil) // nil = use RealClock
			marketRepo := persistence.NewMarketRepository(db)

			// Create mediator with ledger handlers registered
			transactionRepo := persistence.NewGormTransactionRepository(db)
			playerResolver := player.NewPlayerResolver(playerRepo)
			registry := setup.NewHandlerRegistry(
				transactionRepo,
				playerResolver,
				nil, // clock (defaults to real clock)
				nil, // shipRepo (not needed for this CLI command)
				nil, // daemonClient (not needed for this CLI command)
				nil, // storageOpRepo (not needed for this CLI command)
				nil, // storageCoordinator (not needed for this CLI command)
				nil, // waypointRepo (not needed for this CLI command)
				nil, // apiClient (not needed for this CLI command)
			)
			mediator, err := registry.CreateConfiguredMediator()
			if err != nil {
				return fmt.Errorf("failed to create mediator: %w", err)
			}

			// Create handler (nil marketRefresher - CLI doesn't refresh market data after transactions)
			handler := shipCargo.NewSellCargoHandler(shipRepo, playerRepo, apiClient, marketRepo, mediator, nil)

			// Resolve player ID and load player token
			ctx := context.Background()
			var resolvedPlayerID int
			var playerToken string

			if playerIdent.PlayerID > 0 {
				resolvedPlayerID = playerIdent.PlayerID
				// Load player to get token
				player, err := playerRepo.FindByID(ctx, shared.MustNewPlayerID(resolvedPlayerID))
				if err != nil {
					return fmt.Errorf("failed to load player: %w", err)
				}
				playerToken = player.Token
			} else {
				// Look up player by agent symbol
				player, err := playerRepo.FindByAgentSymbol(ctx, playerIdent.AgentSymbol)
				if err != nil {
					return fmt.Errorf("failed to resolve player from agent symbol: %w", err)
				}
				resolvedPlayerID = player.ID.Value()
				playerToken = player.Token
			}

			// Add player token to context for ledger recording
			ctx = auth.WithPlayerToken(ctx, playerToken)

			// Execute command
			response, err := handler.Handle(ctx, &shipCargo.SellCargoCommand{
				ShipSymbol: shipSymbol,
				GoodSymbol: goodSymbol,
				Units:      units,
				PlayerID:   shared.MustNewPlayerID(resolvedPlayerID),
			})
			if err != nil {
				return fmt.Errorf("sell cargo command failed: %w", err)
			}

			result, ok := response.(*shipCargo.SellCargoResponse)
			if !ok {
				return fmt.Errorf("unexpected response type")
			}

			// Display success
			fmt.Println("✓ Cargo sold successfully")
			fmt.Printf("  Ship:          %s\n", shipSymbol)
			fmt.Printf("  Good:          %s\n", goodSymbol)
			fmt.Printf("  Units Sold:    %d\n", result.UnitsSold)
			fmt.Printf("  Total Revenue: %d credits\n", result.TotalRevenue)

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol to sell from (required)")
	cmd.Flags().StringVar(&goodSymbol, "good", "", "Trade good symbol to sell (required)")
	cmd.Flags().IntVar(&units, "units", 0, "Number of units to sell (required)")

	return cmd
}
