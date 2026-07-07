package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	domain "github.com/andrescamacho/spacetraders-go/internal/domain/telemetry"
)

type fakeTokenCollector struct {
	sessions []domain.SessionUsage
	err      error
	gotSince time.Time
}

func (f *fakeTokenCollector) Collect(ctx context.Context, since time.Time) ([]domain.SessionUsage, error) {
	f.gotSince = since
	return f.sessions, f.err
}

func sampleTokenSessions() []domain.SessionUsage {
	return []domain.SessionUsage{
		{Alias: "captain", SessionKey: "c1", Usage: domain.Usage{Input: 1000, Output: 2000, CacheCreation: 500, CacheRead: 6500}, Turns: 5},
		{Alias: "shipwright", SessionKey: "s1", Usage: domain.Usage{Input: 100, Output: 200, CacheCreation: 0, CacheRead: 9700}, Turns: 2},
	}
}

func TestRunTokenReportJSONShapeIsPinned(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	fc := &fakeTokenCollector{sessions: sampleTokenSessions()}
	var buf bytes.Buffer

	err := runTokenReport(context.Background(), fc, "captain", 4, now, true, &buf)
	require.NoError(t, err)

	// Collector was queried over the requested window.
	require.Equal(t, now.AddDate(0, 0, -4), fc.gotSince)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &decoded))
	for _, key := range []string{"window_days", "total_tokens", "tokens_per_day", "tokens_per_wake", "captain_alias", "sessions"} {
		require.Contains(t, decoded, key, "missing json key %q", key)
	}
	require.Equal(t, float64(20000), decoded["total_tokens"])
	require.Equal(t, float64(5000), decoded["tokens_per_day"])
	require.Equal(t, float64(2000), decoded["tokens_per_wake"])
}

func TestRunTokenReportHumanRenderShowsHeadlineAndSessions(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	fc := &fakeTokenCollector{sessions: sampleTokenSessions()}
	var buf bytes.Buffer

	err := runTokenReport(context.Background(), fc, "captain", 4, now, false, &buf)
	require.NoError(t, err)

	out := buf.String()
	require.Contains(t, out, "Tokens/day")
	require.Contains(t, out, "Tokens/wake")
	require.Contains(t, out, "captain")
	require.Contains(t, out, "shipwright")
}

func TestRunTokenReportEmptyIsNotAnError(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	fc := &fakeTokenCollector{sessions: nil}
	var buf bytes.Buffer

	err := runTokenReport(context.Background(), fc, "captain", 7, now, false, &buf)
	require.NoError(t, err)
	require.Contains(t, buf.String(), "No token telemetry")
}
