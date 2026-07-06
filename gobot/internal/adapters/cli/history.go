package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

type historyProvider interface {
	ListEras(ctx context.Context) ([]persistence.EraOverview, error)
	GoodsStats(ctx context.Context, good string, eraID *int) ([]persistence.GoodsEraStat, error)
	ContractsStats(ctx context.Context, eraID *int, good *string) ([]persistence.ContractsEraStat, error)
	PnL(ctx context.Context, eraID *int, byOperation bool) (*persistence.PnLReport, error)
	ManufacturingStats(ctx context.Context, eraID *int, good *string) ([]persistence.ManufacturingGoodStat, error)
	EventStats(ctx context.Context, eraID *int, eventType *string) (*persistence.EventReport, error)
	Summary(ctx context.Context, eraID *int) (*persistence.SummaryReport, error)
}

func parseEraFlag(eraFlag string) (*int, error) {
	if eraFlag == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(eraFlag)
	if err != nil {
		return nil, fmt.Errorf("--era must be a numeric era_id: %w", err)
	}
	return &n, nil
}

func connectHistoryRepository() (*persistence.HistoryRepository, error) {
	cfg, err := config.LoadConfig("")
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	db, err := database.NewConnection(&cfg.Database)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}
	return persistence.NewHistoryRepository(db), nil
}

func NewHistoryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Cross-era priors: query history across universe resets",
		Long: `Read-only queries over the live tables, scoped through the eras registry.

History and live data share the same tables (rev 2, in-place player-partitioned
history), so these are ordinary era-scoped reads. Pattern queries default to
--era all; 'history summary' defaults to the latest CLOSED era.

Examples:
  spacetraders history eras
  spacetraders history goods --good ADVANCED_CIRCUITRY --era 1
  spacetraders history summary`,
	}

	cmd.AddCommand(newHistoryErasCommand())
	cmd.AddCommand(newHistoryGoodsCommand())
	cmd.AddCommand(newHistoryContractsCommand())
	cmd.AddCommand(newHistoryPnLCommand())
	cmd.AddCommand(newHistoryManufacturingCommand())
	cmd.AddCommand(newHistoryEventsCommand())
	cmd.AddCommand(newHistorySummaryCommand())

	return cmd
}

func newHistoryErasCommand() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "eras",
		Short: "List the era registry",
		Long:  "Orientation for every other history query: era_id, name, agent, faction, reset date, duration, final credits.",
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := connectHistoryRepository()
			if err != nil {
				return err
			}
			return runHistoryEras(context.Background(), repo, os.Stdout, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runHistoryEras(ctx context.Context, p historyProvider, out io.Writer, jsonOut bool) error {
	eras, err := p.ListEras(ctx)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(out, eras)
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ERA\tNAME\tAGENT\tFACTION\tRESET DATE\tDURATION(d)\tFINAL CREDITS")
	for _, e := range eras {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%.1f\t%d\n",
			e.EraID, e.Name, e.AgentSymbol, e.Faction, e.UniverseResetDate, e.DurationDays, e.FinalCredits)
	}
	w.Flush()
	return nil
}

func newHistoryGoodsCommand() *cobra.Command {
	var (
		good    string
		eraFlag string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "goods",
		Short: "Per-era supply/price/volatility priors for a good",
		Long:  "Did this good run thin last universe? Markets, median price, supply distribution, trade volume, volatility.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if good == "" {
				return fmt.Errorf("--good flag is required")
			}
			eraID, err := parseEraFlag(eraFlag)
			if err != nil {
				return err
			}
			repo, err := connectHistoryRepository()
			if err != nil {
				return err
			}
			return runHistoryGoods(context.Background(), repo, os.Stdout, good, eraID, jsonOut)
		},
	}
	cmd.Flags().StringVar(&good, "good", "", "Good symbol [required]")
	cmd.Flags().StringVar(&eraFlag, "era", "", "Era ID (default: all eras)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runHistoryGoods(ctx context.Context, p historyProvider, out io.Writer, good string, eraID *int, jsonOut bool) error {
	stats, err := p.GoodsStats(ctx, good, eraID)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(out, stats)
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ERA\tMARKETS\tSAMPLES\tMED BUY\tMED SELL\tAVG VOLUME\tVOLATILITY\tSUPPLY DIST")
	for _, s := range stats {
		fmt.Fprintf(w, "%s\t%d\t%d\t%.1f\t%.1f\t%.1f\t%.2f\t%v\n",
			s.EraName, s.MarketCount, s.SampleCount, s.MedianBuyPrice, s.MedianSellPrice,
			s.AvgTradeVolume, s.SellPriceVolatility, s.SupplyDistribution)
	}
	w.Flush()
	return nil
}

