package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// NewHealthCommand creates the health command
func NewHealthCommand() *cobra.Command {
	var showAPIBudget bool

	cmd := &cobra.Command{
		Use:   "health",
		Short: "Check daemon health status",
		Long:  `Verify that the daemon is running and responsive.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			health, err := client.HealthCheck(ctx)
			if err != nil {
				return fmt.Errorf("health check failed: %w", err)
			}

			fmt.Println("✓ Daemon is healthy")
			fmt.Printf("  Status:            %s\n", health.Status)
			fmt.Printf("  Version:           %s\n", health.Version)
			fmt.Printf("  Active Containers: %d\n", health.ActiveContainers)

			if showAPIBudget {
				budget, err := client.GetAPIBudget(ctx)
				if err != nil {
					return fmt.Errorf("get API budget failed: %w", err)
				}
				printAPIBudget(budget)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&showAPIBudget, "api-budget", false, "Also show API request-budget observability (per-hull req/s, utilization vs ceiling, duty-cycle KPI)")

	return cmd
}

// printAPIBudget renders API request-budget observability: global
// utilization vs the rate ceiling, top consumers by hull, and the duty-cycle
// KPI (ship-hours earning/day per hull). PerHull and Hulls arrive pre-sorted
// descending by consumption from the domain layer (apibudget.ComputeReport,
// dutycycle.ComputeReport), so this prints them in the order received.
func printAPIBudget(budget *pb.GetAPIBudgetResponse) {
	fmt.Println()
	fmt.Println("API Request Budget (current):")
	printAPIBudgetReport(budget.Current)

	fmt.Println()
	fmt.Println("API Request Budget (rolling 5m):")
	printAPIBudgetReport(budget.Rolling_5M)

	fmt.Println()
	fmt.Printf("Duty Cycle (ship-hours earning/day per hull, %.1fh window):\n", budget.DutyCycle.GetWindowHours())
	if len(budget.DutyCycle.GetHulls()) == 0 {
		fmt.Println("  (no samples yet)")
	}
	for _, h := range budget.DutyCycle.GetHulls() {
		fmt.Printf("  %-16s %6.1fh earning / %6.1fh idle  (%5.1f%%)\n", h.GetHull(), h.GetEarningHours(), h.GetIdleHours(), h.GetEarningPct())
	}
}

func printAPIBudgetReport(r *pb.APIBudgetReport) {
	if r == nil {
		fmt.Println("  (no data)")
		return
	}

	fmt.Printf("  Global:           %.2f req/s (ceiling %.2f req/s, %.1f%% utilized, headroom %.2f req/s)\n",
		r.GetGlobalReqPerSec(), r.GetCeilingReqPerSec(), r.GetUtilizationPct(), r.GetHeadroomReqPerSec())
	fmt.Printf("  429s:             %d (%.2f/min)\n", r.GetRateLimited_429(), r.GetRateLimited_429PerMin())
	fmt.Printf("  Hulls to ceiling: %.1f\n", r.GetHullsToCeiling())

	if len(r.GetPurposeSharePct()) > 0 {
		fmt.Printf("  Purpose share:    poll %.1f%%  transact %.1f%%  retry %.1f%%\n",
			r.GetPurposeSharePct()["poll"], r.GetPurposeSharePct()["transact"], r.GetPurposeSharePct()["retry"])
	}

	perHull := r.GetPerHull()
	if len(perHull) == 0 {
		return
	}

	fmt.Println("  Top consumers:")
	limit := len(perHull)
	if limit > 10 {
		limit = 10
	}
	for _, h := range perHull[:limit] {
		fmt.Printf("    %-16s %6.2f req/s  (%d requests)\n", h.GetHull(), h.GetReqPerSec(), h.GetRequestsInWindow())
	}
	if len(perHull) > limit {
		fmt.Printf("    ... and %d more\n", len(perHull)-limit)
	}
}
