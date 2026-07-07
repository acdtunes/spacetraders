// Package telemetry aggregates per-session token/usage telemetry for the
// autonomous fleet. Token cost is unobservable in the engine (sp-593x): the
// watchkeeper wakes the captain and dispatches shipwright workflows as external
// `gc`-managed claude sessions, and the only durable record of what each wake
// costs is the claude-code transcript. This package parses those transcripts
// into per-session usage and derives the fleet-level rates (tokens/day) and the
// captain's per-wake cost (tokens/wake) that the surveyor ritual needs but has
// had no data source for.
//
// Everything here is pure: it reads an io.Reader and computes over in-memory
// values. Locating transcripts and shelling out to `gc` lives in the telemetry
// adapter; surfacing the numbers lives in the CLI. That split keeps the parsing
// and rate math unit-testable with fixtures and free of I/O.
package telemetry

import (
	"bufio"
	"encoding/json"
	"io"
	"sort"
	"time"
)

// Usage is the four-component token usage claude reports for one assistant
// message. They are kept separate (rather than pre-summed) because their prices
// differ by more than an order of magnitude — cache reads are far cheaper than
// fresh input — so a downstream cost model must be able to weight them. Total()
// is the raw all-in token count used for headline rates.
type Usage struct {
	Input         int64 `json:"input_tokens"`
	Output        int64 `json:"output_tokens"`
	CacheCreation int64 `json:"cache_creation_input_tokens"`
	CacheRead     int64 `json:"cache_read_input_tokens"`
}

// Total is the sum of all four components: every token the model processed.
func (u Usage) Total() int64 {
	return u.Input + u.Output + u.CacheCreation + u.CacheRead
}

// Add returns the component-wise sum of two usages.
func (u Usage) Add(o Usage) Usage {
	return Usage{
		Input:         u.Input + o.Input,
		Output:        u.Output + o.Output,
		CacheCreation: u.CacheCreation + o.CacheCreation,
		CacheRead:     u.CacheRead + o.CacheRead,
	}
}

// SessionUsage is one agent session's aggregated token usage over a window.
// Turns counts genuine inbound prompt turns — the wake proxy. Each watchkeeper
// wake (a mail notification or heartbeat nudge) is delivered to the captain
// session as exactly one inbound user turn, so for the captain session Turns is
// its wake count; tool_result turns are excluded.
type SessionUsage struct {
	Alias         string    `json:"alias"`
	SessionKey    string    `json:"session_key"`
	Usage         Usage     `json:"usage"`
	Turns         int       `json:"turns"`
	FirstActivity time.Time `json:"first_activity"`
	LastActivity  time.Time `json:"last_activity"`
}

// TokensPerTurn is this session's all-in tokens divided by its inbound prompt
// turns — the per-wake cost for the captain session. Zero turns yields 0 rather
// than a division by zero.
func (s SessionUsage) TokensPerTurn() float64 {
	if s.Turns <= 0 {
		return 0
	}
	return float64(s.Usage.Total()) / float64(s.Turns)
}

// TranscriptStats is the result of parsing one transcript over a window.
type TranscriptStats struct {
	Usage         Usage
	Turns         int
	FirstActivity time.Time
	LastActivity  time.Time
}

