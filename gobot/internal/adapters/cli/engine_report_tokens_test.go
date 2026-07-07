package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

type fakeReportSource struct{ events []*captain.Event }

func (f fakeReportSource) FindSince(ctx context.Context, playerID int, since time.Time) ([]*captain.Event, error) {
	return f.events, nil
}

func TestRunEngineReportEmbedsTokenBlockBestEffort(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	src := fakeReportSource{events: sampleReportEvents(now)}
	fc := &fakeTokenCollector{sessions: sampleTokenSessions()}
	var buf bytes.Buffer

	err := runEngineReport(context.Background(), src, fc, "captain", 1, 7, now, true, &buf)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &decoded))
	require.Contains(t, decoded, "total_events", "events report must still render")
	require.Contains(t, decoded, "token_usage")

	tu := decoded["token_usage"].(map[string]any)
	require.Equal(t, float64(20000), tu["total_tokens"])
	require.Equal(t, float64(2000), tu["tokens_per_wake"])
}

func TestRunEngineReportNilCollectorOmitsTokenBlock(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	src := fakeReportSource{events: sampleReportEvents(now)}
	var buf bytes.Buffer

	err := runEngineReport(context.Background(), src, nil, "captain", 1, 7, now, true, &buf)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &decoded))
	require.NotContains(t, decoded, "token_usage")
}

func TestRunEngineReportTokenCollectorErrorDoesNotFailReport(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	src := fakeReportSource{events: sampleReportEvents(now)}
	fc := &fakeTokenCollector{err: errors.New("gc unavailable")}
	var buf bytes.Buffer

	err := runEngineReport(context.Background(), src, fc, "captain", 1, 7, now, true, &buf)
	require.NoError(t, err, "a token-collection failure must not fail the events report")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &decoded))
	require.Contains(t, decoded, "total_events")
	require.NotContains(t, decoded, "token_usage")
}
