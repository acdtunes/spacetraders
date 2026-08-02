package cli

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/graph"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// shipReservationRepo wires the cargo-reservation verbs, which need only the ship repo —
// no mediator or market repo, so it does not go through newCargoTradeDeps.
func shipReservationRepo() (*api.ShipRepository, int, error) {
	playerIdent, err := resolvePlayerIdentifier()
	if err != nil {
		return nil, 0, err
	}

	db, err := openDatabase()
	if err != nil {
		return nil, 0, err
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

// newShipReserveCommand creates the ship reserve subcommand
func newShipReserveCommand() *cobra.Command {
	var (
		shipSymbol string
		reason     string
		force      bool
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

By default, reserving a hull a coordinator is actively working fails
('already assigned to container ...'). Pass --force to PREEMPT that claim:
the coordinator's live claim is atomically revoked and the hull transferred
to you, and the coordinator gracefully re-plans without it on its next tick.
--force is the only way to take back a hull the operator is locked out of.

Examples:
  spacetraders ship reserve --ship ENDURANCE-1 --reason "manual gate-supply errand"
  spacetraders ship reserve --ship ENDURANCE-1 --agent ENDURANCE
  spacetraders ship reserve --ship TORWIND-8 --force --reason "reclaim stranded cargo"`,
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

			var reasonPtr *string
			if reason != "" {
				reasonPtr = &reason
			}

			response, err := client.ReserveShip(ctx, shipSymbol, reasonPtr, playerIdent, force)
			if err != nil {
				return fmt.Errorf("failed to reserve ship: %w", err)
			}

			if response.Preempted {
				fmt.Printf("✓ %s preempted from container %s and reserved for captain use\n", response.ShipSymbol, response.PreemptedFrom)
				fmt.Printf("  The coordinator will drop the hull and re-plan on its next tick.\n")
			} else {
				fmt.Printf("✓ %s reserved for captain use\n", response.ShipSymbol)
			}
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
	cmd.Flags().BoolVar(&force, "force", false, "Preempt a coordinator's live claim: revoke it and take the hull for the captain (sp-w3yd)")
	cmd.Flags().BoolVar(&force, "preempt", false, "Alias for --force")

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

			var reasonPtr *string
			if reason != "" {
				reasonPtr = &reason
			}

			response, err := client.ReleaseShip(ctx, shipSymbol, reasonPtr, playerIdent)
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

// newShipReserveCargoCommand marks a good as do-not-sell on a hull, so
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

// newShipUnreserveCargoCommand releases a good for sale on a hull,
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

// newShipReservedCargoCommand shows a hull's cargo reservation state:
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
