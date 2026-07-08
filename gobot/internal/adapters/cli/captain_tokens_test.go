package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	domain "github.com/andrescamacho/spacetraders-go/internal/domain/telemetry"
)

type fakeTokenCollector struct {
	sessions []domain.SessionUsage
	err      error

	// sinceSpawn/sinceSpawnErr optionally override the response when Collect is
	// called with a zero `since` — the tokens-since-spawn unbounded query
	// (sp-0zx9). sinceSpawnSet distinguishes "not configured" (fall back to
	// sessions/err, exactly like any other call) from "deliberately set to
	// empty/erroring", so existing tests that don't know about since-spawn are
	// completely unaffected.
	sinceSpawn    []domain.SessionUsage
	sinceSpawnErr error
	sinceSpawnSet bool

	sinceCalls []time.Time
}

func (f *fakeTokenCollector) Collect(ctx context.Context, since time.Time) ([]domain.SessionUsage, error) {
	f.sinceCalls = append(f.sinceCalls, since)
	if since.IsZero() && f.sinceSpawnSet {
		return f.sinceSpawn, f.sinceSpawnErr
	}
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

	err := runTokenReport(context.Background(), fc, "captain", 4, now, 0, 0, true, &buf)
	require.NoError(t, err)

	// Collector was queried over the requested window (first call).
	require.Equal(t, now.AddDate(0, 0, -4), fc.sinceCalls[0])

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

	err := runTokenReport(context.Background(), fc, "captain", 4, now, 0, 0, false, &buf)
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

	err := runTokenReport(context.Background(), fc, "captain", 7, now, 0, 0, false, &buf)
	require.NoError(t, err)
	require.Contains(t, buf.String(), "No token telemetry")
}

// TestRunTokenReportIncludesTokensSinceSpawnPerSession is the core sp-0zx9(b)
// case: each session row carries its lifetime (since-spawn) token total,
// joined by SessionKey, from a SEPARATE unbounded collect call — not an echo
// of the windowed total. The since-spawn sample data is deliberately
// different from the windowed sample data so a bug that collapsed the two
// calls into one would be caught.
func TestRunTokenReportIncludesTokensSinceSpawnPerSession(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	fc := &fakeTokenCollector{
		sessions: sampleTokenSessions(),
		sinceSpawn: []domain.SessionUsage{
			{Alias: "captain", SessionKey: "c1", Usage: domain.Usage{Input: 5000, Output: 8000, CacheCreation: 2000, CacheRead: 30000}, Turns: 20},
			{Alias: "shipwright", SessionKey: "s1", Usage: domain.Usage{Input: 1000, Output: 2000, CacheCreation: 500, CacheRead: 20000}, Turns: 10},
		},
		sinceSpawnSet: true,
	}
	var buf bytes.Buffer

	err := runTokenReport(context.Background(), fc, "captain", 4, now, 0, 0, true, &buf)
	require.NoError(t, err)

	require.Len(t, fc.sinceCalls, 2, "the windowed collect plus the unbounded since-spawn collect")
	require.True(t, fc.sinceCalls[1].IsZero(), "the since-spawn collect requests the whole transcript (zero since)")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &decoded))
	sessions, ok := decoded["sessions"].([]any)
	require.True(t, ok)
	require.Len(t, sessions, 2)

	byAlias := map[string]map[string]any{}
	for _, s := range sessions {
		row := s.(map[string]any)
		byAlias[row["alias"].(string)] = row
	}
	require.Equal(t, float64(45000), byAlias["captain"]["tokens_since_spawn"], "captain lifetime total: 5000+8000+2000+30000")
	require.Equal(t, float64(23500), byAlias["shipwright"]["tokens_since_spawn"], "shipwright lifetime total: 1000+2000+500+20000")
}

// TestRunTokenReportSinceSpawnBestEffortOnCollectorError proves a failed
// since-spawn collect degrades gracefully (TokensSinceSpawn: 0 per row)
// rather than failing the windowed report the captain already relies on.
func TestRunTokenReportSinceSpawnBestEffortOnCollectorError(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	fc := &fakeTokenCollector{
		sessions:      sampleTokenSessions(),
		sinceSpawnErr: errors.New("gc unavailable"),
		sinceSpawnSet: true,
	}
	var buf bytes.Buffer

	err := runTokenReport(context.Background(), fc, "captain", 4, now, 0, 0, true, &buf)
	require.NoError(t, err, "a since-spawn collection failure must not fail the windowed report")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &decoded))
	sessions, ok := decoded["sessions"].([]any)
	require.True(t, ok)
	require.Len(t, sessions, 2, "windowed sessions still render")
	for _, s := range sessions {
		row := s.(map[string]any)
		require.Equal(t, float64(0), row["tokens_since_spawn"], "since-spawn defaults to 0 on collector failure")
	}
}

// TestRunTokenReportQuotaBlockPresentWhenBudgetConfigured is the core sp-1vkr
// case: a configured weekly_token_budget produces a quota block comparing
// this window's total against it.
func TestRunTokenReportQuotaBlockPresentWhenBudgetConfigured(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	fc := &fakeTokenCollector{sessions: sampleTokenSessions()}
	var buf bytes.Buffer

	err := runTokenReport(context.Background(), fc, "captain", 4, now, 100000, 80, true, &buf)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &decoded))
	quota, ok := decoded["quota"].(map[string]any)
	require.True(t, ok, "quota block must be present when a budget is configured")
	require.Equal(t, float64(100000), quota["budget_tokens"])
	require.Equal(t, float64(20000), quota["used_tokens"])
	require.Equal(t, float64(20), quota["used_pct"])
	require.Equal(t, float64(80), quota["alert_threshold_pct"])
	require.Equal(t, false, quota["alert"])
}

// TestRunTokenReportQuotaBlockOmittedWhenBudgetUnconfigured proves the
// feature is fully inert (not just zero-valued) when the operator has not
// set weekly_token_budget — omitted from JSON, not present-as-zero.
func TestRunTokenReportQuotaBlockOmittedWhenBudgetUnconfigured(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	fc := &fakeTokenCollector{sessions: sampleTokenSessions()}
	var buf bytes.Buffer

	err := runTokenReport(context.Background(), fc, "captain", 4, now, 0, 0, true, &buf)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &decoded))
	require.NotContains(t, decoded, "quota", "quota block must be omitted when no budget is configured")
}

// TestRunTokenReportQuotaAlertFlagsWhenThresholdCrossed proves the human
// render surfaces a clearly grep-able alert line once usage crosses the
// configured threshold percent — the sp-1vkr "budget alerting" requirement.
func TestRunTokenReportQuotaAlertFlagsWhenThresholdCrossed(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	fc := &fakeTokenCollector{sessions: sampleTokenSessions()}
	var buf bytes.Buffer

	// Budget 20000, used 20000 -> 100%, well past an 80% threshold.
	err := runTokenReport(context.Background(), fc, "captain", 4, now, 20000, 80, false, &buf)
	require.NoError(t, err)

	out := buf.String()
	require.Contains(t, out, "Quota alert")
	require.Contains(t, out, "THRESHOLD CROSSED")
}
