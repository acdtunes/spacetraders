package cli

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"time"

	"github.com/spf13/cobra"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// The A→B graduation gate (spec sp-1ek0): a hull graduates from supervised one-shot
// tours to an autonomous circuit only after 10 completed tours with (i) zero guard
// violations, (ii) realized $/hr ≥ 1.5× the trailing single-lane $/hr, and (iii)
// median plan-vs-realized price error ≤ ±15% (the metric that proves the model, not
// just profit). `tour report` measures all three from the telemetry + ledger.
const (
	tourGateMinTours    = 10
	tourGateMinRatio    = 1.5
	tourGateMaxErrorPct = 15.0
)

// TourGateMetrics holds the three computed graduation-gate metrics plus the verdict.
type TourGateMetrics struct {
	ToursCompleted           int
	GuardViolations          int
	TourCreditsPerHour       float64
	SingleLaneCreditsPerHour float64
	RatioAvailable           bool
	Ratio                    float64
	MedianPriceErrorPct      float64
	MedianAvailable          bool
	Pass                     bool
}

// tourReportSource is the report's data dependency, split out so the compute/render
// is unit-testable with a fake (the engine-report idiom).
type tourReportSource interface {
	// TourTelemetry returns the player's per-leg planned-vs-realized rows in the window.
	// Used ONLY for the tour COUNT and the plan-vs-realized price-error median — the
	// two metrics that are inherently telemetry (planned vs realized unit price). The
	// tour $/hr itself is NOT netted from these rows (see TourCreditsPerHour).
	TourTelemetry(ctx context.Context, playerID int, since time.Time) ([]trading.TourLegTelemetry, error)
	// FailedTourRunCount is the guard-violation count: tour_run containers that
	// terminalized FAILED (a stranded-cargo veto or an operational failure).
	FailedTourRunCount(ctx context.Context, playerID int, since time.Time) (int, error)
	// TourCreditsPerHour is the tour realized $/hr from the TRANSACTIONS-CASH ledger
	// (operation_type="tour"), not telemetry netting (sp-461l, epic sp-g9td). sp-rd21
	// proved telemetry netting read ~2x inflated — it dropped ~1/3 of buy legs while
	// their sells stayed logged, so net = sells − (partial buys) over-counted. The
	// transactions ledger records EVERY cargo trade and reconciles to the treasury, so
	// this is the true tour $/hr the graduation ratio judges. ok=false (fail-closed) on
	// an empty tour window — the ratio is then n/a and the gate cannot pass.
	TourCreditsPerHour(ctx context.Context, playerID int, since time.Time) (float64, bool, error)
	// TradeCreditsPerHour is the trailing single-lane baseline (proxy): net trade
	// credits over the window ÷ window hours, filtering operation_type <> 'tour' so a
	// tour is never measured against its own trades (tour writes ARE stamped
	// operation_type="tour", sp-lgnh). ok=false when there is no trade activity to form
	// a baseline. It omits REFUEL (a small cost) the tour side includes, which leaves
	// the baseline marginally HIGHER ⇒ the ratio marginally LOWER ⇒ the gate marginally
	// TIGHTER (never loosened — RULINGS #4).
	TradeCreditsPerHour(ctx context.Context, playerID int, since time.Time) (float64, bool, error)
}

