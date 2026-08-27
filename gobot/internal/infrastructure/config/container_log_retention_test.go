package config

import (
	"testing"
	"time"
)

// The two windows are the whole policy, so the numbers are pinned. Anyone changing them has to
// change this line and argue with the reasoning on the constants.
func TestContainerLogRetentionDefaults_ShortForChatterLongForProblems(t *testing.T) {
	if DefaultContainerLogRetention != 48*time.Hour {
		t.Errorf("transient window = %s, want 48h: every PRODUCTION reader of container_logs needs "+
			"minutes (the 12-min liveness watchdog) to hours (container logs --tail 20), and 48h is what "+
			"lets an overnight incident still be read the next morning", DefaultContainerLogRetention)
	}
	if DefaultContainerLogProblemRetention != 14*24*time.Hour {
		t.Errorf("problem window = %s, want 14d: it deliberately equals the containers table's own "+
			"retention window, so an ERROR trail never outlives — or predeceases — the container row it "+
			"explains", DefaultContainerLogProblemRetention)
	}
	if DefaultContainerLogProblemRetention <= DefaultContainerLogRetention {
		t.Fatal("the problem window must be the LONGER of the two; the sweep's stop condition assumes it")
	}
}

func TestContainerLogRetentionConfig_ResolvesEveryKnobWithDefaults(t *testing.T) {
	tests := []struct {
		name        string
		cfg         ContainerLogRetentionConfig
		wantWindow  time.Duration
		wantProblem time.Duration
		wantBatch   int
		wantMax     int
	}{
		{
			"absent -> defaults",
			ContainerLogRetentionConfig{},
			DefaultContainerLogRetention, DefaultContainerLogProblemRetention,
			DefaultContainerLogSweepBatch, DefaultContainerLogSweepMaxBatches,
		},
		{
			"explicit values",
			ContainerLogRetentionConfig{WindowHours: 24, ProblemWindowHours: 168, BatchSize: 500, MaxBatches: 7},
			24 * time.Hour, 168 * time.Hour, 500, 7,
		},
		{
			"non-positive knobs fall back rather than disabling the sweep by accident",
			ContainerLogRetentionConfig{WindowHours: -1, ProblemWindowHours: 0, BatchSize: -5, MaxBatches: 0},
			DefaultContainerLogRetention, DefaultContainerLogProblemRetention,
			DefaultContainerLogSweepBatch, DefaultContainerLogSweepMaxBatches,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.ResolvedWindow(); got != tc.wantWindow {
				t.Errorf("ResolvedWindow() = %s, want %s", got, tc.wantWindow)
			}
			if got := tc.cfg.ResolvedProblemWindow(); got != tc.wantProblem {
				t.Errorf("ResolvedProblemWindow() = %s, want %s", got, tc.wantProblem)
			}
			if got := tc.cfg.ResolvedBatchSize(); got != tc.wantBatch {
				t.Errorf("ResolvedBatchSize() = %d, want %d", got, tc.wantBatch)
			}
			if got := tc.cfg.ResolvedMaxBatches(); got != tc.wantMax {
				t.Errorf("ResolvedMaxBatches() = %d, want %d", got, tc.wantMax)
			}
		})
	}
}

// RULINGS #22: the sweep is ON with no config present. The kill switch exists (#5) but it must
// take an explicit `disabled: true` to reach — an absent section can never leave the table
// unbounded again.
func TestContainerLogRetentionConfig_ShipsOnWithNoConfigPresent(t *testing.T) {
	if (ContainerLogRetentionConfig{}).Enabled() != true {
		t.Fatal("an absent [container_log_retention] section must leave the sweep ARMED — a dormant " +
			"retention policy is how the table reached 3.9M rows in the first place")
	}
	if (ContainerLogRetentionConfig{Disabled: true}).Enabled() != false {
		t.Fatal("disabled: true must actually disable the sweep")
	}
}
