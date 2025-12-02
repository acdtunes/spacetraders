package cli

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/ledger/queries"
	"github.com/andrescamacho/spacetraders-go/internal/application/player"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

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

	// Add subcommands
	cmd.AddCommand(newLedgerListCommand())
	cmd.AddCommand(newLedgerReportCommand())

	return cmd
}

// newLedgerListCommand creates the ledger list subcommand
func newLedgerListCommand() *cobra.Command {
	var (
		startDate   string
		endDate     string
		category    string
		txType      string
		limit       int
		offset      int
		orderBy     string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List transactions",
		Long: `List financial transactions with optional filtering.

Transactions can be filtered by date range, category, and type.
Results are ordered by timestamp descending (newest first) by default.

Categories:
  FUEL_COSTS        - Fuel expenses
  TRADING_REVENUE   - Income from selling cargo
  TRADING_COSTS     - Expenses from purchasing cargo
  SHIP_INVESTMENTS  - Expenses from purchasing ships
  CONTRACT_REVENUE  - Income from contracts

Transaction Types:
  REFUEL              - Ship refueling
  PURCHASE_CARGO      - Cargo purchase
  SELL_CARGO          - Cargo sale
  PURCHASE_SHIP       - Ship purchase
  CONTRACT_ACCEPTED   - Contract acceptance payment
  CONTRACT_FULFILLED  - Contract fulfillment payment

Examples:
  spacetraders ledger list --player-id 1 --limit 10
  spacetraders ledger list --category FUEL_COSTS
  spacetraders ledger list --start-date 2024-01-15 --end-date 2024-01-22`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLedgerList(playerID, startDate, endDate, category, txType, limit, offset, orderBy)
		},
	}

	cmd.Flags().StringVar(&startDate, "start-date", "", "Start date (YYYY-MM-DD)")
	cmd.Flags().StringVar(&endDate, "end-date", "", "End date (YYYY-MM-DD)")
	cmd.Flags().StringVar(&category, "category", "", "Filter by category")
	cmd.Flags().StringVar(&txType, "type", "", "Filter by transaction type")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum number of transactions to return")
	cmd.Flags().IntVar(&offset, "offset", 0, "Number of transactions to skip")
	cmd.Flags().StringVar(&orderBy, "order-by", "timestamp DESC", "Sort order")

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
			return runProfitLoss(playerID, startDate, endDate)
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
			return runCashFlow(playerID, startDate, endDate, groupBy)
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
func runLedgerList(playerID int, startDate, endDate, category, txType string, limit, offset int, orderBy string) error {
	if playerID == 0 {
		return fmt.Errorf("--player-id flag is required")
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

	// Create repository and handler
	transactionRepo := persistence.NewGormTransactionRepository(db)
	playerRepo := persistence.NewGormPlayerRepository(db)
	playerResolver := player.NewPlayerResolver(playerRepo)
	handler := queries.NewGetTransactionsHandler(transactionRepo, playerResolver)

	// Parse dates
	var start, end *time.Time
	if startDate != "" {
		parsed, err := time.Parse("2006-01-02", startDate)
		if err != nil {
			return fmt.Errorf("invalid start date format: %w", err)
		}
		start = &parsed
	}
	if endDate != "" {
		parsed, err := time.Parse("2006-01-02", endDate)
		if err != nil {
			return fmt.Errorf("invalid end date format: %w", err)
		}
		// Set to end of day
		endOfDay := parsed.Add(23*time.Hour + 59*time.Minute + 59*time.Second)
		end = &endOfDay
	}

	// Build query
	query := &queries.GetTransactionsQuery{
		PlayerID:  playerID,
		StartDate: start,
		EndDate:   end,
		Limit:     limit,
		Offset:    offset,
		OrderBy:   orderBy,
	}

	if category != "" {
		query.Category = &category
	}
	if txType != "" {
		query.TransactionType = &txType
	}

	// Execute query
	ctx := context.Background()
	result, err := handler.Handle(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to query transactions: %w", err)
	}

	response := result.(*queries.GetTransactionsResponse)

	// Display results
	displayTransactionList(response)

	return nil
}

// runProfitLoss executes the profit & loss report command
func runProfitLoss(playerID int, startDate, endDate string) error {
	if playerID == 0 {
		return fmt.Errorf("--player-id flag is required")
	}

	// Parse dates
	start, err := time.Parse("2006-01-02", startDate)
	if err != nil {
		return fmt.Errorf("invalid start date format: %w", err)
	}
	end, err := time.Parse("2006-01-02", endDate)
	if err != nil {
		return fmt.Errorf("invalid end date format: %w", err)
	}
	// Set to end of day
	end = end.Add(23*time.Hour + 59*time.Minute + 59*time.Second)

	// Load config and connect to database
	cfg, err := config.LoadConfig("")
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	db, err := database.NewConnection(&cfg.Database)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}

	// Create repository and handler
	transactionRepo := persistence.NewGormTransactionRepository(db)
	handler := queries.NewGetProfitLossHandler(transactionRepo)

	// Execute query
	ctx := context.Background()
	result, err := handler.Handle(ctx, &queries.GetProfitLossQuery{
		PlayerID:  playerID,
		StartDate: start,
		EndDate:   end,
	})
	if err != nil {
		return fmt.Errorf("failed to generate P&L report: %w", err)
	}

	response := result.(*queries.GetProfitLossResponse)

	// Display results
	displayProfitLoss(response)

	return nil
}

