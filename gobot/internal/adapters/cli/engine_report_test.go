package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// fakeReportEventSource is a reportEventSource test double that records the
// playerID it was queried with, so tests can assert a resolved --agent flag
// (sp-yr3f) reached the source as a concrete numeric ID.
type fakeReportEventSource struct {
	events       []*captain.Event
	err          error
	lastPlayerID int
}

func (f *fakeReportEventSource) FindSince(ctx context.Context, playerID int, since time.Time) ([]*captain.Event, error) {
	f.lastPlayerID = playerID
	return f.events, f.err
}

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

// --- sp-yr3f: `captain report` honors global --agent ---

// TestCaptainReportResolvedHonorsAgentFlagWithoutPlayerID reproduces the
// verified repro pattern for "captain report --agent TORWIND" (previously
// "--player-id flag is required" even though --agent was set): with only
// --agent set, resolution must succeed and the source must be queried with
// the resolved numeric player ID.
func TestCaptainReportResolvedHonorsAgentFlagWithoutPlayerID(t *testing.T) {
	setPlayerFlags(t, 0, "TORWIND")
	repo := &fakePlayerRepo{bySymbol: map[string]*player.Player{
		"TORWIND": player.NewPlayer(shared.MustNewPlayerID(9), "TORWIND", "TOKEN-9"),
	}}
	source := &fakeReportEventSource{}
	var buf bytes.Buffer

	err := runEngineReportResolved(context.Background(), repo, source, nil, "", 7, time.Now(), 0, 0, true, &buf)

	require.NoError(t, err)
	require.Equal(t, 9, source.lastPlayerID)
}

// TestCaptainReportResolvedErrorsWhenNoPlayerIdentifiable confirms the
// helpful error remains when neither --player-id, --agent, nor a persisted
// default identifies a player.
func TestCaptainReportResolvedErrorsWhenNoPlayerIdentifiable(t *testing.T) {
	setPlayerFlags(t, 0, "")
	t.Setenv("HOME", t.TempDir())
	repo := &fakePlayerRepo{}
	source := &fakeReportEventSource{}
	var buf bytes.Buffer

	err := runEngineReportResolved(context.Background(), repo, source, nil, "", 7, time.Now(), 0, 0, true, &buf)

	require.Error(t, err)
}
