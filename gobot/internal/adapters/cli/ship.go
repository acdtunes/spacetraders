package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
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
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
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
	cmd.AddCommand(newShipReserveCommand())
	cmd.AddCommand(newShipReleaseCommand())
	cmd.AddCommand(newShipReserveCargoCommand())
	cmd.AddCommand(newShipUnreserveCargoCommand())
	cmd.AddCommand(newShipReservedCargoCommand())
	cmd.AddCommand(newShipNavigateCommand())
	cmd.AddCommand(newShipRouteCommand())
	cmd.AddCommand(newShipDockCommand())
	cmd.AddCommand(newShipOrbitCommand())
	cmd.AddCommand(newShipRefuelCommand())
	cmd.AddCommand(newShipJumpCommand())
	cmd.AddCommand(newShipSellCommand())
	cmd.AddCommand(newShipBuyCommand())
	cmd.AddCommand(newShipJettisonCommand())
	cmd.AddCommand(newShipOutfitCommand())

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
	// Fleet (sp-ioqt) is the ship's permanent dedicated-fleet tag (sp-snmb),
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
				// sp-i1ku: a captain reservation has no ContainerID (it was
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

// newShipReserveCommand creates the ship reserve subcommand
func newShipReserveCommand() *cobra.Command {
	var (
		shipSymbol string
		reason     string
	)

	cmd := &cobra.Command{
		Use:   "reserve",
		Short: "Reserve a ship for the captain's direct manual use",
		Long: `Reserve a ship for the captain's own direct, manual use, hiding it from
every coordinator's assignment discovery (mfg, contracts, scouting, trade
routes, etc.).

A captain reservation is persisted as an assignment row, so it survives
daemon restarts and is excluded from the stale-claim reconciliation pass —
no coordinator can claim a reserved hull out from under you, and refreshing
the ship's cache will never release the reservation on your behalf. Use
'ship release' when you're done with it.

If the reserved hull was the last idle ship of its role, a warning is
printed — the reservation still succeeds; the warning is advisory only.

Examples:
  spacetraders ship reserve --ship ENDURANCE-1 --reason "manual gate-supply errand"
  spacetraders ship reserve --ship ENDURANCE-1 --agent ENDURANCE`,
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

			playerID, agentSymbol := playerPointers(playerIdent)

			var reasonPtr *string
			if reason != "" {
				reasonPtr = &reason
			}

			response, err := client.ReserveShip(ctx, shipSymbol, reasonPtr, playerID, agentSymbol)
			if err != nil {
				return fmt.Errorf("failed to reserve ship: %w", err)
			}

			fmt.Printf("✓ %s reserved for captain use\n", response.ShipSymbol)
			if response.Reason != "" {
				fmt.Printf("  Reason: %s\n", response.Reason)
			}
			if response.Warning != "" {
				fmt.Printf("  Warning: %s\n", response.Warning)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol (required)")
	cmd.Flags().StringVar(&reason, "reason", "", "Free-text reason, shown in 'ship list' (optional)")

	return cmd
}

// newShipReleaseCommand creates the ship release subcommand
func newShipReleaseCommand() *cobra.Command {
	var (
		shipSymbol string
		reason     string
	)

	cmd := &cobra.Command{
		Use:   "release",
		Short: "Clear a captain reservation on a ship",
		Long: `Clear a captain reservation, returning the ship to idle so normal
coordinator discovery (mfg, contracts, scouting, trade routes, etc.) can
claim it again.

Examples:
  spacetraders ship release --ship ENDURANCE-1
  spacetraders ship release --ship ENDURANCE-1 --reason "errand complete"`,
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

			playerID, agentSymbol := playerPointers(playerIdent)

			var reasonPtr *string
			if reason != "" {
				reasonPtr = &reason
			}

			response, err := client.ReleaseShip(ctx, shipSymbol, reasonPtr, playerID, agentSymbol)
			if err != nil {
				return fmt.Errorf("failed to release ship: %w", err)
			}

			fmt.Printf("✓ %s reservation released — available for coordinator discovery again\n", response.ShipSymbol)

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol (required)")
	cmd.Flags().StringVar(&reason, "reason", "", "Free-text release reason (optional)")

	return cmd
}

// shipReservationRepo builds the ship repository and resolves the player for the
// cargo-reservation CLI verbs (sp-1vhv), mirroring newShipSellCommand's direct-DB
// wiring. Reservation reads/writes need only the ship repo — no mediator or market
// repo — so this is a trimmed copy of that command's dependency assembly.
func shipReservationRepo() (*api.ShipRepository, int, error) {
	playerIdent, err := resolvePlayerIdentifier()
	if err != nil {
		return nil, 0, err
	}

	cfg, err := config.LoadConfig("")
	if err != nil {
		return nil, 0, fmt.Errorf("failed to load config: %w", err)
	}
	db, err := database.NewConnection(&cfg.Database)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to connect to database: %w", err)
	}

	playerRepo := persistence.NewGormPlayerRepository(db)
	apiClient := api.NewSpaceTradersClient()
	waypointRepo := persistence.NewGormWaypointRepository(db)
	systemGraphRepo := persistence.NewGormSystemGraphRepository(db)
	graphBuilder := api.NewGraphBuilder(apiClient, playerRepo, waypointRepo)
	graphService := graph.NewGraphService(systemGraphRepo, waypointRepo, graphBuilder)
	shipRepo := api.NewShipRepository(apiClient, playerRepo, waypointRepo, graphService, db, nil)

	var resolvedPlayerID int
	if playerIdent.PlayerID > 0 {
		resolvedPlayerID = playerIdent.PlayerID
	} else {
		p, err := playerRepo.FindByAgentSymbol(context.Background(), playerIdent.AgentSymbol)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to resolve player from agent symbol: %w", err)
		}
		resolvedPlayerID = p.ID.Value()
	}
	return shipRepo, resolvedPlayerID, nil
}

// newShipReserveCargoCommand marks a good as do-not-sell on a hull (sp-1vhv), so
// no coordinator (tour/arb/circuit/held-liquidation) or CLI sell can liquidate it.
// Ship hardware (MODULE_*/MOUNT_*) is already reserved by default; this verb pins
// an additional good, or re-protects a module previously released with
// `ship unreserve-cargo`.
func newShipReserveCargoCommand() *cobra.Command {
	var (
		shipSymbol string
		goodSymbol string
	)

	cmd := &cobra.Command{
		Use:   "reserve-cargo",
		Short: "Mark a cargo good as do-not-sell on a ship",
		Long: `Reserve a cargo good so no coordinator or CLI sell ever liquidates it.

Ship hardware bought for outfitting (MODULE_*/MOUNT_*) is reserved by DEFAULT —
you only need this verb to protect an additional good, or to re-protect a module
you previously released with 'ship unreserve-cargo'. The reservation is persisted
per-hull and survives daemon restarts.

Examples:
  spacetraders ship reserve-cargo --ship TORWIND-1E --good ANTIMATTER --agent TORWIND
  spacetraders ship reserve-cargo --ship TORWIND-1E --good MODULE_CARGO_HOLD_III --player-id 1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}
			if goodSymbol == "" {
				return fmt.Errorf("--good flag is required")
			}

			shipRepo, playerID, err := shipReservationRepo()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			if err := shipRepo.SetCargoReservation(ctx, shipSymbol, goodSymbol, true, shared.MustNewPlayerID(playerID)); err != nil {
				return fmt.Errorf("failed to reserve cargo: %w", err)
			}

			fmt.Printf("✓ %s on %s reserved (do-not-sell) — coordinators will not liquidate it\n", goodSymbol, shipSymbol)
			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol (required)")
	cmd.Flags().StringVar(&goodSymbol, "good", "", "Trade good or module symbol (required)")
	return cmd
}

// newShipUnreserveCargoCommand releases a good for sale on a hull (sp-1vhv),
// overriding the default MODULE_*/MOUNT_* reservation — the deliberate-resale
// escape hatch. After this, a coordinator or CLI sell of the good is permitted.
func newShipUnreserveCargoCommand() *cobra.Command {
	var (
		shipSymbol string
		goodSymbol string
	)

	cmd := &cobra.Command{
		Use:   "unreserve-cargo",
		Short: "Release a reserved cargo good for sale on a ship",
		Long: `Release a good for sale, overriding the default do-not-sell reservation.

Use this for the rare deliberate resale of ship hardware — e.g. selling a spare
MODULE_* you no longer intend to install. The override is persisted per-hull; run
'ship reserve-cargo' to protect the good again.

Examples:
  spacetraders ship unreserve-cargo --ship TORWIND-1E --good MODULE_CARGO_HOLD_III --agent TORWIND
  spacetraders ship unreserve-cargo --ship TORWIND-1E --good MOUNT_MINING_LASER_I --player-id 1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}
			if goodSymbol == "" {
				return fmt.Errorf("--good flag is required")
			}

			shipRepo, playerID, err := shipReservationRepo()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			if err := shipRepo.SetCargoReservation(ctx, shipSymbol, goodSymbol, false, shared.MustNewPlayerID(playerID)); err != nil {
				return fmt.Errorf("failed to unreserve cargo: %w", err)
			}

			fmt.Printf("✓ %s on %s released for sale — it may now be sold\n", goodSymbol, shipSymbol)
			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol (required)")
	cmd.Flags().StringVar(&goodSymbol, "good", "", "Trade good or module symbol (required)")
	return cmd
}

// newShipReservedCargoCommand shows a hull's cargo reservation state (sp-1vhv):
// the per-hull overrides and, for each good currently in the hold, whether it is
// reserved and why (default classification vs an explicit override).
func newShipReservedCargoCommand() *cobra.Command {
	var shipSymbol string

	cmd := &cobra.Command{
		Use:   "reserved-cargo",
		Short: "Show a ship's cargo do-not-sell reservations",
		Long: `Show which cargo is reserved (do-not-sell) on a ship.

Lists the per-hull reservation overrides and, for each good currently in the
hold, whether it is reserved and why — the default MODULE_*/MOUNT_* rule or an
explicit override set with 'ship reserve-cargo'/'ship unreserve-cargo'.

Examples:
  spacetraders ship reserved-cargo --ship TORWIND-1E --agent TORWIND
  spacetraders ship reserved-cargo --ship TORWIND-1E --player-id 1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}

			shipRepo, playerID, err := shipReservationRepo()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			ship, err := shipRepo.FindBySymbol(ctx, shipSymbol, shared.MustNewPlayerID(playerID))
			if err != nil {
				return fmt.Errorf("failed to load ship: %w", err)
			}
			if ship == nil {
				return fmt.Errorf("ship %s not found", shipSymbol)
			}

			fmt.Printf("Cargo reservations for %s\n", shipSymbol)
			if ship.ReservationStateCorrupt() {
				fmt.Println("  ⚠ reservation state is UNREADABLE — failing closed: ALL cargo is treated as reserved")
			}

			overrides := ship.ReservationOverrides()
			if len(overrides) == 0 {
				fmt.Println("  Overrides: none (defaults apply: MODULE_*/MOUNT_* reserved)")
			} else {
				fmt.Println("  Overrides:")
				for _, good := range sortedOverrideKeys(overrides) {
					state := "reserved (do-not-sell)"
					if !overrides[good] {
						state = "released for sale"
					}
					fmt.Printf("    %-28s %s\n", good, state)
				}
			}

			fmt.Println("  In hold:")
			inHold := false
			if c := ship.Cargo(); c != nil {
				for _, item := range c.Inventory {
					inHold = true
					status := "sellable"
					if ship.IsCargoReserved(item.Symbol) {
						status = "RESERVED"
					}
					fmt.Printf("    %-28s x%-5d %s\n", item.Symbol, item.Units, status)
				}
			}
			if !inHold {
				fmt.Println("    (hold empty)")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol (required)")
	return cmd
}

// sortedOverrideKeys returns the override map's good symbols in stable order for
// deterministic CLI output.
func sortedOverrideKeys(overrides map[string]bool) []string {
	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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

// newShipRouteCommand creates the ship route subcommand (sp-6hjw)
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

			// Execute route command
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			result, err := client.RouteShip(ctx, shipSymbol, destination, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("route failed: %w", err)
			}

			// Display result
			fmt.Println("✓ Route started successfully")
			fmt.Printf("  Container ID:     %s\n", result.ContainerID)
			fmt.Printf("  Ship:             %s\n", result.ShipSymbol)
			fmt.Printf("  Destination:      %s\n", result.Destination)
			fmt.Printf("  Status:           %s\n", result.Status)
			fmt.Printf("\nTrack progress with: spacetraders container logs %s\n", result.ContainerID)

			return nil
		},
	}

	// Command-specific flags
	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol to route (required)")
	cmd.Flags().StringVar(&destination, "destination", "", "Destination waypoint symbol in any reachable system (required)")

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

// newShipBuyCommand creates the ship buy subcommand.
//
// This is a faithful mirror of newShipSellCommand: it purchases cargo from the
// market at the ship's current docked waypoint, delegating to the shared
// PurchaseCargoHandler (the buy side of the same CargoTransactionHandler that
// powers sell). Cargo-capacity and market-availability validation, transaction
// splitting, and PURCHASE_CARGO ledger recording all live in that handler.
func newShipBuyCommand() *cobra.Command {
	var (
		shipSymbol string
		goodSymbol string
		units      int
	)

	cmd := &cobra.Command{
		Use:   "buy",
		Short: "Buy cargo for a ship",
		Long: `Buy cargo for a ship from the market at its current location.
Ship must be docked at a marketplace.

Examples:
  spacetraders ship buy --ship AGENT-1 --good IRON_ORE --units 50 --player-id 1
  spacetraders ship buy --ship ENDURANCE-1 --good IRON_ORE --units 100 --agent ENDURANCE`,
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
			handler := shipCargo.NewPurchaseCargoHandler(shipRepo, playerRepo, apiClient, marketRepo, mediator, nil)

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
			response, err := handler.Handle(ctx, &shipCargo.PurchaseCargoCommand{
				ShipSymbol: shipSymbol,
				GoodSymbol: goodSymbol,
				Units:      units,
				PlayerID:   shared.MustNewPlayerID(resolvedPlayerID),
			})
			if err != nil {
				return fmt.Errorf("buy cargo command failed: %w", err)
			}

			result, ok := response.(*shipCargo.PurchaseCargoResponse)
			if !ok {
				return fmt.Errorf("unexpected response type")
			}

			// Display success
			fmt.Println("✓ Cargo purchased successfully")
			fmt.Printf("  Ship:           %s\n", shipSymbol)
			fmt.Printf("  Good:           %s\n", goodSymbol)
			fmt.Printf("  Units Purchased: %d\n", result.UnitsAdded)
			fmt.Printf("  Total Cost:     %d credits\n", result.TotalCost)

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol to buy for (required)")
	cmd.Flags().StringVar(&goodSymbol, "good", "", "Trade good symbol to buy (required)")
	cmd.Flags().IntVar(&units, "units", 0, "Number of units to buy (required)")

	return cmd
}

// newShipJettisonCommand creates the ship jettison subcommand
func newShipJettisonCommand() *cobra.Command {
	var (
		shipSymbol string
		goodSymbol string
		units      int
	)

	cmd := &cobra.Command{
		Use:   "jettison",
		Short: "Jettison cargo from a ship into space",
		Long: `Jettison cargo from a ship, permanently discarding it.

Use this to dispose of stranded or unsellable cargo (e.g. bait/leftover units
blocking a hull) when no reachable market buys the good — the last resort
when a direct sell isn't possible. The ship is automatically moved to orbit
first if it is currently docked, since jettisoning requires orbit.

Examples:
  spacetraders ship jettison --ship AGENT-1 --good IRON_ORE --units 50 --player-id 1
  spacetraders ship jettison --ship ENDURANCE-1 --good GAS --units 12 --agent ENDURANCE`,
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

			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			result, err := client.JettisonCargo(ctx, shipSymbol, goodSymbol, units, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("jettison failed: %w", err)
			}

			fmt.Println("✓ Jettison operation started")
			fmt.Printf("  Container ID:     %s\n", result.ContainerID)
			fmt.Printf("  Ship:             %s\n", result.ShipSymbol)
			fmt.Printf("  Good:             %s\n", result.GoodSymbol)
			fmt.Printf("  Units Discarded:  %d\n", result.UnitsJettisoned)
			fmt.Printf("  Status:           %s\n", result.Status)

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol to jettison cargo from (required)")
	cmd.Flags().StringVar(&goodSymbol, "good", "", "Trade good symbol to jettison (required)")
	cmd.Flags().IntVar(&units, "units", 0, "Number of units to jettison (required)")

	return cmd
}
