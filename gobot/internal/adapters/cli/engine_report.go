package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	adaptertelemetry "github.com/andrescamacho/spacetraders-go/internal/adapters/telemetry"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	telemetry "github.com/andrescamacho/spacetraders-go/internal/domain/telemetry"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

type EngineReport struct {
	PlayerID            int            `json:"player_id"`
	WindowDays          int            `json:"window_days"`
	TotalEvents         int            `json:"total_events"`
	EventsPerDay        float64        `json:"events_per_day"`
	AckLatencyP50Sec    float64        `json:"ack_latency_p50_sec"`
	AckLatencyMaxSec    float64        `json:"ack_latency_max_sec"`
	BacklogCount        int            `json:"backlog_count"`
	BacklogOldestAgeSec float64        `json:"backlog_oldest_age_sec"`
	PerType             map[string]int `json:"per_type"`
	// TokenUsage is a compact per-wake/per-day token cost summary attached
	// best-effort from the claude-session transcripts (sp-593x). It is omitted
	// when token telemetry is unavailable (no `gc`, no transcripts) so the
	// events report never depends on it. The full per-agent breakdown lives in
	// `captain tokens`.
	TokenUsage *TokenSummary `json:"token_usage,omitempty"`
}

// TokenSummary is the compact token block surfaced inside the captain report:
// the two rates the surveyor ritual needs (tokens/day fleet burn, tokens/wake
// captain per-activation cost) plus the all-in total over the window.
type TokenSummary struct {
	TotalTokens   int64   `json:"total_tokens"`
	TokensPerDay  float64 `json:"tokens_per_day"`
	TokensPerWake float64 `json:"tokens_per_wake"`
	// Quota is the sp-1vkr weekly-quota visibility proxy (see QuotaSummary in
	// captain_tokens.go, same package): the report's own TotalTokens compared
	// against a CONFIGURED weekly_token_budget. Nil/omitted whenever no budget
	// is configured.
	Quota *QuotaSummary `json:"quota,omitempty"`
}

type reportEventSource interface {
	FindSince(ctx context.Context, playerID int, since time.Time) ([]*captain.Event, error)
}

func computeEngineReport(events []*captain.Event, playerID, days int, now time.Time) EngineReport {
	since := now.AddDate(0, 0, -days)
	report := EngineReport{
		PlayerID:   playerID,
		WindowDays: days,
		PerType:    map[string]int{},
	}

	latencies := make([]float64, 0, len(events))
	var oldestBacklog *time.Time

	for _, event := range events {
		if event.CreatedAt.Before(since) {
			continue
		}
		report.TotalEvents++
		report.PerType[string(event.Type)]++

		if event.ProcessedAt != nil {
			latencies = append(latencies, event.ProcessedAt.Sub(event.CreatedAt).Seconds())
			continue
		}

		report.BacklogCount++
		if oldestBacklog == nil || event.CreatedAt.Before(*oldestBacklog) {
			created := event.CreatedAt
			oldestBacklog = &created
		}
	}

	if days > 0 {
		report.EventsPerDay = float64(report.TotalEvents) / float64(days)
	}
	report.AckLatencyP50Sec, report.AckLatencyMaxSec = latencyPercentiles(latencies)
	if oldestBacklog != nil {
		report.BacklogOldestAgeSec = now.Sub(*oldestBacklog).Seconds()
	}
	return report
}

func latencyPercentiles(latencies []float64) (p50, max float64) {
	if len(latencies) == 0 {
		return 0, 0
	}
	sorted := make([]float64, len(latencies))
	copy(sorted, latencies)
	sort.Float64s(sorted)
	return sorted[len(sorted)/2], sorted[len(sorted)-1]
}

type gormReportEventSource struct {
	db *gorm.DB
}

func (s *gormReportEventSource) FindSince(ctx context.Context, playerID int, since time.Time) ([]*captain.Event, error) {
	var models []persistence.CaptainEventModel
	err := s.db.WithContext(ctx).
		Where("player_id = ? AND created_at >= ?", playerID, since).
		Order("created_at ASC, id ASC").
		Find(&models).Error
	if err != nil {
		return nil, err
	}
	events := make([]*captain.Event, 0, len(models))
	for i := range models {
		m := models[i]
		events = append(events, &captain.Event{
			ID:          m.ID,
			Type:        captain.EventType(m.Type),
			Ship:        m.Ship,
			PlayerID:    m.PlayerID,
			Payload:     m.Payload,
			CreatedAt:   m.CreatedAt,
			ProcessedAt: m.ProcessedAt,
		})
	}
	return events, nil
}

func newReportEventSource() (reportEventSource, error) {
	cfg, err := config.LoadConfig("")
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	db, err := database.NewConnection(&cfg.Database)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}
	return &gormReportEventSource{db: db}, nil
}

