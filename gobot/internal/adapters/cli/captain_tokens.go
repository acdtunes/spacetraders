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

// QuotaSummary is the sp-1vkr weekly-quota VISIBILITY proxy. No
// machine-readable Claude/Anthropic billing or quota source exists anywhere
// in this codebase (confirmed by investigation before this feature was
// built), so this compares the report's own windowed token total against an
// operator-CONFIGURED weekly budget, styled after the existing
// credits.threshold alert but computed CLI-side only: it is NOT a live quota
// read, and it never raises a captain.Event/detector.
//
// The comparison is only meaningful when the report window is a full week
// (the `captain tokens`/`captain report` default, --days 7) — WindowDays
// travels alongside it (top-level on tokensReportOutput / inside
// EngineReport) so a shorter or longer window is never silently misread as a
// weekly figure.
//
// Nil (and omitted from JSON via omitempty) whenever the operator has not
// configured weekly_token_budget.
type QuotaSummary struct {
	BudgetTokens int64   `json:"budget_tokens"`
	UsedTokens   int64   `json:"used_tokens"`
	UsedPct      float64 `json:"used_pct"`
	ThresholdPct int     `json:"alert_threshold_pct"`
	Alert        bool    `json:"alert"`
}

// computeQuotaSummary returns nil when budgetTokens <= 0 (unconfigured):
// asserting a percentage against a zero/absent budget would be a nonsense
// number, not a graceful "off" state.
func computeQuotaSummary(usedTokens, budgetTokens int64, alertThresholdPct int) *QuotaSummary {
	if budgetTokens <= 0 {
		return nil
	}
	pct := float64(usedTokens) / float64(budgetTokens) * 100
	return &QuotaSummary{
		BudgetTokens: budgetTokens,
		UsedTokens:   usedTokens,
		UsedPct:      pct,
		ThresholdPct: alertThresholdPct,
		Alert:        alertThresholdPct > 0 && pct >= float64(alertThresholdPct),
	}
}

// tokenSessionRow is one session's row in the `captain tokens` output: the
// existing per-session usage fields (promoted from the embedded
// domain.SessionUsage) plus TokensSinceSpawn (sp-0zx9) — that session's token
// spend across its ENTIRE transcript, not just the `--days` reporting
// window, which is what makes the cost of skipping a rollover visible
// regardless of how narrow a window the operator asked to view.
type tokenSessionRow struct {
	domain.SessionUsage
	TokensSinceSpawn int64 `json:"tokens_since_spawn"`
}

// tokensReportOutput is the JSON/render shape for `captain tokens`. It
// embeds domain.Report so WindowDays/TotalTokens/TokensPerDay/TokensPerWake/
// CaptainAlias are promoted unchanged, but shadows Sessions with the richer
// tokenSessionRow (sp-0zx9 tokens-since-spawn) and adds Quota (sp-1vkr) —
// without changing domain.Report/SessionUsage themselves, which live in
// internal/domain/telemetry, out of this feature's scope. Go's encoding/json
// promotes an embedded field only when no shallower field shares its JSON
// key, so the explicit Sessions field below (depth 0) suppresses promotion
// of the embedded Report.Sessions (depth 1) for both field access and
// encoding.
type tokensReportOutput struct {
	domain.Report
	Sessions []tokenSessionRow `json:"sessions"`
	Quota    *QuotaSummary     `json:"quota,omitempty"`
}

// buildTokenRows attaches each session's lifetime (since-spawn) token total
// onto its windowed report row, matched by SessionKey — not Alias, since one
// alias can span multiple session keys across restarts, and
// domain.ComputeReport is confirmed to pass SessionKey through unchanged
// (one row per input SessionUsage, sorted but never merged/aggregated). A
// session present in the windowed report but missing from sinceSpawn (e.g.
// the best-effort unbounded collect failed or returned nothing for it) gets
// a zero TokensSinceSpawn rather than blocking the rest of the report.
func buildTokenRows(sessions []domain.SessionUsage, sinceSpawn []domain.SessionUsage) []tokenSessionRow {
	totals := make(map[string]int64, len(sinceSpawn))
	for _, s := range sinceSpawn {
		totals[s.SessionKey] = s.Usage.Total()
	}
	rows := make([]tokenSessionRow, 0, len(sessions))
	for _, s := range sessions {
		rows = append(rows, tokenSessionRow{SessionUsage: s, TokensSinceSpawn: totals[s.SessionKey]})
	}
	return rows
}

