package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/ledger/queries"
	"github.com/andrescamacho/spacetraders-go/internal/application/player"
)

// ledgerDateLayout is the accepted form of the --start-date/--end-date flags.
const ledgerDateLayout = "2006-01-02"

// NewLedgerCommand creates the ledger command with subcommands
func NewLedgerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ledger",
		Short: "Financial ledger operations",
		Long: `View and analyze financial transactions.

The ledger tracks all credit-affecting operations including fuel costs,
cargo trading, ship purchases, and contract payments. Use these commands
to view transaction history and generate financial reports.

Examples:
  spacetraders ledger list --player-id 1
  spacetraders ledger list --category FUEL_COSTS --limit 20
  spacetraders ledger report profit-loss --start-date 2024-01-01 --end-date 2024-01-31
  spacetraders ledger report cash-flow --start-date 2024-01-15 --end-date 2024-01-22`,
	}

	cmd.AddCommand(newLedgerListCommand())
	cmd.AddCommand(newLedgerPositionsCommand())
	cmd.AddCommand(newLedgerReportCommand())

	return cmd
}

// ledgerListFlags is one `ledger list` query as the operator typed it.
type ledgerListFlags struct {
	startDate string
	endDate   string
	category  string
	txType    string
	limit     int
	offset    int
	orderBy   string
	jsonOut   bool
}

// newLedgerListCommand creates the ledger list subcommand
func newLedgerListCommand() *cobra.Command {
	var flags ledgerListFlags

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List transactions",
		Long: `List financial transactions with optional filtering.

Transactions can be filtered by date range, category, and type.
Results are ordered by timestamp descending (newest first) by default.

Categories (derived deterministically from transaction type):
  FUEL_COSTS        - Fuel expenses
  TRADING_REVENUE   - Income from ANY cargo sale (SELL_CARGO): factory output,
                      tour/trade legs, construction supply, contract delivery —
                      not just standalone arbitrage trades
  TRADING_COSTS     - Cost of ANY cargo purchase (PURCHASE_CARGO): factory inputs,
                      tour/trade buys, construction supply — not just standalone trades
  SHIP_INVESTMENTS  - Expenses from purchasing ships AND from outfitting them
                      (MODULE_INSTALL / MODULE_REMOVE shipyard fees)
  CONTRACT_REVENUE  - Income from contracts
  TRAVEL_COSTS      - NON-fuel cost of moving a hull; today only jump gate fees.
                      Kept separate from FUEL_COSTS: a gate fee is charged per
                      jump, fuel scales with distance burned
  CHARTING_REVENUE  - Income from publicly charting a waypoint (CHART), priced
                      by the API on the rarity of the traits the chart reveals

Transaction Types:
  REFUEL              - Ship refueling
  PURCHASE_CARGO      - Cargo purchase
  SELL_CARGO          - Cargo sale
  PURCHASE_SHIP       - Ship purchase
  CONTRACT_ACCEPTED   - Contract acceptance payment
  CONTRACT_FULFILLED  - Contract fulfillment payment
  JUMP                - Jump gate fee charged for a cross-system jump
  MODULE_INSTALL      - Shipyard fee to install a module on a hull
  MODULE_REMOVE       - Shipyard fee to remove a module (a fee, not a refund)
  CHART               - One-time reward paid for publicly charting a waypoint

Examples:
  spacetraders ledger list --player-id 1 --limit 10
  spacetraders ledger list --category FUEL_COSTS
  spacetraders ledger list --start-date 2024-01-15 --end-date 2024-01-22`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLedgerList(flags)
		},
	}

	cmd.Flags().StringVar(&flags.startDate, "start-date", "", "Start date (YYYY-MM-DD)")
	cmd.Flags().StringVar(&flags.endDate, "end-date", "", "End date (YYYY-MM-DD)")
	cmd.Flags().StringVar(&flags.category, "category", "", "Filter by category")
	cmd.Flags().StringVar(&flags.txType, "type", "", "Filter by transaction type")
	cmd.Flags().IntVar(&flags.limit, "limit", 50, "Maximum number of transactions to return")
	cmd.Flags().IntVar(&flags.offset, "offset", 0, "Number of transactions to skip")
	cmd.Flags().StringVar(&flags.orderBy, "order-by", "timestamp DESC", "Sort order")
	cmd.Flags().BoolVar(&flags.jsonOut, "json", false, "Output as JSON (full entry fields including good/ship/waypoint attribution)")

	return cmd
}