func newHistoryContractsCommand() *cobra.Command {
	var (
		eraFlag string
		good    string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "contracts",
		Short: "Per-era contract economics",
		Long:  "Count by type/faction, payout stats, fulfillment rate, accept-to-deadline slack.",
		RunE: func(cmd *cobra.Command, args []string) error {
			eraID, err := parseEraFlag(eraFlag)
			if err != nil {
				return err
			}
			var goodPtr *string
			if good != "" {
				goodPtr = &good
			}
			repo, err := connectHistoryRepository()
			if err != nil {
				return err
			}
			return runHistoryContracts(context.Background(), repo, os.Stdout, eraID, goodPtr, jsonOut)
		},
	}
	cmd.Flags().StringVar(&eraFlag, "era", "", "Era ID (default: all eras)")
	cmd.Flags().StringVar(&good, "good", "", "Filter to contracts delivering this good")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runHistoryContracts(ctx context.Context, p historyProvider, out io.Writer, eraID *int, good *string, jsonOut bool) error {
	stats, err := p.ContractsStats(ctx, eraID, good)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(out, stats)
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ERA\tCOUNT\tAVG PAYOUT\tPAYOUT VAR\tFULFILL RATE\tAVG SLACK(h)\tPAYOUT/UNIT\tBY TYPE\tBY GOOD")
	for _, s := range stats {
		fmt.Fprintf(w, "%s\t%d\t%.0f\t%.0f\t%.2f\t%.1f\t%.2f\t%v\t%v\n",
			s.EraName, s.TotalCount, s.AvgTotalPayout, s.PayoutVariance, s.FulfillmentRate,
			s.AvgAcceptSlackHours, s.PayoutPerDeliveredUnit, s.ByType, s.ByGood)
	}
	w.Flush()
	return nil
}

func newHistoryPnLCommand() *cobra.Command {
	var (
		eraFlag     string
		byCategory  bool
		byOperation bool
		jsonOut     bool
	)
	cmd := &cobra.Command{
		Use:   "pnl",
		Short: "Era P&L rollup from era-scoped transactions",
		Long:  "Net by category or operation type, plus the daily ramp curve when a single era is given.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if byCategory && byOperation {
				return fmt.Errorf("--by-category and --by-operation are mutually exclusive")
			}
			eraID, err := parseEraFlag(eraFlag)
			if err != nil {
				return err
			}
			repo, err := connectHistoryRepository()
			if err != nil {
				return err
			}
			return runHistoryPnL(context.Background(), repo, os.Stdout, eraID, byOperation, jsonOut)
		},
	}
	cmd.Flags().StringVar(&eraFlag, "era", "", "Era ID (default: all eras)")
	cmd.Flags().BoolVar(&byCategory, "by-category", false, "Group by transaction category (default)")
	cmd.Flags().BoolVar(&byOperation, "by-operation", false, "Group by operation type")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runHistoryPnL(ctx context.Context, p historyProvider, out io.Writer, eraID *int, byOperation bool, jsonOut bool) error {
	report, err := p.PnL(ctx, eraID, byOperation)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(out, report)
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "KEY\tNET\tCOUNT")
	for _, b := range report.Breakdown {
		fmt.Fprintf(w, "%s\t%d\t%d\n", b.Key, b.Net, b.Count)
	}
	fmt.Fprintf(w, "TOTAL\t%d\t\n", report.NetTotal)
	w.Flush()
	if len(report.Daily) > 0 {
		fmt.Fprintln(out, "\nDAILY NET")
		dw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(dw, "DATE\tNET")
		for _, d := range report.Daily {
			fmt.Fprintf(dw, "%s\t%d\n", d.Date, d.Net)
		}
		dw.Flush()
	}
	return nil
}