// runTokenReport collects per-session token usage over the last `days`, derives
// the fleet report, and renders it as JSON or a table to w. It is the testable
// core of the `captain tokens` verb.
//
// budgetTokens/alertThresholdPct feed the sp-1vkr quota-visibility block
// (computeQuotaSummary); budgetTokens <= 0 disables it entirely (unconfigured).
func runTokenReport(ctx context.Context, c tokenCollector, captainAlias string, days int, now time.Time, budgetTokens int64, alertThresholdPct int, jsonOut bool, w io.Writer) error {
	since := now.AddDate(0, 0, -days)
	sessions, err := c.Collect(ctx, since)
	if err != nil {
		return fmt.Errorf("collect token telemetry: %w", err)
	}
	report := domain.ComputeReport(sessions, captainAlias, days)

	// Tokens-since-spawn (sp-0zx9) is additive visibility layered on top of the
	// windowed report: a second, unbounded collect (Collect's documented
	// contract: a zero `since` returns the whole transcript) gives each
	// session's lifetime total. Best-effort — its failure must never break the
	// windowed report the captain already relies on.
	var sinceSpawn []domain.SessionUsage
	if full, err := c.Collect(ctx, time.Time{}); err != nil {
		fmt.Fprintf(os.Stderr, "captain tokens: tokens-since-spawn unavailable: %v\n", err)
	} else {
		sinceSpawn = full
	}

	out := tokensReportOutput{
		Report:   report,
		Sessions: buildTokenRows(report.Sessions, sinceSpawn),
		Quota:    computeQuotaSummary(report.TotalTokens, budgetTokens, alertThresholdPct),
	}

	if jsonOut {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	return renderTokenReport(out, w)
}

func renderTokenReport(out tokensReportOutput, w io.Writer) error {
	report := out.Report
	hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(hw, "Window (days)\t%d\n", report.WindowDays)
	fmt.Fprintf(hw, "Total tokens\t%d\n", report.TotalTokens)
	fmt.Fprintf(hw, "Tokens/day\t%.0f\n", report.TokensPerDay)
	fmt.Fprintf(hw, "Tokens/wake (%s)\t%.0f\n", report.CaptainAlias, report.TokensPerWake)
	if out.Quota != nil {
		fmt.Fprintf(hw, "Weekly budget\t%d\n", out.Quota.BudgetTokens)
		fmt.Fprintf(hw, "Budget used\t%.1f%% (of %d-day window)\n", out.Quota.UsedPct, report.WindowDays)
		if out.Quota.Alert {
			fmt.Fprintf(hw, "Quota alert\tTHRESHOLD CROSSED (>=%d%%)\n", out.Quota.ThresholdPct)
		}
	}
	if err := hw.Flush(); err != nil {
		return err
	}

	if len(out.Sessions) == 0 {
		_, err := fmt.Fprintln(w, "\nNo token telemetry (no sessions with transcripts in window).")
		return err
	}

	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tTOTAL\tINPUT\tOUTPUT\tCACHE_CREATE\tCACHE_READ\tTURNS\tTOKENS/TURN\tSINCE_SPAWN")
	for _, s := range out.Sessions {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%.0f\t%d\n",
			s.Alias, s.Usage.Total(), s.Usage.Input, s.Usage.Output,
			s.Usage.CacheCreation, s.Usage.CacheRead, s.Turns, s.TokensPerTurn(), s.TokensSinceSpawn)
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
SINCE_SPAWN is that session's token total across its entire transcript, not
just this window (sp-0zx9) — the cost of skipping a rollover.

When captain.weekly_token_budget is configured, a quota block compares this
window's total tokens against that budget (sp-1vkr): a CONFIGURED proxy, not
a live Claude/Anthropic quota read. Most meaningful at the default --days 7.

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
			return runTokenReport(context.Background(), collector, captainAliasOrDefault(cfg.Captain.CaptainAgent), days, time.Now(),
				weeklyTokenBudgetOrDefault(cfg.Captain.WeeklyTokenBudget), quotaAlertThresholdPctOrDefault(cfg.Captain.QuotaAlertThresholdPct),
				jsonOut, os.Stdout)
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

// weeklyTokenBudgetOrDefault reads the sp-1vkr configured-budget proxy. Nil
// (unconfigured) maps to 0, which computeQuotaSummary treats as "disabled" —
// there is no default budget, unlike QuotaAlertThresholdPct below, because a
// fabricated budget number would be actively misleading.
func weeklyTokenBudgetOrDefault(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

// quotaAlertThresholdPctOrDefault reads the sp-1vkr alert-threshold percent.
// defaults.go already back-fills this to 80 when config is loaded normally;
// the nil-safe fallback here (0, which computeQuotaSummary's
// `alertThresholdPct > 0` guard treats as "never alert") only matters for
// callers that bypass that defaulting (e.g. a zero-value CaptainConfig).
func quotaAlertThresholdPctOrDefault(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}