// runCashFlow executes the cash flow report command
func runCashFlow(playerID int, startDate, endDate, groupBy string) error {
	if playerID == 0 {
		return fmt.Errorf("--player-id flag is required")
	}

	// Parse dates
	start, err := time.Parse("2006-01-02", startDate)
	if err != nil {
		return fmt.Errorf("invalid start date format: %w", err)
	}
	end, err := time.Parse("2006-01-02", endDate)
	if err != nil {
		return fmt.Errorf("invalid end date format: %w", err)
	}
	// Set to end of day
	end = end.Add(23*time.Hour + 59*time.Minute + 59*time.Second)

	// Load config and connect to database
	cfg, err := config.LoadConfig("")
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	db, err := database.NewConnection(&cfg.Database)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}

	// Create repository and handler
	transactionRepo := persistence.NewGormTransactionRepository(db)
	handler := queries.NewGetCashFlowHandler(transactionRepo)

	// Execute query
	ctx := context.Background()
	result, err := handler.Handle(ctx, &queries.GetCashFlowQuery{
		PlayerID:  playerID,
		StartDate: start,
		EndDate:   end,
		GroupBy:   groupBy,
	})
	if err != nil {
		return fmt.Errorf("failed to generate cash flow report: %w", err)
	}

	response := result.(*queries.GetCashFlowResponse)

	// Display results
	displayCashFlow(response)

	return nil
}

// displayTransactionList formats and displays transaction list
func displayTransactionList(response *queries.GetTransactionsResponse) {
	if len(response.Transactions) == 0 {
		fmt.Println("No transactions found")
		return
	}

	fmt.Printf("\nTRANSACTIONS (Showing %d of %d total)\n", len(response.Transactions), response.Total)
	fmt.Println("─────────────────────────────────────────────────────────────────────────────")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "Timestamp\tType\tCategory\tAmount\tBalance")
	fmt.Fprintln(w, "─────────\t────\t────────\t──────\t───────")

	for _, tx := range response.Transactions {
		timestamp := tx.Timestamp.Format("2006-01-02 15:04:05")
		amount := formatAmount(tx.Amount)
		balance := formatCredits(tx.BalanceAfter)

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			timestamp,
			tx.Type,
			tx.Category,
			amount,
			balance,
		)
	}

	w.Flush()
	fmt.Println("─────────────────────────────────────────────────────────────────────────────")
	fmt.Printf("Total: %d transactions\n\n", response.Total)
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