// newLedgerReportCommand creates the ledger report command group
func newLedgerReportCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Generate financial reports",
		Long: `Generate profit & loss and cash flow reports.

Reports analyze transactions over a specified date range to provide
financial insights including revenue, expenses, and net profit.

Examples:
  spacetraders ledger report profit-loss --start-date 2024-01-01 --end-date 2024-01-31
  spacetraders ledger report cash-flow --start-date 2024-01-15 --end-date 2024-01-22`,
	}

	cmd.AddCommand(newLedgerProfitLossCommand())
	cmd.AddCommand(newLedgerCashFlowCommand())

	return cmd
}

// newLedgerProfitLossCommand creates the profit & loss report subcommand
func newLedgerProfitLossCommand() *cobra.Command {
	var (
		startDate string
		endDate   string
	)

	cmd := &cobra.Command{
		Use:   "profit-loss",
		Short: "Generate profit & loss statement",
		Long: `Generate a profit & loss (P&L) statement for a date range.

The P&L statement shows:
- Total revenue by category
- Total expenses by category
- Net profit (revenue - expenses)

Example:
  spacetraders ledger report profit-loss --player-id 1 \
    --start-date 2024-01-01 --end-date 2024-01-31`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProfitLoss(startDate, endDate)
		},
	}

	cmd.Flags().StringVar(&startDate, "start-date", "", "Start date (YYYY-MM-DD) [required]")
	cmd.Flags().StringVar(&endDate, "end-date", "", "End date (YYYY-MM-DD) [required]")
	cmd.MarkFlagRequired("start-date")
	cmd.MarkFlagRequired("end-date")

	return cmd
}

// newLedgerCashFlowCommand creates the cash flow report subcommand
func newLedgerCashFlowCommand() *cobra.Command {
	var (
		startDate string
		endDate   string
		groupBy   string
	)

	cmd := &cobra.Command{
		Use:   "cash-flow",
		Short: "Generate cash flow statement",
		Long: `Generate a cash flow statement grouped by category.

The cash flow statement shows:
- Total inflow (income) by category
- Total outflow (expenses) by category
- Net cash flow by category
- Number of transactions per category

Example:
  spacetraders ledger report cash-flow --player-id 1 \
    --start-date 2024-01-15 --end-date 2024-01-22`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCashFlow(startDate, endDate, groupBy)
		},
	}

	cmd.Flags().StringVar(&startDate, "start-date", "", "Start date (YYYY-MM-DD) [required]")
	cmd.Flags().StringVar(&endDate, "end-date", "", "End date (YYYY-MM-DD) [required]")
	cmd.Flags().StringVar(&groupBy, "group-by", "category", "Group by (category)")
	cmd.MarkFlagRequired("start-date")
	cmd.MarkFlagRequired("end-date")

	return cmd
}

// runLedgerList executes the ledger list command
func runLedgerList(flags ledgerListFlags) error {
	db, err := openDatabase()
	if err != nil {
		return err
	}

	transactionRepo := persistence.NewGormTransactionRepository(db)
	playerRepo := persistence.NewGormPlayerRepository(db)
	playerResolver := player.NewPlayerResolver(playerRepo)
	handler := queries.NewGetTransactionsHandler(transactionRepo, playerResolver)

	// Resolve the effective player (flags > persisted default) into a concrete ID,
	// so `ledger list` honors the default set via `config set-player`.
	ctx := context.Background()
	resolvedPlayer, err := resolveDefaultPlayer(ctx, playerRepo)
	if err != nil {
		return err
	}

	start, end, err := flags.dateRange()
	if err != nil {
		return err
	}

	query := &queries.GetTransactionsQuery{
		PlayerID:  resolvedPlayer.ID.Value(),
		StartDate: start,
		EndDate:   end,
		Limit:     flags.limit,
		Offset:    flags.offset,
		OrderBy:   flags.orderBy,
	}

	if flags.category != "" {
		query.Category = &flags.category
	}
	if flags.txType != "" {
		query.TransactionType = &flags.txType
	}

	result, err := handler.Handle(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to query transactions: %w", err)
	}

	response := result.(*queries.GetTransactionsResponse)

	return renderTransactionList(os.Stdout, response, flags.jsonOut)
}