// newReportTokenCollector builds the live token collector and captain alias for
// the report's best-effort token block, plus the sp-1vkr quota inputs
// (weekly_token_budget/quota_alert_threshold_pct). On any config failure it
// returns a nil collector so the events report still renders — token
// telemetry (and the quota block riding on it) is additive.
func newReportTokenCollector() (tokenCollector, string, int64, int) {
	cfg, err := config.LoadConfig("")
	if err != nil {
		return nil, "captain", 0, 0
	}
	collector := adaptertelemetry.NewLiveCollector(
		gcBinOrDefault(cfg.Captain.GCBin),
		cfg.Captain.CityDir,
		os.Getenv("CLAUDE_PROJECTS_ROOT"),
	)
	return collector, captainAliasOrDefault(cfg.Captain.CaptainAgent),
		weeklyTokenBudgetOrDefault(cfg.Captain.WeeklyTokenBudget),
		quotaAlertThresholdPctOrDefault(cfg.Captain.QuotaAlertThresholdPct)
}

func runEngineReport(ctx context.Context, source reportEventSource, tc tokenCollector, captainAlias string, playerID, days int, now time.Time, budgetTokens int64, alertThresholdPct int, jsonOut bool, w io.Writer) error {
	since := now.AddDate(0, 0, -days)
	events, err := source.FindSince(ctx, playerID, since)
	if err != nil {
		return fmt.Errorf("failed to load captain events: %w", err)
	}
	report := computeEngineReport(events, playerID, days, now)
	// Token telemetry is additive and best-effort: attach it when a collector
	// is wired and succeeds, but never let its absence or failure (missing `gc`,
	// no transcripts) fail the events report the captain relies on. The quota
	// block (sp-1vkr) rides along inside it for the same reason.
	report.TokenUsage = collectTokenSummary(ctx, tc, captainAlias, days, since, budgetTokens, alertThresholdPct)

	if jsonOut {
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	return renderEngineReport(report, w)
}

// collectTokenSummary returns the compact token block, or nil when no collector
// is wired or collection fails. Errors are swallowed by design (see caller).
func collectTokenSummary(ctx context.Context, tc tokenCollector, captainAlias string, days int, since time.Time, budgetTokens int64, alertThresholdPct int) *TokenSummary {
	if tc == nil {
		return nil
	}
	sessions, err := tc.Collect(ctx, since)
	if err != nil {
		return nil
	}
	rep := telemetry.ComputeReport(sessions, captainAlias, days)
	return &TokenSummary{
		TotalTokens:   rep.TotalTokens,
		TokensPerDay:  rep.TokensPerDay,
		TokensPerWake: rep.TokensPerWake,
		Quota:         computeQuotaSummary(rep.TotalTokens, budgetTokens, alertThresholdPct),
	}
}

func renderEngineReport(report EngineReport, out io.Writer) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "Player\t%d\n", report.PlayerID)
	fmt.Fprintf(w, "Window (days)\t%d\n", report.WindowDays)
	fmt.Fprintf(w, "Total events\t%d\n", report.TotalEvents)
	fmt.Fprintf(w, "Events/day\t%.2f\n", report.EventsPerDay)
	fmt.Fprintf(w, "Ack latency p50 (s)\t%.1f\n", report.AckLatencyP50Sec)
	fmt.Fprintf(w, "Ack latency max (s)\t%.1f\n", report.AckLatencyMaxSec)
	fmt.Fprintf(w, "Backlog count\t%d\n", report.BacklogCount)
	fmt.Fprintf(w, "Backlog oldest age (s)\t%.1f\n", report.BacklogOldestAgeSec)
	if report.TokenUsage != nil {
		fmt.Fprintf(w, "Total tokens\t%d\n", report.TokenUsage.TotalTokens)
		fmt.Fprintf(w, "Tokens/day\t%.0f\n", report.TokenUsage.TokensPerDay)
		fmt.Fprintf(w, "Tokens/wake\t%.0f\n", report.TokenUsage.TokensPerWake)
		if q := report.TokenUsage.Quota; q != nil {
			fmt.Fprintf(w, "Weekly budget\t%d\n", q.BudgetTokens)
			fmt.Fprintf(w, "Budget used\t%.1f%% (of %d-day window)\n", q.UsedPct, report.WindowDays)
			if q.Alert {
				fmt.Fprintf(w, "Quota alert\tTHRESHOLD CROSSED (>=%d%%)\n", q.ThresholdPct)
			}
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}

	types := make([]string, 0, len(report.PerType))
	for t := range report.PerType {
		types = append(types, t)
	}
	sort.Strings(types)
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TYPE\tCOUNT")
	for _, t := range types {
		fmt.Fprintf(tw, "%s\t%d\n", t, report.PerType[t])
	}
	return tw.Flush()
}

func newCaptainReportCommand() *cobra.Command {
	var days int
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "report",
		Short: "Engine telemetry from the captain event queue",
		Long: `Report captain-engine telemetry over a recent window: event volume,
acknowledgement latency, unprocessed backlog, and per-type counts.

Examples:
  spacetraders captain report --player-id 1
  spacetraders captain report --player-id 1 --days 14 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if playerID <= 0 {
				return fmt.Errorf("--player-id flag is required")
			}
			if days <= 0 {
				return fmt.Errorf("--days must be positive")
			}
			source, err := newReportEventSource()
			if err != nil {
				return err
			}
			tc, captainAlias, budgetTokens, alertThresholdPct := newReportTokenCollector()
			return runEngineReport(context.Background(), source, tc, captainAlias, playerID, days, time.Now(), budgetTokens, alertThresholdPct, jsonOut, os.Stdout)
		},
	}

	cmd.Flags().IntVar(&days, "days", 7, "Window size in days")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")

	return cmd
}