// computeTourGateMetrics derives the three gate metrics from the telemetry rows, the
// failed-tour count, the transactions-cash tour $/hr, and the single-lane baseline.
// sp-461l: the tour $/hr is the TRANSACTIONS-CASH tour rate (tourCPH), NOT telemetry
// netting — sp-rd21 proved telemetry netting read ~2x inflated (dropped buy legs), so a
// graduation ratio built on it over-stated the multiple. The telemetry rows are still the
// source for the two metrics that are inherently telemetry: the completed-tour COUNT (one
// distinct tour_id) and the plan-vs-realized price-error median. The ratio needs BOTH the
// cash tour rate and the baseline to be readable; an unreadable tour rate fails the ratio
// closed (n/a) so the gate cannot pass on a fabricated number.
func computeTourGateMetrics(rows []trading.TourLegTelemetry, failedTours int, tourCPH float64, tourCPHAvailable bool, singleLaneCPH float64, singleLaneAvailable bool) TourGateMetrics {
	tourIDs := map[string]bool{}
	var errs []float64

	for _, r := range rows {
		tourIDs[r.TourID] = true

		// Price error only over executed trades with a realized price (telemetry-native).
		if r.RealizedUnits > 0 && r.PlannedUnitPrice > 0 && r.RealizedUnitPrice > 0 {
			errs = append(errs, math.Abs(float64(r.RealizedUnitPrice-r.PlannedUnitPrice))/float64(r.PlannedUnitPrice)*100)
		}
	}

	m := TourGateMetrics{
		ToursCompleted:           len(tourIDs),
		GuardViolations:          failedTours,
		SingleLaneCreditsPerHour: singleLaneCPH,
	}
	if tourCPHAvailable {
		m.TourCreditsPerHour = tourCPH
	}
	if tourCPHAvailable && singleLaneAvailable && singleLaneCPH > 0 {
		m.RatioAvailable = true
		m.Ratio = tourCPH / singleLaneCPH
	}
	if len(errs) > 0 {
		m.MedianAvailable = true
		m.MedianPriceErrorPct = medianFloat(errs)
	}

	m.Pass = m.ToursCompleted >= tourGateMinTours &&
		m.GuardViolations == 0 &&
		m.RatioAvailable && m.Ratio >= tourGateMinRatio &&
		m.MedianAvailable && m.MedianPriceErrorPct <= tourGateMaxErrorPct
	return m
}

func medianFloat(vs []float64) float64 {
	sorted := make([]float64, len(vs))
	copy(sorted, vs)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func renderTourReport(m TourGateMetrics, w io.Writer) {
	fmt.Fprintln(w, "Tour graduation report (sp-1ek0 A→B gate)")
	fmt.Fprintf(w, "  1. Completed tours: %d   (guard violations: %d)\n", m.ToursCompleted, m.GuardViolations)
	if m.RatioAvailable {
		fmt.Fprintf(w, "  2. Tour $/hr: %.0f  vs single-lane $/hr: %.0f   →  %.2fx\n", m.TourCreditsPerHour, m.SingleLaneCreditsPerHour, m.Ratio)
	} else {
		fmt.Fprintf(w, "  2. Tour $/hr: %.0f   (single-lane baseline unavailable → ratio n/a)\n", m.TourCreditsPerHour)
	}
	if m.MedianAvailable {
		fmt.Fprintf(w, "  3. Median plan-vs-realized price error: %.1f%%\n", m.MedianPriceErrorPct)
	} else {
		fmt.Fprintln(w, "  3. Median plan-vs-realized price error: n/a (no executed trades)")
	}
	verdict := "FAIL"
	if m.Pass {
		verdict = "PASS"
	}
	fmt.Fprintf(w, "GATE: %s (need: %d tours, >=%.1fx, <=%d%%)\n", verdict, tourGateMinTours, tourGateMinRatio, int(tourGateMaxErrorPct))
}

// runTourReport is the testable core: fetch → compute → render.
func runTourReport(ctx context.Context, source tourReportSource, playerID int, since time.Time, w io.Writer) error {
	rows, err := source.TourTelemetry(ctx, playerID, since)
	if err != nil {
		return fmt.Errorf("read tour telemetry: %w", err)
	}
	failed, err := source.FailedTourRunCount(ctx, playerID, since)
	if err != nil {
		return fmt.Errorf("count failed tours: %w", err)
	}
	tourCPH, tourOK, err := source.TourCreditsPerHour(ctx, playerID, since)
	if err != nil {
		return fmt.Errorf("read tour cash rate: %w", err)
	}
	baseline, ok, err := source.TradeCreditsPerHour(ctx, playerID, since)
	if err != nil {
		return fmt.Errorf("read single-lane baseline: %w", err)
	}
	renderTourReport(computeTourGateMetrics(rows, failed, tourCPH, tourOK, baseline, ok), w)
	return nil
}

type gormTourReportSource struct {
	db  *gorm.DB
	now time.Time
}

func (s *gormTourReportSource) TourTelemetry(ctx context.Context, playerID int, since time.Time) ([]trading.TourLegTelemetry, error) {
	return persistence.NewTourTelemetryRepository(s.db).ListByPlayer(ctx, playerID, since)
}

func (s *gormTourReportSource) FailedTourRunCount(ctx context.Context, playerID int, since time.Time) (int, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&persistence.ContainerModel{}).
		Where("player_id = ? AND command_type = ? AND status = ? AND started_at >= ?", playerID, "tour_run", "FAILED", since).
		Count(&n).Error
	return int(n), err
}

