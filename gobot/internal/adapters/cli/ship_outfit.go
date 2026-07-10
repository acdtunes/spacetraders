package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// newShipOutfitCommand builds the `ship outfit` command group: install / remove
// / list modules on a ship. Module install/remove changes ship state (cargo
// capacity), so the verbs dispatch to the daemon (RULING #3) rather than
// calling the API directly.
func newShipOutfitCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "outfit",
		Short: "Install, remove, or list ship modules",
		Long: `Install, remove, or list ship modules (e.g. MODULE_CARGO_HOLD_III).

Installing a module requires it to already be in the ship's cargo (buy it as a
good at a shipyard first). The daemon atomically claims the hull, gates the
shipyard modification fee on the working-capital reserve, docks, installs, and
persists the ship's new cargo capacity.

Examples:
  spacetraders ship outfit install --ship ENDURANCE-1 --module MODULE_CARGO_HOLD_III --agent ENDURANCE
  spacetraders ship outfit remove  --ship ENDURANCE-1 --module MODULE_CARGO_HOLD_III --agent ENDURANCE
  spacetraders ship outfit list    --ship ENDURANCE-1 --agent ENDURANCE`,
	}

	cmd.AddCommand(newShipOutfitInstallCommand())
	cmd.AddCommand(newShipOutfitRemoveCommand())
	cmd.AddCommand(newShipOutfitListCommand())

	return cmd
}

func newShipOutfitInstallCommand() *cobra.Command {
	var (
		shipSymbol   string
		moduleSymbol string
	)

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install a module (from the ship's cargo) onto a ship",
		Long: `Install a module onto a ship. The module must already be in the ship's cargo.

Examples:
  spacetraders ship outfit install --ship ENDURANCE-1 --module MODULE_CARGO_HOLD_III --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}
			if moduleSymbol == "" {
				return fmt.Errorf("--module flag is required")
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

			result, err := client.InstallModule(ctx, shipSymbol, moduleSymbol, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("install failed: %w", err)
			}
			if result.Error != "" {
				return fmt.Errorf("install failed: %s", result.Error)
			}

			fmt.Printf("✓ Installed %s on %s\n", result.ModuleSymbol, result.ShipSymbol)
			fmt.Printf("  Fee:             %d\n", result.Fee)
			fmt.Printf("  Cargo capacity:  %d\n", result.CargoCapacity)
			printModuleList("  Installed modules:", result.Modules)

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol to install onto (required)")
	cmd.Flags().StringVar(&moduleSymbol, "module", "", "Module symbol to install, e.g. MODULE_CARGO_HOLD_III (required)")

	return cmd
}

func newShipOutfitRemoveCommand() *cobra.Command {
	var (
		shipSymbol   string
		moduleSymbol string
	)

	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Remove an installed module from a ship (back into cargo)",
		Long: `Remove an installed module from a ship. The module is placed back into the
ship's cargo.

Examples:
  spacetraders ship outfit remove --ship ENDURANCE-1 --module MODULE_CARGO_HOLD_III --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}
			if moduleSymbol == "" {
				return fmt.Errorf("--module flag is required")
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

			result, err := client.RemoveModule(ctx, shipSymbol, moduleSymbol, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("remove failed: %w", err)
			}
			if result.Error != "" {
				return fmt.Errorf("remove failed: %s", result.Error)
			}

			fmt.Printf("✓ Removed %s from %s\n", result.ModuleSymbol, result.ShipSymbol)
			fmt.Printf("  Fee:             %d\n", result.Fee)
			fmt.Printf("  Cargo capacity:  %d\n", result.CargoCapacity)
			printModuleList("  Remaining modules:", result.Modules)

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol to remove from (required)")
	cmd.Flags().StringVar(&moduleSymbol, "module", "", "Module symbol to remove (required)")

	return cmd
}

func newShipOutfitListCommand() *cobra.Command {
	var shipSymbol string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the modules installed on a ship",
		Long: `List the modules currently installed on a ship.

Examples:
  spacetraders ship outfit list --ship ENDURANCE-1 --agent ENDURANCE`,
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

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			result, err := client.ListShipModules(ctx, shipSymbol, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("list modules failed: %w", err)
			}
			if result.Error != "" {
				return fmt.Errorf("list modules failed: %s", result.Error)
			}

			fmt.Printf("Modules on %s:\n", result.ShipSymbol)
			if len(result.Modules) == 0 {
				fmt.Println("  (none)")
				return nil
			}
			for _, m := range result.Modules {
				printModuleLine(m)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol whose modules to list (required)")

	return cmd
}

// printModuleList prints a header followed by one line per module (or "(none)").
func printModuleList(header string, modules []ModuleInfoDTO) {
	fmt.Println(header)
	if len(modules) == 0 {
		fmt.Println("    (none)")
		return
	}
	for _, m := range modules {
		printModuleLine(m)
	}
}

// printModuleLine prints a single module, including its capacity bonus when non-zero.
func printModuleLine(m ModuleInfoDTO) {
	if m.Capacity > 0 {
		fmt.Printf("    - %s (capacity %d)  %s\n", m.Symbol, m.Capacity, m.Name)
		return
	}
	fmt.Printf("    - %s  %s\n", m.Symbol, m.Name)
}
