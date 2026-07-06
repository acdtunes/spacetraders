package cli

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

func processedAt(t time.Time) *time.Time { return &t }

func sampleReportEvents(now time.Time) []*captain.Event {
	day := 24 * time.Hour
	return []*captain.Event{
		{ID: 1, Type: captain.EventIncomeStalled, PlayerID: 1, CreatedAt: now.Add(-6 * day), ProcessedAt: processedAt(now.Add(-6*day + 10*time.Second))},
		{ID: 2, Type: captain.EventIncomeStalled, PlayerID: 1, CreatedAt: now.Add(-5 * day), ProcessedAt: processedAt(now.Add(-5*day + 30*time.Second))},
		{ID: 3, Type: captain.EventStreamDown, PlayerID: 1, CreatedAt: now.Add(-4 * day), ProcessedAt: processedAt(now.Add(-4*day + 20*time.Second))},
		{ID: 4, Type: captain.EventShipIdle, PlayerID: 1, CreatedAt: now.Add(-2 * day)},
		{ID: 5, Type: captain.EventShipIdle, PlayerID: 1, CreatedAt: now.Add(-3 * day)},
		{ID: 6, Type: captain.EventShipIdle, PlayerID: 1, CreatedAt: now.Add(-10 * day), ProcessedAt: processedAt(now.Add(-10*day + 999*time.Second))},
	}
}

func TestEngineReportComputesLatencyBacklogAndTypeCounts(t *testing.T) {
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	report := computeEngineReport(sampleReportEvents(now), 1, 7, now)

	require.Equal(t, 5, report.TotalEvents)
	require.InDelta(t, 5.0/7.0, report.EventsPerDay, 0.001)
	require.Equal(t, 20.0, report.AckLatencyP50Sec)
	require.Equal(t, 30.0, report.AckLatencyMaxSec)
	require.Equal(t, 2, report.BacklogCount)
	require.Equal(t, 3*24*3600.0, report.BacklogOldestAgeSec)
	require.Equal(t, 2, report.PerType["income.stalled"])
	require.Equal(t, 1, report.PerType["stream.down"])
	require.Equal(t, 2, report.PerType["ship.idle"])
}

func TestEngineReportJSONShapeIsPinned(t *testing.T) {
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	report := computeEngineReport(sampleReportEvents(now), 1, 7, now)

	raw, err := json.Marshal(report)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))

	for _, key := range []string{
		"player_id", "window_days", "total_events", "events_per_day",
		"ack_latency_p50_sec", "ack_latency_max_sec", "backlog_count",
		"backlog_oldest_age_sec", "per_type",
	} {
		require.Contains(t, decoded, key, "missing json key %q", key)
	}
	require.Equal(t, float64(1), decoded["player_id"])
	require.Equal(t, float64(7), decoded["window_days"])
}
