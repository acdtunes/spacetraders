package cli

import (
	"context"
	"fmt"
	"strings"
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
	var (
		shipSymbol      string
		candidateSymbol string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the modules installed on a ship",
		Long: `List the modules currently installed on a ship, along with its
reactor power / module slot / crew budget summary.

Power, slots, and crew are computed offline from the ship's last-synced
state (sp-el60) - reactors, frames, and crew capacity have no swap endpoint
in the SpaceTraders API, so these budgets are permanent for the life of the
hull and don't require a live trial-and-error install to check.

Pass --candidate to check offline whether a not-yet-installed module would
fit. The candidate's own power/crew/slot requirements are resolved
automatically from another ship in the fleet that has it installed (sp-el60
acceptance fix) - there is no catalog of unowned module specs to take them
from on the command line, so there are no --power/--crew/--slots flags. If
no ship anywhere has ever carried the candidate symbol, the requirements are
reported as unknown and the verdict is UNKNOWN-REQUIREMENTS, never a
trivially-satisfied CAN-INSTALL.

Examples:
  spacetraders ship outfit list --ship ENDURANCE-1 --agent ENDURANCE
  spacetraders ship outfit list --ship ENDURANCE-1 --agent ENDURANCE \
    --candidate MODULE_CARGO_HOLD_III`,
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

			result, err := client.ListShipModules(ctx, shipSymbol, playerIdent.PlayerID, playerIdent.AgentSymbol,
				candidateSymbol)
			if err != nil {
				return fmt.Errorf("list modules failed: %w", err)
			}
			if result.Error != "" {
				return fmt.Errorf("list modules failed: %s", result.Error)
			}

			fmt.Printf("Modules on %s:\n", result.ShipSymbol)
			if len(result.Modules) == 0 {
				fmt.Println("  (none)")
			} else {
				for _, m := range result.Modules {
					printModuleLine(m)
				}
			}

			printPowerSlotsSummary(result)

			if result.Feasibility != nil {
				fmt.Println("Feasibility:")
				fmt.Printf("    %s\n", formatRequirementsLine(result.Feasibility))
				fmt.Printf("    %s\n", formatFeasibility(result.Feasibility))
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol whose modules to list (required)")
	cmd.Flags().StringVar(&candidateSymbol, "candidate", "", "Symbol of a not-yet-installed module to check offline install feasibility for")

	return cmd
}

// printPowerSlotsSummary prints the ship's reactor power / module slot /
// mounting point / crew budget (sp-el60), computed offline from cached ship
// state.
func printPowerSlotsSummary(result *ShipModulesResponse) {
	fmt.Println("Power / slots:")
	fmt.Printf("    Power:           %d/%d used (%d free)\n",
		result.PowerUsed, result.ReactorPowerOutput, result.ReactorPowerOutput-result.PowerUsed)
	fmt.Printf("    Module slots:    %d/%d used (%d free)\n",
		result.ModuleSlotsUsed, result.ModuleSlots, result.ModuleSlots-result.ModuleSlotsUsed)
	fmt.Printf("    Mounting points: %d/%d used (%d free)\n",
		result.MountingPointsUsed, result.MountingPoints, result.MountingPoints-result.MountingPointsUsed)
	fmt.Printf("    Crew:            %d current, %d required, %d capacity\n",
		result.CrewCurrent, result.CrewRequired, result.CrewCapacity)
}

// formatRequirementsLine renders the candidate's resolved power/crew/slot
// requirements, or an explicit "requirements: unknown" when no ship in the
// fleet has ever carried the symbol (sp-el60 acceptance fix). The
// requirements a verdict was checked against must always be visible in the
// output, never silently omitted - the original acceptance bug printed a
// CAN-INSTALL verdict with no requirements line at all.
func formatRequirementsLine(f *ModuleFeasibilityDTO) string {
	if !f.RequirementsKnown {
		return fmt.Sprintf("%s requirements: unknown", f.CandidateSymbol)
	}
	return fmt.Sprintf("%s requirements: power=%d crew=%d slots=%d",
		f.CandidateSymbol, f.RequirementsPower, f.RequirementsCrew, f.RequirementsSlots)
}

// formatFeasibility renders an offline install-feasibility verdict as a
// single CAN-INSTALL / UNKNOWN-REQUIREMENTS / *-SHORT-N line (sp-el60).
// UNKNOWN-REQUIREMENTS is checked first and unconditionally: a candidate
// whose requirements could not be resolved must never print CAN-INSTALL,
// even though the DTO's CanInstall bool is already guaranteed false by the
// domain layer for this case - the output must say so explicitly, per the
// sp-el60 acceptance fix, not just fail closed silently.
func formatFeasibility(f *ModuleFeasibilityDTO) string {
	if !f.RequirementsKnown {
		return fmt.Sprintf("%s: UNKNOWN-REQUIREMENTS", f.CandidateSymbol)
	}
	if f.CanInstall {
		return fmt.Sprintf("%s: CAN-INSTALL", f.CandidateSymbol)
	}
	var gaps []string
	if f.PowerShort > 0 {
		gaps = append(gaps, fmt.Sprintf("POWER-SHORT-%d", f.PowerShort))
	}
	if f.SlotShort > 0 {
		gaps = append(gaps, fmt.Sprintf("SLOT-SHORT-%d", f.SlotShort))
	}
	if f.CrewShort > 0 {
		gaps = append(gaps, fmt.Sprintf("CREW-SHORT-%d", f.CrewShort))
	}
	return fmt.Sprintf("%s: %s", f.CandidateSymbol, strings.Join(gaps, ", "))
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

// printModuleLine prints a single module, including its capacity bonus (when
// non-zero) and its power/crew/slots install requirements (sp-el60, when any
// are non-zero).
func printModuleLine(m ModuleInfoDTO) {
	suffix := ""
	if req := formatModuleRequirements(m); req != "" {
		suffix = "  requires: " + req
	}
	if m.Capacity > 0 {
		fmt.Printf("    - %s (capacity %d)  %s%s\n", m.Symbol, m.Capacity, m.Name, suffix)
		return
	}
	fmt.Printf("    - %s  %s%s\n", m.Symbol, m.Name, suffix)
}

// formatModuleRequirements renders a module's power/crew/slots install
// requirements as a compact "power N, crew N, slots N" fragment, omitting
// any that are zero. Returns "" when all three are zero.
func formatModuleRequirements(m ModuleInfoDTO) string {
	var parts []string
	if m.Power > 0 {
		parts = append(parts, fmt.Sprintf("power %d", m.Power))
	}
	if m.Crew > 0 {
		parts = append(parts, fmt.Sprintf("crew %d", m.Crew))
	}
	if m.Slots > 0 {
		parts = append(parts, fmt.Sprintf("slots %d", m.Slots))
	}
	return strings.Join(parts, ", ")
}