func newHistoryManufacturingCommand() *cobra.Command {
	var (
		eraFlag string
		good    string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "manufacturing",
		Short: "Per-product-good pipeline outcomes across eras",
		Long:  "Which chains were worth running: count, success rate, avg cost, avg net profit.",
		RunE: func(cmd *cobra.Command, args []string) error {
			eraID, err := parseEraFlag(eraFlag)
			if err != nil {
				return err
			}
			var goodPtr *string
			if good != "" {
				goodPtr = &good
			}
			repo, err := connectHistoryRepository()
			if err != nil {
				return err
			}
			return runHistoryManufacturing(context.Background(), repo, os.Stdout, eraID, goodPtr, jsonOut)
		},
	}
	cmd.Flags().StringVar(&eraFlag, "era", "", "Era ID (default: all eras)")
	cmd.Flags().StringVar(&good, "good", "", "Filter to a single product good")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runHistoryManufacturing(ctx context.Context, p historyProvider, out io.Writer, eraID *int, good *string, jsonOut bool) error {
	stats, err := p.ManufacturingStats(ctx, eraID, good)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(out, stats)
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "GOOD\tCOUNT\tSUCCESS RATE\tAVG COST\tAVG NET PROFIT")
	for _, s := range stats {
		fmt.Fprintf(w, "%s\t%d\t%.2f\t%.0f\t%.0f\n", s.Good, s.Count, s.SuccessRate, s.AvgCost, s.AvgNetProfit)
	}
	w.Flush()
	return nil
}

func newHistoryEventsCommand() *cobra.Command {
	var (
		eraFlag   string
		eventType string
		jsonOut   bool
	)
	cmd := &cobra.Command{
		Use:   "events",
		Short: "captain_events frequency and timing across eras",
		Long:  "Feeds detector tuning and the retrospective's incident section.",
		RunE: func(cmd *cobra.Command, args []string) error {
			eraID, err := parseEraFlag(eraFlag)
			if err != nil {
				return err
			}
			var typePtr *string
			if eventType != "" {
				typePtr = &eventType
			}
			repo, err := connectHistoryRepository()
			if err != nil {
				return err
			}
			return runHistoryEvents(context.Background(), repo, os.Stdout, eraID, typePtr, jsonOut)
		},
	}
	cmd.Flags().StringVar(&eraFlag, "era", "", "Era ID (default: all eras)")
	cmd.Flags().StringVar(&eventType, "type", "", "Filter to a single event type")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runHistoryEvents(ctx context.Context, p historyProvider, out io.Writer, eraID *int, eventType *string, jsonOut bool) error {
	report, err := p.EventStats(ctx, eraID, eventType)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(out, report)
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TYPE\tCOUNT")
	for _, s := range report.ByType {
		fmt.Fprintf(w, "%s\t%d\n", s.Type, s.Count)
	}
	fmt.Fprintf(w, "TOTAL\t%d\n", report.Total)
	w.Flush()
	return nil
}

func newHistorySummaryCommand() *cobra.Command {
	var (
		eraFlag string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "summary",
		Short: "The cold-start brief for one era",
		Long:  "Defaults to the latest CLOSED era. Duration, treasury, income mix, top goods, contracts, thin goods, fuel band, events.",
		RunE: func(cmd *cobra.Command, args []string) error {
			eraID, err := parseEraFlag(eraFlag)
			if err != nil {
				return err
			}
			repo, err := connectHistoryRepository()
			if err != nil {
				return err
			}
			return runHistorySummary(context.Background(), repo, os.Stdout, eraID, jsonOut)
		},
	}
	cmd.Flags().StringVar(&eraFlag, "era", "", "Era ID (default: latest closed era)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runHistorySummary(ctx context.Context, p historyProvider, out io.Writer, eraID *int, jsonOut bool) error {
	summary, err := p.Summary(ctx, eraID)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(out, summary)
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "Era\t%s (id %d)\n", summary.EraName, summary.EraID)
	fmt.Fprintf(w, "Duration\t%.1f days\n", summary.DurationDays)
	fmt.Fprintf(w, "Final credits\t%d\n", summary.FinalCredits)
	fmt.Fprintf(w, "Contracts\t%d (fulfillment %.0f%%)\n", summary.ContractCount, summary.ContractFulfillmentRate*100)
	fmt.Fprintf(w, "Fuel price band\t%d-%d\n", summary.FuelPriceMin, summary.FuelPriceMax)
	fmt.Fprintf(w, "Thin goods\t%v\n", summary.ThinGoods)
	w.Flush()

	fmt.Fprintln(out, "\nIncome mix %")
	iw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for k, v := range summary.IncomeMixPct {
		fmt.Fprintf(iw, "%s\t%.1f%%\n", k, v)
	}
	iw.Flush()

	fmt.Fprintln(out, "\nTop goods by trading profit")
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, g := range summary.TopGoodsByTradingProfit {
		fmt.Fprintf(tw, "%s\t%d\n", g.Good, g.NetProfit)
	}
	tw.Flush()

	fmt.Fprintln(out, "\nEvent highlights")
	ew := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, e := range summary.EventHighlights {
		fmt.Fprintf(ew, "%s\t%d\n", e.Type, e.Count)
	}
	ew.Flush()

	return nil
}

func writeJSON(out io.Writer, v interface{}) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
