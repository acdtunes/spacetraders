package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
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

	cmd.AddCommand(newShipListCommand())
	cmd.AddCommand(newShipInfoCommand())
	cmd.AddCommand(newShipRefreshCommand())
	cmd.AddCommand(newShipReserveCommand())
	cmd.AddCommand(newShipReleaseCommand())
	cmd.AddCommand(newShipReserveCargoCommand())
	cmd.AddCommand(newShipUnreserveCargoCommand())
	cmd.AddCommand(newShipReservedCargoCommand())
	cmd.AddCommand(newShipNavigateCommand())
	cmd.AddCommand(newShipRouteCommand())
	cmd.AddCommand(newShipWarpCommand())
	cmd.AddCommand(newShipDockCommand())
	cmd.AddCommand(newShipOrbitCommand())
	cmd.AddCommand(newShipRefuelCommand())
	cmd.AddCommand(newShipJumpCommand())
	cmd.AddCommand(newShipSellCommand())
	cmd.AddCommand(newShipBuyCommand())
	cmd.AddCommand(newShipJettisonCommand())
	cmd.AddCommand(newShipOutfitCommand())
	cmd.AddCommand(newShipTransferCommand())

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
	// Fleet is the ship's permanent dedicated-fleet tag,
	// or "-" when unreserved. This is the sp-lybx-prevention column: it
	// surfaces a hull pinned to the wrong fleet at purchase time without
	// requiring a per-ship cross-check against `fleet list`.
	Fleet      string `json:"fleet"`
	Assignment string `json:"assignment"`
	CacheAge   string `json:"cacheAge"`
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
// per-ship assignment info, defaulting role/fleet/assignment/cache age to
// "-" for ships that have no assignment row. Rows are returned sorted by
// ship symbol in natural order (TORWIND-2 before TORWIND-10) so a fleet
// roster reads in the order a human expects.
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
			Fleet:         "-",
			Assignment:    "-",
			CacheAge:      "-",
		}

		if info, ok := infos[s.Symbol]; ok {
			if info.Role != "" {
				row.Role = info.Role
			}
			if info.DedicatedFleet != "" {
				row.Fleet = info.DedicatedFleet
			}
			switch {
			case info.AssignmentOwner == string(navigation.AssignmentOwnerCaptain):
				// A captain reservation has no ContainerID (it was
				// never a container claim), so without this branch it would
				// fall through to "-" and look identical to a genuinely idle,
				// unassigned ship. Show the reservation itself, plus the
				// reason when the captain gave one.
				if info.AssignmentReason != "" {
					row.Assignment = fmt.Sprintf("captain (%s)", info.AssignmentReason)
				} else {
					row.Assignment = "captain"
				}
			case info.ContainerID != "":
				row.Assignment = info.ContainerID
			}
			if !info.SyncedAt.IsZero() {
				row.CacheAge = humanizeDuration(now.Sub(info.SyncedAt))
			}
		}

		rows = append(rows, row)
	}

	sortShipListRowsNatural(rows)

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
	fmt.Fprintln(w, "SHIP SYMBOL\tLOCATION\tSTATUS\tFUEL\tCARGO\tSPEED\tROLE\tFLEET\tASSIGNMENT\tCACHE AGE")
	fmt.Fprintln(w, "-----------\t--------\t------\t----\t-----\t-----\t----\t-----\t----------\t---------")

	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d/%d\t%d/%d\t%d\t%s\t%s\t%s\t%s\n",
			r.Symbol,
			r.Location,
			r.NavStatus,
			r.FuelCurrent,
			r.FuelCapacity,
			r.CargoUnits,
			r.CargoCapacity,
			r.EngineSpeed,
			r.Role,
			r.Fleet,
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
	db, err := openDatabase()
	if err != nil {
		return nil, nil, err
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
dedicated fleet (permanent pin, e.g. "contract", or "-" if unpinned), owning
assignment (container id or "-"), and cache age. Rows are sorted by ship
symbol in natural order (TORWIND-2 before TORWIND-10).

The FLEET column is a one-glance check for a hull pinned to the wrong fleet
at purchase time (the sp-lybx incident) — no need to cross-check each ship
against 'fleet list' individually.

Examples:
  spacetraders ship list --player-id 1
  spacetraders ship list --agent ENDURANCE
  spacetraders ship list --player-id 1 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			ctx := context.Background()
			response, err := client.ListShips(ctx, playerIdent)
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

			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			ctx := context.Background()
			response, err := client.GetShip(ctx, shipSymbol, playerIdent)
			if err != nil {
				return fmt.Errorf("failed to get ship: %w", err)
			}

			s := response.Ship

			fmt.Printf("Ship Information\n")
			fmt.Printf("================\n\n")
			fmt.Printf("Ship Symbol:    %s\n", s.Symbol)
			fmt.Printf("Role:           %s\n", s.Role)
			fmt.Printf("Location:       %s\n", s.Location)
			fmt.Printf("Nav Status:     %s\n", s.NavStatus)
			fmt.Printf("Fuel:           %d / %d\n", s.FuelCurrent, s.FuelCapacity)
			fmt.Printf("Cargo:          %d / %d units\n", s.CargoUnits, s.CargoCapacity)
			fmt.Printf("Engine Speed:   %d\n", s.EngineSpeed)

			printShipPowerSlots(s)

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

// printShipPowerSlots prints a ship's reactor power / module slot / mounting
// point / crew budget (sp-el60), computed offline from cached ship state.
// Reactors, frames, and crew capacity have no swap endpoint in the
// SpaceTraders API, so this budget is permanent for the life of the hull.
func printShipPowerSlots(s *pb.ShipDetail) {
	fmt.Printf("\nPower / Slots\n")
	fmt.Printf("-------------\n")
	fmt.Printf("Reactor:        %s (%s)\n", s.ReactorSymbol, s.ReactorName)
	fmt.Printf("Power:          %d / %d used (%d free)\n",
		s.PowerUsed, s.ReactorPowerOutput, s.ReactorPowerOutput-s.PowerUsed)
	fmt.Printf("Module Slots:   %d / %d used (%d free)\n",
		s.ModuleSlotsUsed, s.ModuleSlots, s.ModuleSlots-s.ModuleSlotsUsed)
	fmt.Printf("Mounting Points: %d / %d used (%d free)\n",
		s.MountingPointsUsed, s.MountingPoints, s.MountingPoints-s.MountingPointsUsed)
	fmt.Printf("Crew:           %d current, %d required, %d capacity\n",
		s.CrewCurrent, s.CrewRequired, s.CrewCapacity)
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

			response, err := client.RefreshShip(ctx, shipSymbol, playerIdent)
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

			printShipPowerSlots(s)

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
