package cli

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	scoutingQuery "github.com/andrescamacho/spacetraders-go/internal/application/scouting/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
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

	cmd.AddCommand(newMarketGetCommand())
	cmd.AddCommand(newMarketListCommand())
	cmd.AddCommand(newMarketVolatilityCommand())
	cmd.AddCommand(newMarketHistoryCommand())
	cmd.AddCommand(newMarketFindCommand())
	cmd.AddCommand(newMarketSpreadsCommand())

	return cmd
}

// openMarketRepo opens the market cache and resolves the effective player. Market rows are
// player-partitioned, so every read needs both.
func openMarketRepo(ctx context.Context) (*persistence.MarketRepositoryGORM, int, error) {
	playerIdent, err := resolvePlayerIdentifier()
	if err != nil {
		return nil, 0, err
	}

	db, err := openDatabase()
	if err != nil {
		return nil, 0, err
	}

	resolvedPlayerID, err := resolveNumericPlayerID(ctx, db, playerIdent)
	if err != nil {
		return nil, 0, err
	}
	return persistence.NewMarketRepository(db), resolvedPlayerID, nil
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

			ctx := context.Background()
			marketRepo, resolvedPlayerID, err := openMarketRepo(ctx)
			if err != nil {
				return err
			}
			handler := scoutingQuery.NewGetMarketDataHandler(marketRepo)

			response, err := handler.Handle(ctx, &scoutingQuery.GetMarketDataQuery{
				PlayerID:       shared.MustNewPlayerID(resolvedPlayerID),
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

			market := result.Market
			fmt.Printf("\n=== Market Data for %s ===\n", market.WaypointSymbol())
			fmt.Printf("Last Updated: %s\n\n", market.LastUpdated().Format("2006-01-02 15:04:05"))

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

			ctx := context.Background()
			marketRepo, resolvedPlayerID, err := openMarketRepo(ctx)
			if err != nil {
				return err
			}
			handler := scoutingQuery.NewListMarketDataHandler(marketRepo)

			response, err := handler.Handle(ctx, &scoutingQuery.ListMarketDataQuery{
				PlayerID:      shared.MustNewPlayerID(resolvedPlayerID),
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
