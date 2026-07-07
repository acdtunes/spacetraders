package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	adapter "github.com/andrescamacho/spacetraders-go/internal/adapters/telemetry"
	domain "github.com/andrescamacho/spacetraders-go/internal/domain/telemetry"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// tokenCollector is the read port the token-report verb consumes;
// telemetry.Collector satisfies it. Kept narrow so the render/compute path is
// unit-tested with a fake instead of a live `gc`.
type tokenCollector interface {
	Collect(ctx context.Context, since time.Time) ([]domain.SessionUsage, error)
}

// runTokenReport collects per-session token usage over the last `days`, derives
// the fleet report, and renders it as JSON or a table to w. It is the testable
// core of the `captain tokens` verb.
func runTokenReport(ctx context.Context, c tokenCollector, captainAlias string, days int, now time.Time, jsonOut bool, w io.Writer) error {
	since := now.AddDate(0, 0, -days)
	sessions, err := c.Collect(ctx, since)
	if err != nil {
		return fmt.Errorf("collect token telemetry: %w", err)
	}
	report := domain.ComputeReport(sessions, captainAlias, days)

	if jsonOut {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	return renderTokenReport(report, w)
}

func renderTokenReport(report domain.Report, w io.Writer) error {
	hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(hw, "Window (days)\t%d\n", report.WindowDays)
	fmt.Fprintf(hw, "Total tokens\t%d\n", report.TotalTokens)
	fmt.Fprintf(hw, "Tokens/day\t%.0f\n", report.TokensPerDay)
	fmt.Fprintf(hw, "Tokens/wake (%s)\t%.0f\n", report.CaptainAlias, report.TokensPerWake)
	if err := hw.Flush(); err != nil {
		return err
	}

	if len(report.Sessions) == 0 {
		_, err := fmt.Fprintln(w, "\nNo token telemetry (no sessions with transcripts in window).")
		return err
	}

	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tTOTAL\tINPUT\tOUTPUT\tCACHE_CREATE\tCACHE_READ\tTURNS\tTOKENS/TURN")
	for _, s := range report.Sessions {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%.0f\n",
			s.Alias, s.Usage.Total(), s.Usage.Input, s.Usage.Output,
			s.Usage.CacheCreation, s.Usage.CacheRead, s.Turns, s.TokensPerTurn())
	}
	return tw.Flush()
}

func newCaptainTokensCommand() *cobra.Command {
	var days int
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "tokens",
		Short: "Per-session token/usage telemetry (tokens/wake + tokens/day)",
		Long: `Report the fleet's token spend over a recent window, aggregated per agent
session from the claude-code transcripts the gc-managed sessions produced.

Surfaces the two rates the surveyor ritual needs but has had no data source
for: tokens/day (fleet burn) and tokens/wake (the captain's per-activation
cost). Per-agent rows break the four usage components out separately (input,
output, cache-create, cache-read) so a cost model can price them. "Turns" is a
session's inbound prompt count — for the captain that is its wake count.

This is additive read-only telemetry: it observes transcripts already written
by the externally-run sessions and never touches the wake path.

Examples:
  spacetraders captain tokens
  spacetraders captain tokens --days 1 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if days <= 0 {
				return fmt.Errorf("--days must be positive")
			}
			cfg, err := config.LoadConfig("")
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}
			collector := adapter.NewLiveCollector(
				gcBinOrDefault(cfg.Captain.GCBin),
				cfg.Captain.CityDir,
				os.Getenv("CLAUDE_PROJECTS_ROOT"),
			)
			return runTokenReport(context.Background(), collector, captainAliasOrDefault(cfg.Captain.CaptainAgent), days, time.Now(), jsonOut, os.Stdout)
		},
	}

	cmd.Flags().IntVar(&days, "days", 7, "Window size in days")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func gcBinOrDefault(v string) string {
	if v == "" {
		return "gc"
	}
	return v
}

func captainAliasOrDefault(v string) string {
	if v == "" {
		return "captain"
	}
	return v
}