// dateRange resolves the optional --start-date/--end-date pair. An empty bound stays
// nil (unbounded); the end bound covers the whole calendar day.
func (f ledgerListFlags) dateRange() (*time.Time, *time.Time, error) {
	var start, end *time.Time
	if f.startDate != "" {
		parsed, err := time.Parse(ledgerDateLayout, f.startDate)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid start date format: %w", err)
		}
		start = &parsed
	}
	if f.endDate != "" {
		parsed, err := time.Parse(ledgerDateLayout, f.endDate)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid end date format: %w", err)
		}
		endOfDay := parsed.Add(23*time.Hour + 59*time.Minute + 59*time.Second)
		end = &endOfDay
	}
	return start, end, nil
}

// runProfitLoss executes the profit & loss report command
func runProfitLoss(startDate, endDate string) error {
	start, err := time.Parse(ledgerDateLayout, startDate)
	if err != nil {
		return fmt.Errorf("invalid start date format: %w", err)
	}
	end, err := time.Parse(ledgerDateLayout, endDate)
	if err != nil {
		return fmt.Errorf("invalid end date format: %w", err)
	}
	end = end.Add(23*time.Hour + 59*time.Minute + 59*time.Second)

	db, err := openDatabase()
	if err != nil {
		return err
	}

	transactionRepo := persistence.NewGormTransactionRepository(db)
	playerRepo := persistence.NewGormPlayerRepository(db)
	handler := queries.NewGetProfitLossHandler(transactionRepo)

	// Resolve the effective player (flags > persisted default), same as
	// `ledger list`, so `ledger report profit-loss` honors the default set
	// via `config set-player` instead of hard-requiring --player-id.
	ctx := context.Background()
	resolvedPlayer, err := resolveDefaultPlayer(ctx, playerRepo)
	if err != nil {
		return err
	}

	result, err := handler.Handle(ctx, &queries.GetProfitLossQuery{
		PlayerID:  resolvedPlayer.ID.Value(),
		StartDate: start,
		EndDate:   end,
	})
	if err != nil {
		return fmt.Errorf("failed to generate P&L report: %w", err)
	}

	response := result.(*queries.GetProfitLossResponse)

	displayProfitLoss(response)

	return nil
}

// runCashFlow executes the cash flow report command
func runCashFlow(startDate, endDate, groupBy string) error {
	start, err := time.Parse(ledgerDateLayout, startDate)
	if err != nil {
		return fmt.Errorf("invalid start date format: %w", err)
	}
	end, err := time.Parse(ledgerDateLayout, endDate)
	if err != nil {
		return fmt.Errorf("invalid end date format: %w", err)
	}
	end = end.Add(23*time.Hour + 59*time.Minute + 59*time.Second)

	db, err := openDatabase()
	if err != nil {
		return err
	}

	transactionRepo := persistence.NewGormTransactionRepository(db)
	playerRepo := persistence.NewGormPlayerRepository(db)
	handler := queries.NewGetCashFlowHandler(transactionRepo)

	// Resolve the effective player (flags > persisted default), same as
	// `ledger list`, so `ledger report cash-flow` honors the default set via
	// `config set-player` instead of hard-requiring --player-id.
	ctx := context.Background()
	resolvedPlayer, err := resolveDefaultPlayer(ctx, playerRepo)
	if err != nil {
		return err
	}

	result, err := handler.Handle(ctx, &queries.GetCashFlowQuery{
		PlayerID:  resolvedPlayer.ID.Value(),
		StartDate: start,
		EndDate:   end,
		GroupBy:   groupBy,
	})
	if err != nil {
		return fmt.Errorf("failed to generate cash flow report: %w", err)
	}

	response := result.(*queries.GetCashFlowResponse)

	displayCashFlow(response)

	return nil
}

