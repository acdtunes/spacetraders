package cli

import (
	"context"
	"fmt"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	scoutingQuery "github.com/andrescamacho/spacetraders-go/internal/application/scouting/queries"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// NewMarketCommand creates the market command with subcommands
func NewMarketCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "market",
		Short: "View market data",
		Long: `Query cached market data for waypoints and systems.

Markets show trade goods with supply, activity, purchase prices, sell prices,
and trade volumes. Use these commands to find trading opportunities.

Examples:
  spacetraders market get --waypoint X1-GZ7-B2 --agent ENDURANCE
  spacetraders market list --system X1-GZ7 --agent ENDURANCE`,
	}

	// Add subcommands
	cmd.AddCommand(newMarketGetCommand())
	cmd.AddCommand(newMarketListCommand())

	return cmd
}

// newMarketGetCommand creates the market get subcommand
func newMarketGetCommand() *cobra.Command {
	var waypointSymbol string

	cmd := &cobra.Command{
		Use:   "get",
		Short: "Get market data for a waypoint",
		Long: `Query cached market data for a specific waypoint.

Shows trade goods with supply, activity, purchase price, sell price, and volume.

Examples:
  spacetraders market get --waypoint X1-TEST-A1 --player-id 1
  spacetraders market get --waypoint X1-GZ7-B2 --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if waypointSymbol == "" {
				return fmt.Errorf("--waypoint flag is required")
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

			// Create repositories and handler
			playerRepo := persistence.NewGormPlayerRepository(db)
			marketRepo := persistence.NewMarketRepository(db)
			handler := scoutingQuery.NewGetMarketDataHandler(marketRepo)

			// Resolve player ID from identifier
			ctx := context.Background()
			var resolvedPlayerID uint
			if playerIdent.PlayerID > 0 {
				resolvedPlayerID = uint(playerIdent.PlayerID)
			} else {
				// Look up player by agent symbol
				player, err := playerRepo.FindByAgentSymbol(ctx, playerIdent.AgentSymbol)
				if err != nil {
					return fmt.Errorf("failed to resolve player from agent symbol: %w", err)
				}
				resolvedPlayerID = uint(player.ID.Value())
			}

			// Execute query
			response, err := handler.Handle(ctx, &scoutingQuery.GetMarketDataQuery{
				PlayerID:       shared.MustNewPlayerID(int(resolvedPlayerID)),
				WaypointSymbol: waypointSymbol,
			})
			if err != nil {
				return fmt.Errorf("failed to get market data: %w", err)
			}

			result, ok := response.(*scoutingQuery.GetMarketDataResponse)
			if !ok {
				return fmt.Errorf("unexpected response type")
			}

			if result.Market == nil {
				fmt.Printf("No market data found for %s\n", waypointSymbol)
				return nil
			}

			// Display market data
			market := result.Market
			fmt.Printf("\n=== Market Data for %s ===\n", market.WaypointSymbol())
			fmt.Printf("Last Updated: %s\n\n", market.LastUpdated().Format("2006-01-02 15:04:05"))

			// Display trade goods table
			goods := market.TradeGoods()
			if len(goods) == 0 {
				fmt.Println("No trade goods available")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "SYMBOL\tSUPPLY\tACTIVITY\tBUY PRICE\tSELL PRICE\tVOLUME")
			fmt.Fprintln(w, "------\t------\t--------\t---------\t----------\t------")

			for _, good := range goods {
				supplyStr := "N/A"
				if supply := good.Supply(); supply != nil && *supply != "" {
					supplyStr = *supply
				}
				activityStr := "N/A"
				if activity := good.Activity(); activity != nil && *activity != "" {
					activityStr = *activity
				}

				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\n",
					good.Symbol(),
					supplyStr,
					activityStr,
					good.PurchasePrice(),
					good.SellPrice(),
					good.TradeVolume(),
				)
			}

			w.Flush()
			fmt.Println()

			return nil
		},
	}

	cmd.Flags().StringVar(&waypointSymbol, "waypoint", "", "Waypoint symbol (required)")

	return cmd
}

// newMarketListCommand creates the market list subcommand
func newMarketListCommand() *cobra.Command {
	var (
		systemSymbol  string
		maxAgeMinutes int
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List markets in a system",
		Long: `Query all cached market data for a system with optional age filtering.

Shows waypoint symbols, number of goods available, and last update timestamp.

Examples:
  spacetraders market list --system X1-TEST --player-id 1
  spacetraders market list --system X1-GZ7 --max-age-minutes 60 --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if systemSymbol == "" {
				return fmt.Errorf("--system flag is required")
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

			// Create repositories and handler
			playerRepo := persistence.NewGormPlayerRepository(db)
			marketRepo := persistence.NewMarketRepository(db)
			handler := scoutingQuery.NewListMarketDataHandler(marketRepo)

			// Resolve player ID from identifier
			ctx := context.Background()
			var resolvedPlayerID uint
			if playerIdent.PlayerID > 0 {
				resolvedPlayerID = uint(playerIdent.PlayerID)
			} else {
				// Look up player by agent symbol
				player, err := playerRepo.FindByAgentSymbol(ctx, playerIdent.AgentSymbol)
				if err != nil {
					return fmt.Errorf("failed to resolve player from agent symbol: %w", err)
				}
				resolvedPlayerID = uint(player.ID.Value())
			}

			// Execute query
			response, err := handler.Handle(ctx, &scoutingQuery.ListMarketDataQuery{
				PlayerID:      shared.MustNewPlayerID(int(resolvedPlayerID)),
				SystemSymbol:  systemSymbol,
				MaxAgeMinutes: maxAgeMinutes,
			})
			if err != nil {
				return fmt.Errorf("failed to list markets: %w", err)
			}

			result, ok := response.(*scoutingQuery.ListMarketDataResponse)
			if !ok {
				return fmt.Errorf("unexpected response type")
			}

			if len(result.Markets) == 0 {
				fmt.Printf("No markets found in system %s\n", systemSymbol)
				if maxAgeMinutes > 0 {
					fmt.Printf("(filtered by max age: %d minutes)\n", maxAgeMinutes)
				}
				return nil
			}

			// Display markets table
			fmt.Printf("\n=== Markets in %s ===\n\n", systemSymbol)

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "WAYPOINT\tGOODS\tLAST UPDATED")
			fmt.Fprintln(w, "--------\t-----\t------------")

			for _, market := range result.Markets {
				fmt.Fprintf(w, "%s\t%d\t%s\n",
					market.WaypointSymbol(),
					market.GoodsCount(),
					market.LastUpdated().Format("2006-01-02 15:04:05"),
				)
			}

			w.Flush()
			fmt.Printf("\nTotal markets: %d\n", len(result.Markets))
			if maxAgeMinutes > 0 {
				fmt.Printf("(filtered by max age: %d minutes)\n", maxAgeMinutes)
			}
			fmt.Println()

			return nil
		},
	}

	cmd.Flags().StringVar(&systemSymbol, "system", "", "System symbol (required)")
	cmd.Flags().IntVar(&maxAgeMinutes, "max-age-minutes", 0, "Only show markets updated within this many minutes (0 = all)")

	return cmd
}
