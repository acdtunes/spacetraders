package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/graph"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	appMediator "github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	"github.com/andrescamacho/spacetraders-go/internal/application/player"
	"github.com/andrescamacho/spacetraders-go/internal/application/setup"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// newShipTransferCommand creates the ship transfer subcommand
func newShipTransferCommand() *cobra.Command {
	var (
		fromShip   string
		toShip     string
		goodSymbol string
		units      int
	)

	cmd := &cobra.Command{
		Use:   "transfer",
		Short: "Move cargo from one ship to another at the same waypoint",
		Long: `Move cargo directly from one ship's hold into another's.

Both ships must be parked at the same waypoint - the game requires it, and there
is no in-flight handover. The daemon aligns their nav states (both docked or both
in orbit) before the move, so they need not already match.

A module removed with 'ship outfit remove' sits in the ship's cargo as an ordinary
good, so this is how a module moves between hulls:

  ship outfit remove   --ship EXPLORER-1 --module MODULE_WARP_DRIVE_I
  ship transfer --from EXPLORER-1 --to FREIGHTER-1 --good MODULE_WARP_DRIVE_I --units 1
  ship outfit install  --ship FREIGHTER-1 --module MODULE_WARP_DRIVE_I

Two refusals are possible and both leave the cargo exactly where it is: the ships
are at different waypoints (reported with each one's location), or the receiving
ship has no cargo space left (reported with its capacity and load). The move is
instantaneous, so either is printed here rather than in a container log.

Examples:
  spacetraders ship transfer --from TORWIND-F6 --to TORWIND-2 --good MODULE_WARP_DRIVE_I --units 1 --player-id 1
  spacetraders ship transfer --from TORWIND-F6 --to TORWIND-2 --good ALUMINUM --units 40 --agent TORWIND`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if fromShip == "" {
				return fmt.Errorf("--from flag is required")
			}
			if toShip == "" {
				return fmt.Errorf("--to flag is required")
			}
			if goodSymbol == "" {
				return fmt.Errorf("--good flag is required")
			}
			if units <= 0 {
				return fmt.Errorf("--units must be greater than zero")
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

			result, err := client.TransferCargo(ctx, fromShip, toShip, goodSymbol, units,
				playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("transfer failed: %w", err)
			}
			// The daemon's refusal already names its own condition (different waypoints,
			// or no room on the receiver) - pass it through whole rather than restating it.
			if result.Error != "" {
				return fmt.Errorf("transfer refused: %s", result.Error)
			}

			fmt.Printf("✓ Transferred %d x %s from %s to %s\n",
				result.UnitsTransferred, result.GoodSymbol, result.FromShipSymbol, result.ToShipSymbol)
			fmt.Printf("  Left on %s:  %d\n", result.FromShipSymbol, result.RemainingUnits)

			return nil
		},
	}

	cmd.Flags().StringVar(&fromShip, "from", "", "Ship symbol the cargo leaves (required)")
	cmd.Flags().StringVar(&toShip, "to", "", "Ship symbol the cargo arrives on, at the same waypoint (required)")
	cmd.Flags().StringVar(&goodSymbol, "good", "", "Good symbol to move, e.g. MODULE_WARP_DRIVE_I (required)")
	cmd.Flags().IntVar(&units, "units", 0, "Units to move (required, must be greater than zero)")

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

// newShipSellCommand creates the ship sell subcommand
// cargoTradeDeps is the direct-DB dependency set the `ship buy` / `ship sell` verbs share.
type cargoTradeDeps struct {
	ident    *PlayerIdentifier
	ships    *api.ShipRepository
	players  *persistence.GormPlayerRepository
	client   *api.SpaceTradersClient
	markets  *persistence.MarketRepositoryGORM
	mediator appMediator.Mediator
}

func newCargoTradeDeps() (*cargoTradeDeps, error) {
	playerIdent, err := resolvePlayerIdentifier()
	if err != nil {
		return nil, err
	}

	db, err := openDatabase()
	if err != nil {
		return nil, err
	}

	playerRepo := persistence.NewGormPlayerRepository(db)
	apiClient := api.NewSpaceTradersClient()
	waypointRepo := persistence.NewGormWaypointRepository(db)
	systemGraphRepo := persistence.NewGormSystemGraphRepository(db)
	graphBuilder := api.NewGraphBuilder(apiClient, playerRepo, waypointRepo)
	graphService := graph.NewGraphService(systemGraphRepo, waypointRepo, graphBuilder)
	shipRepo := api.NewShipRepository(apiClient, playerRepo, waypointRepo, graphService, db, nil) // nil = use RealClock

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
		return nil, fmt.Errorf("failed to create mediator: %w", err)
	}

	return &cargoTradeDeps{
		ident:    playerIdent,
		ships:    shipRepo,
		players:  playerRepo,
		client:   apiClient,
		markets:  persistence.NewMarketRepository(db),
		mediator: mediator,
	}, nil
}

// resolvePlayer returns the numeric player ID and a context carrying that player's token,
// which the ledger handlers require to record the transaction.
func (d *cargoTradeDeps) resolvePlayer(ctx context.Context) (context.Context, int, error) {
	if d.ident.PlayerID > 0 {
		p, err := d.players.FindByID(ctx, shared.MustNewPlayerID(d.ident.PlayerID))
		if err != nil {
			return nil, 0, fmt.Errorf("failed to load player: %w", err)
		}
		return auth.WithPlayerToken(ctx, p.Token), d.ident.PlayerID, nil
	}

	p, err := d.players.FindByAgentSymbol(ctx, d.ident.AgentSymbol)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to resolve player from agent symbol: %w", err)
	}
	return auth.WithPlayerToken(ctx, p.Token), p.ID.Value(), nil
}

func validateCargoTradeFlags(shipSymbol, goodSymbol string, units int) error {
	if shipSymbol == "" {
		return fmt.Errorf("--ship flag is required")
	}
	if goodSymbol == "" {
		return fmt.Errorf("--good flag is required")
	}
	if units <= 0 {
		return fmt.Errorf("--units must be greater than 0")
	}
	return nil
}

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
			if err := validateCargoTradeFlags(shipSymbol, goodSymbol, units); err != nil {
				return err
			}

			deps, err := newCargoTradeDeps()
			if err != nil {
				return err
			}

			// Create handler (nil marketRefresher - CLI doesn't refresh market data after transactions)
			handler := shipCargo.NewSellCargoHandler(deps.ships, deps.players, deps.client, deps.markets, deps.mediator, nil)

			ctx, resolvedPlayerID, err := deps.resolvePlayer(context.Background())
			if err != nil {
				return err
			}

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
			if err := validateCargoTradeFlags(shipSymbol, goodSymbol, units); err != nil {
				return err
			}

			deps, err := newCargoTradeDeps()
			if err != nil {
				return err
			}

			// Create handler (nil marketRefresher - CLI doesn't refresh market data after transactions)
			handler := shipCargo.NewPurchaseCargoHandler(deps.ships, deps.players, deps.client, deps.markets, deps.mediator, nil)

			ctx, resolvedPlayerID, err := deps.resolvePlayer(context.Background())
			if err != nil {
				return err
			}

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
