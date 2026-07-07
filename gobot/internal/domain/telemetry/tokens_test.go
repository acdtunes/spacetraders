package telemetry

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A synthetic claude-code transcript exercising every case the parser must
// handle: two JSONL lines that repeat the SAME assistant message.id (claude
// streams a message across lines, each repeating cumulative usage — summing raw
// lines would double-count), a genuine inbound prompt turn, a tool_result user
// turn that must NOT count as a wake, a non-message meta line, and a line older
// than the window that must be excluded.
func transcriptFixture() string {
	return strings.Join([]string{
		// meta line — ignored entirely.
		`{"type":"agent-name","name":"captain"}`,
		// out-of-window assistant usage (before since) — excluded.
		`{"type":"assistant","timestamp":"2026-07-01T00:00:00Z","message":{"id":"msg_old","usage":{"input_tokens":9999,"output_tokens":9999,"cache_creation_input_tokens":9999,"cache_read_input_tokens":9999}}}`,
		// genuine inbound prompt (string content) — one wake/turn.
		`{"type":"user","timestamp":"2026-07-07T10:00:00Z","message":{"content":"wake: 2 events + heartbeat — check mail"}}`,
		// assistant message msg_a, streamed across two lines with identical usage.
		`{"type":"assistant","timestamp":"2026-07-07T10:00:01Z","message":{"id":"msg_a","usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":20,"cache_read_input_tokens":1000}}}`,
		`{"type":"assistant","timestamp":"2026-07-07T10:00:02Z","message":{"id":"msg_a","usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":20,"cache_read_input_tokens":1000}}}`,
		// tool_result user turn — NOT a wake.
		`{"type":"user","timestamp":"2026-07-07T10:00:03Z","message":{"content":[{"type":"tool_result","content":"ok"}]}}`,
		// a second distinct assistant message msg_b.
		`{"type":"assistant","timestamp":"2026-07-07T10:00:04Z","message":{"id":"msg_b","usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":0,"cache_read_input_tokens":200}}}`,
		// a blank line — tolerated.
		``,
	}, "\n")
}

func TestParseTranscriptDeduplicatesUsageByMessageID(t *testing.T) {
	since := time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC)
	stats, err := ParseTranscript(strings.NewReader(transcriptFixture()), since)
	require.NoError(t, err)

	// msg_a counted ONCE (not twice) + msg_b; msg_old excluded by window.
	require.Equal(t, int64(110), stats.Usage.Input)
	require.Equal(t, int64(55), stats.Usage.Output)
	require.Equal(t, int64(20), stats.Usage.CacheCreation)
	require.Equal(t, int64(1200), stats.Usage.CacheRead)
	require.Equal(t, int64(110+55+20+1200), stats.Usage.Total())
}

func TestParseTranscriptCountsOnlyGenuinePromptTurns(t *testing.T) {
	since := time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC)
	stats, err := ParseTranscript(strings.NewReader(transcriptFixture()), since)
	require.NoError(t, err)

	// Exactly one genuine inbound prompt; the tool_result user turn is excluded.
	require.Equal(t, 1, stats.Turns)
}

func TestParseTranscriptTracksActivityWindow(t *testing.T) {
	since := time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC)
	stats, err := ParseTranscript(strings.NewReader(transcriptFixture()), since)
	require.NoError(t, err)

	require.Equal(t, time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC), stats.FirstActivity)
	require.Equal(t, time.Date(2026, 7, 7, 10, 0, 4, 0, time.UTC), stats.LastActivity)
}

func TestParseTranscriptZeroSinceIncludesEverything(t *testing.T) {
	stats, err := ParseTranscript(strings.NewReader(transcriptFixture()), time.Time{})
	require.NoError(t, err)

	// With no window, the old message is included too: msg_old + msg_a + msg_b.
	require.Equal(t, int64(9999+100+10), stats.Usage.Input)
}

func TestComputeReportDerivesPerDayAndPerWake(t *testing.T) {
	sessions := []SessionUsage{
		{
			Alias:      "captain",
			SessionKey: "k1",
			Usage:      Usage{Input: 1000, Output: 2000, CacheCreation: 500, CacheRead: 6500}, // total 10000
			Turns:      5,
		},
		{
			Alias:      "shipwright",
			SessionKey: "k2",
			Usage:      Usage{Input: 100, Output: 200, CacheCreation: 0, CacheRead: 9700}, // total 10000
			Turns:      2,
		},
	}
	report := ComputeReport(sessions, "captain", 4)

	// Fleet total = 20000 over 4 days -> 5000/day.
	require.Equal(t, int64(20000), report.TotalTokens)
	require.InDelta(t, 5000.0, report.TokensPerDay, 0.001)
	// Per-wake = captain total / captain turns = 10000/5 = 2000.
	require.InDelta(t, 2000.0, report.TokensPerWake, 0.001)
	require.Equal(t, 2, len(report.Sessions))
	// Sessions are sorted by total tokens descending (tie broken by alias).
	require.Equal(t, "captain", report.Sessions[0].Alias)
}

func TestComputeReportHandlesMissingCaptainAndZeroDays(t *testing.T) {
	sessions := []SessionUsage{
		{Alias: "shipwright", SessionKey: "k2", Usage: Usage{Output: 300}, Turns: 0},
	}
	report := ComputeReport(sessions, "captain", 0)

	require.Equal(t, int64(300), report.TotalTokens)
	require.Equal(t, 0.0, report.TokensPerDay)  // days <= 0 -> no rate, not a div-by-zero
	require.Equal(t, 0.0, report.TokensPerWake) // no captain session -> 0
}

func TestUsagePerTurnGuardsZeroTurns(t *testing.T) {
	s := SessionUsage{Usage: Usage{Output: 100}, Turns: 0}
	require.Equal(t, 0.0, s.TokensPerTurn())
	s.Turns = 4
	require.InDelta(t, 25.0, s.TokensPerTurn(), 0.001)
}