// TourCreditsPerHour is the transactions-cash tour realized $/hr over [since, now) —
// SELL_CARGO(+) − PURCHASE_CARGO(−) − REFUEL(−) scoped to operation_type="tour", divided
// by the window's wall-clock hours (sp-461l). It defers to the canonical cash-rate reader
// (GormTransactionRepository.RealizedCashRate) so this and every other cash-true consumer
// share one window basis. Readable=false on an empty tour window → the ratio fails closed.
func (s *gormTourReportSource) TourCreditsPerHour(ctx context.Context, playerID int, since time.Time) (float64, bool, error) {
	rate, err := persistence.NewGormTransactionRepository(s.db).RealizedCashRate(ctx, playerID, since, s.now, "tour")
	if err != nil {
		return 0, false, err
	}
	return rate.CreditsPerHour, rate.Readable, nil
}

func (s *gormTourReportSource) TradeCreditsPerHour(ctx context.Context, playerID int, since time.Time) (float64, bool, error) {
	var sum int64
	err := s.db.WithContext(ctx).Model(&persistence.TransactionModel{}).
		Where("player_id = ? AND transaction_type IN ? AND timestamp >= ? AND operation_type <> ?",
			playerID, []string{"SELL_CARGO", "PURCHASE_CARGO"}, since, "tour").
		Select("COALESCE(SUM(amount), 0)").Scan(&sum).Error
	if err != nil {
		return 0, false, err
	}
	hours := s.now.Sub(since).Hours()
	if hours <= 0 || sum == 0 {
		return 0, false, nil
	}
	return float64(sum) / hours, true, nil
}

// NewTourCommand builds the `tour` command group and its `report` subcommand.
func NewTourCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tour",
		Short: "Multi-hop trade-tour tooling (sp-1ek0)",
		Long: `Tooling for the multi-hop trade-tour program (spec: sp-1ek0) — the graduation
path from single-lane trading to chained A→B→C tours that keep a hull's cargo
hold working across several legs.

Currently exposes the "report" subcommand, which computes the A→B graduation
gate — completed-tour count and guard violations, tour realized $/hr versus the
trailing single-lane rate, and median plan-vs-realized unit-price error — over
a trailing window.

Examples:
  spacetraders tour report --agent TORWIND
  spacetraders tour report --since 72h`,
	}
	cmd.AddCommand(newTourReportCommand())
	return cmd
}

func newTourReportCommand() *cobra.Command {
	var since time.Duration
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Report the three A→B graduation-gate metrics from tour telemetry + ledger",
		Long: `Compute the multi-hop trade-tour graduation gate (sp-1ek0) over a trailing window:
  1. completed tours and guard violations (FAILED tour_run containers);
  2. tour realized $/hr vs the trailing single-lane $/hr;
  3. median plan-vs-realized unit-price error %.
The gate passes at 10 tours, >=1.5x single-lane $/hr, and <=15% median price error.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}
			cfg, err := config.LoadConfig("")
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}
			db, err := database.NewConnection(&cfg.Database)
			if err != nil {
				return fmt.Errorf("failed to connect to database: %w", err)
			}
			now := time.Now()
			source := &gormTourReportSource{db: db, now: now}
			return runTourReport(cmd.Context(), source, playerIdent.PlayerID, now.Add(-since), os.Stdout)
		},
	}
	cmd.Flags().DurationVar(&since, "since", 168*time.Hour, "Trailing window to measure (default 168h = 7 days)")
	return cmd
}