// renderTransactionList writes the transaction list to out, either as a table
// (with good/ship/waypoint attribution columns) or, when jsonOut is set, as
// machine-readable JSON carrying the full entry fields.
func renderTransactionList(out io.Writer, response *queries.GetTransactionsResponse, jsonOut bool) error {
	if jsonOut {
		return writeJSON(out, toLedgerJSON(response))
	}

	if len(response.Transactions) == 0 {
		fmt.Fprintln(out, "No transactions found")
		return nil
	}

	fmt.Fprintf(out, "\nTRANSACTIONS (Showing %d of %d total)\n", len(response.Transactions), response.Total)
	fmt.Fprintln(out, "─────────────────────────────────────────────────────────────────────────────")

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "Timestamp\tType\tCategory\tGood\tShip\tWaypoint\tAmount\tBalance")
	fmt.Fprintln(w, "─────────\t────\t────────\t────\t────\t────────\t──────\t───────")

	for _, tx := range response.Transactions {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			tx.Timestamp.Format("2006-01-02 15:04:05"),
			tx.Type,
			tx.Category,
			orDash(metaString(tx.Metadata, "good_symbol")),
			orDash(metaString(tx.Metadata, "ship_symbol")),
			orDash(metaString(tx.Metadata, "waypoint")),
			formatAmount(tx.Amount),
			formatCredits(tx.BalanceAfter),
		)
	}

	w.Flush()
	fmt.Fprintln(out, "─────────────────────────────────────────────────────────────────────────────")
	fmt.Fprintf(out, "Total: %d transactions\n\n", response.Total)
	return nil
}

