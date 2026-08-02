package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

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
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}
			if destination == "" {
				return fmt.Errorf("--destination flag is required")
			}

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

			result, err := client.NavigateShip(ctx, shipSymbol, destination, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("navigation failed: %w", err)
			}

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

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol to navigate (required)")
	cmd.Flags().StringVar(&destination, "destination", "", "Destination waypoint symbol (required)")

	return cmd
}

// newShipRouteCommand creates the ship route subcommand
func newShipRouteCommand() *cobra.Command {
	var (
		shipSymbol  string
		destination string
	)

	cmd := &cobra.Command{
		Use:   "route",
		Short: "Route a ship point-to-point to a waypoint in ANY reachable system",
		Long: `Route a ship to a destination waypoint in any reachable system, crossing
jump gates as needed.

Unlike 'ship navigate' (which is in-system only and fails cross-system with
"waypoint not found in cache for system X") and 'ship jump' (a single gate hop
that requires the ship already at the gate), 'ship route' reuses the same
multi-jump travel machinery the trade/tour/warehouse workflows use internally.

The daemon will automatically:
- Orbit the ship if docked
- Fly to the source jump gate if not already there
- Resolve and fly the multi-hop gate path (with per-hop cooldown waits)
- Fly the final gate-to-waypoint hop at the destination
- Return a container ID for tracking progress

Examples:
  spacetraders ship route --ship ENDURANCE-7 --destination X1-JP61-B1 --player-id 1
  spacetraders ship route --ship SPARE-2 --destination X1-FAR-A1 --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}
			if destination == "" {
				return fmt.Errorf("--destination flag is required")
			}

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

			result, err := client.RouteShip(ctx, shipSymbol, destination, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("route failed: %w", err)
			}

			fmt.Println("✓ Route started successfully")
			fmt.Printf("  Container ID:     %s\n", result.ContainerID)
			fmt.Printf("  Ship:             %s\n", result.ShipSymbol)
			fmt.Printf("  Destination:      %s\n", result.Destination)
			fmt.Printf("  Status:           %s\n", result.Status)
			fmt.Printf("\nTrack progress with: spacetraders container logs %s\n", result.ContainerID)

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol to route (required)")
	cmd.Flags().StringVar(&destination, "destination", "", "Destination waypoint symbol in any reachable system (required)")

	return cmd
}

// newShipWarpCommand creates the ship warp subcommand
func newShipWarpCommand() *cobra.Command {
	var (
		shipSymbol  string
		destination string
	)

	cmd := &cobra.Command{
		Use:   "warp",
		Short: "Warp a ship OFF the jump-gate network to a waypoint in another system",
		Long: `Warp a ship directly to a waypoint in another system, without a jump gate.

Unlike 'ship route' (which crosses jump gates) and 'ship jump' (a single gate hop),
warp reaches systems the gate network does not connect. The ship must have a warp
drive module installed - only an explorer hull carries one.

The daemon will automatically:
- Orbit the ship if docked
- Enforce the fuel-safety guard (topping off first when the origin sells fuel)
- Warp to the destination and chart the arrival system
- Return a container ID for tracking progress

Two refusals are possible and both come BEFORE any warp is attempted, leaving the
ship exactly where it is: the ship has no warp drive, or the leg would strand it
(reported with the fuel required, available and tank capacity). Read either one with
'spacetraders container logs <container-id>'.

Examples:
  spacetraders ship warp --ship TORWIND-F6 --destination X1-TY66-A1 --player-id 1
  spacetraders ship warp --ship EXPLORER-1 --destination X1-FAR-A1 --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}
			if destination == "" {
				return fmt.Errorf("--destination flag is required")
			}

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

			result, err := client.WarpShip(ctx, shipSymbol, destination, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("warp failed: %w", err)
			}

			fmt.Println("✓ Warp started successfully")
			fmt.Printf("  Container ID:     %s\n", result.ContainerID)
			fmt.Printf("  Ship:             %s\n", result.ShipSymbol)
			fmt.Printf("  Destination:      %s\n", result.Destination)
			fmt.Printf("  Status:           %s\n", result.Status)
			fmt.Printf("\nTrack progress with: spacetraders container logs %s\n", result.ContainerID)
			fmt.Println("A refusal (no warp drive, or the leg would strand the ship) is reported there in full.")

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol to warp - must have a warp drive (required)")
	cmd.Flags().StringVar(&destination, "destination", "", "Destination waypoint symbol in another system (required)")

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