// transcriptLine is the minimal shape read from each JSONL record. Only
// assistant lines (for usage) and user lines (for turn counting) are relevant;
// every other type is a gc/claude meta record and is skipped.
type transcriptLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		ID      string          `json:"id"`
		Usage   *Usage          `json:"usage"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// ParseTranscript reads a claude-code transcript JSONL stream and aggregates
// token usage and inbound prompt turns whose timestamp is at or after `since`
// (a zero `since` includes the whole transcript).
//
// Usage is de-duplicated by assistant message.id: claude streams one assistant
// message across multiple JSONL lines, each repeating that message's cumulative
// usage, so summing every line triple-counts. Each unique message.id therefore
// contributes its usage exactly once (the last line seen for that id wins, which
// carries the final cumulative usage).
func ParseTranscript(r io.Reader, since time.Time) (TranscriptStats, error) {
	// usageByID holds one usage per assistant message.id (last write wins), so
	// the final sum counts each message once regardless of how many streamed
	// lines repeated it.
	usageByID := make(map[string]Usage)
	var stats TranscriptStats

	scanner := bufio.NewScanner(r)
	// Transcript lines can be large (a single assistant message with many tool
	// blocks); raise the token limit well above bufio's 64KB default.
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	for scanner.Scan() {
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		var line transcriptLine
		if err := json.Unmarshal(raw, &line); err != nil {
			// A single malformed line must not abort the whole aggregation; skip it.
			continue
		}
		if line.Type != "assistant" && line.Type != "user" {
			continue
		}

		ts, _ := time.Parse(time.RFC3339, line.Timestamp)
		// A line before the window (including one with an unparseable/zero
		// timestamp when `since` is set) is out of scope. With a zero `since`,
		// a zero ts is not Before(since) and so is included.
		if ts.Before(since) {
			continue
		}

		switch line.Type {
		case "assistant":
			if line.Message.Usage == nil {
				continue
			}
			usageByID[line.Message.ID] = *line.Message.Usage
			stats.observe(ts)
		case "user":
			if isToolResult(line.Message.Content) {
				continue
			}
			stats.Turns++
			stats.observe(ts)
		}
	}
	if err := scanner.Err(); err != nil {
		return TranscriptStats{}, err
	}

	for _, u := range usageByID {
		stats.Usage = stats.Usage.Add(u)
	}
	return stats, nil
}

// observe extends the first/last activity window to include ts.
func (s *TranscriptStats) observe(ts time.Time) {
	if ts.IsZero() {
		return
	}
	if s.FirstActivity.IsZero() || ts.Before(s.FirstActivity) {
		s.FirstActivity = ts
	}
	if ts.After(s.LastActivity) {
		s.LastActivity = ts
	}
}

// isToolResult reports whether a user message's content is a tool_result
// delivery (a synthetic turn carrying tool output) rather than a genuine inbound
// prompt. Content is either a JSON string (always a genuine prompt) or an array
// of blocks; if any block is a tool_result the turn is tool output, not a wake.
func isToolResult(content json.RawMessage) bool {
	if len(content) == 0 {
		return false
	}
	var blocks []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		// Not an array (e.g. a plain string prompt) -> a genuine inbound turn.
		return false
	}
	for _, b := range blocks {
		if b.Type == "tool_result" {
			return true
		}
	}
	return false
}

// Report is the fleet-level token telemetry surfaced by the read verb: the
// per-session breakdown plus the two rates the bead calls for — tokens/day
// (fleet burn) and tokens/wake (the captain's per-activation cost).
type Report struct {
	WindowDays    int            `json:"window_days"`
	TotalTokens   int64          `json:"total_tokens"`
	TokensPerDay  float64        `json:"tokens_per_day"`
	TokensPerWake float64        `json:"tokens_per_wake"`
	CaptainAlias  string         `json:"captain_alias"`
	Sessions      []SessionUsage `json:"sessions"`
}

// ComputeReport aggregates per-session usage into the fleet report. tokens/day
// is the fleet's all-in tokens over the window (0 when days <= 0, never a
// division by zero); tokens/wake is the captain session's tokens per inbound
// prompt turn (0 when there is no captain session or it has no turns yet).
// Sessions are returned sorted by total tokens descending so the biggest
// spenders lead, ties broken by alias for deterministic output.
func ComputeReport(sessions []SessionUsage, captainAlias string, windowDays int) Report {
	report := Report{
		WindowDays:   windowDays,
		CaptainAlias: captainAlias,
		Sessions:     append([]SessionUsage(nil), sessions...),
	}

	for _, s := range sessions {
		report.TotalTokens += s.Usage.Total()
		if s.Alias == captainAlias {
			report.TokensPerWake = s.TokensPerTurn()
		}
	}
	if windowDays > 0 {
		report.TokensPerDay = float64(report.TotalTokens) / float64(windowDays)
	}

	sort.SliceStable(report.Sessions, func(i, j int) bool {
		ti, tj := report.Sessions[i].Usage.Total(), report.Sessions[j].Usage.Total()
		if ti != tj {
			return ti > tj
		}
		return report.Sessions[i].Alias < report.Sessions[j].Alias
	})
	return report
}