// ledgerJSONEntry is the machine-readable form of a ledger transaction. The
// attribution fields (good/ship/waypoint) are hoisted out of metadata to the
// top level for easy piping (e.g. `jq`), while the full metadata map is
// preserved so no recorded field is dropped.
type ledgerJSONEntry struct {
	ID            string                 `json:"id"`
	Timestamp     time.Time              `json:"timestamp"`
	Type          string                 `json:"type"`
	Category      string                 `json:"category"`
	Good          string                 `json:"good,omitempty"`
	Ship          string                 `json:"ship,omitempty"`
	Waypoint      string                 `json:"waypoint,omitempty"`
	Amount        int                    `json:"amount"`
	BalanceBefore int                    `json:"balance_before"`
	BalanceAfter  int                    `json:"balance_after"`
	Description   string                 `json:"description,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
}

// ledgerJSONOutput is the top-level JSON envelope for `ledger list --json`.
type ledgerJSONOutput struct {
	Total        int               `json:"total"`
	Shown        int               `json:"shown"`
	Transactions []ledgerJSONEntry `json:"transactions"`
}

// toLedgerJSON maps a transactions response to its JSON envelope.
func toLedgerJSON(response *queries.GetTransactionsResponse) ledgerJSONOutput {
	entries := make([]ledgerJSONEntry, 0, len(response.Transactions))
	for _, tx := range response.Transactions {
		entries = append(entries, ledgerJSONEntry{
			ID:            tx.ID,
			Timestamp:     tx.Timestamp,
			Type:          tx.Type,
			Category:      tx.Category,
			Good:          metaString(tx.Metadata, "good_symbol"),
			Ship:          metaString(tx.Metadata, "ship_symbol"),
			Waypoint:      metaString(tx.Metadata, "waypoint"),
			Amount:        tx.Amount,
			BalanceBefore: tx.BalanceBefore,
			BalanceAfter:  tx.BalanceAfter,
			Description:   tx.Description,
			Metadata:      tx.Metadata,
		})
	}
	return ledgerJSONOutput{
		Total:        response.Total,
		Shown:        len(response.Transactions),
		Transactions: entries,
	}
}

// metaString extracts a string value from transaction metadata, returning ""
// when the key is absent or not a string. Non-trade entries (refuel, contracts)
// legitimately lack good/waypoint keys, so callers treat "" as "not attributed".
func metaString(metadata map[string]interface{}, key string) string {
	if metadata == nil {
		return ""
	}
	if v, ok := metadata[key].(string); ok {
		return v
	}
	return ""
}

// orDash renders empty attribution cells as "-" so table columns stay aligned.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// displayProfitLoss formats and displays P&L report
func displayProfitLoss(response *queries.GetProfitLossResponse) {
	fmt.Printf("\nPROFIT & LOSS STATEMENT\n")
	fmt.Printf("Period: %s\n", response.Period)
	fmt.Println("─────────────────────────────────────────────────────────────────────────────")

	fmt.Println("\nREVENUE")
	for category, amount := range response.RevenueBreakdown {
		fmt.Printf("  %-25s %s\n", category+":", formatCredits(amount))
	}
	fmt.Println("                          ─────────────")
	fmt.Printf("  %-25s %s\n", "Total Revenue:", formatCredits(response.TotalRevenue))

	fmt.Println("\nEXPENSES")
	for category, amount := range response.ExpenseBreakdown {
		fmt.Printf("  %-25s %s\n", category+":", formatCredits(-amount))
	}
	fmt.Println("                          ─────────────")
	fmt.Printf("  %-25s %s\n", "Total Expenses:", formatCredits(-response.TotalExpenses))

	fmt.Println("\n─────────────────────────────────────────────────────────────────────────────")
	fmt.Printf("NET PROFIT:               %s\n", formatAmount(response.NetProfit))
	fmt.Println("─────────────────────────────────────────────────────────────────────────────")
}

// displayCashFlow formats and displays cash flow report
func displayCashFlow(response *queries.GetCashFlowResponse) {
	fmt.Printf("\nCASH FLOW STATEMENT (By Category)\n")
	fmt.Printf("Period: %s\n", response.Period)
	fmt.Println("─────────────────────────────────────────────────────────────────────────────")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "Category\tInflow\tOutflow\tNet Flow\tTransactions")
	fmt.Fprintln(w, "────────\t──────\t───────\t────────\t────────────")

	totalInflow := 0
	totalOutflow := 0
	totalNetFlow := 0
	totalTransactions := 0

	for _, cat := range response.Categories {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\n",
			cat.Category,
			formatCredits(cat.TotalInflow),
			formatCredits(-cat.TotalOutflow),
			formatAmount(cat.NetFlow),
			cat.Transactions,
		)
		totalInflow += cat.TotalInflow
		totalOutflow += cat.TotalOutflow
		totalNetFlow += cat.NetFlow
		totalTransactions += cat.Transactions
	}

	fmt.Fprintln(w, "────────\t──────\t───────\t────────\t────────────")
	fmt.Fprintf(w, "TOTAL\t%s\t%s\t%s\t%d\n",
		formatCredits(totalInflow),
		formatCredits(-totalOutflow),
		formatAmount(totalNetFlow),
		totalTransactions,
	)

	w.Flush()
	fmt.Println("─────────────────────────────────────────────────────────────────────────────")
}

// formatAmount formats an amount with +/- sign
func formatAmount(amount int) string {
	if amount >= 0 {
		return fmt.Sprintf("+%s", formatCredits(amount))
	}
	return formatCredits(amount)
}

// formatCredits formats credits with thousands separator
func formatCredits(credits int) string {
	if credits < 0 {
		return "-" + addThousandsSeparator(-credits)
	}
	return addThousandsSeparator(credits)
}

// addThousandsSeparator adds commas to a number (e.g., 1234567 -> "1,234,567")
func addThousandsSeparator(n int) string {
	str := fmt.Sprintf("%d", n)
	if len(str) <= 3 {
		return str
	}

	// Insert commas from right to left
	var result []byte
	for i, c := range str {
		if i > 0 && (len(str)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, byte(c))
	}
	return string(result)
}
