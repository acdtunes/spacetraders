package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// The build-info collector must expose the running daemon's identity so a scrape
// can reveal a stale second instance (sp-wrh84): build_info pinned to 1 with the
// commit + start-time labels, and process_start_time_seconds as the unix start.
func TestDaemonBuildInfoCollectorExposesBuildIdentityAndStartTime(t *testing.T) {
	InitRegistry()

	c := NewDaemonBuildInfoCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	started := time.Unix(1_700_000_000, 0)
	c.Record("abc123def456", started)

	// process_start_time_seconds equals the unix start time.
	if got := testutil.ToFloat64(c.startTime); got != float64(started.Unix()) {
		t.Fatalf("process_start_time_seconds = %v, want %v", got, float64(started.Unix()))
	}

	// build_info carries the commit + started_at labels at value 1. WithLabelValues
	// reads the exact series Record wrote; a wrong commit/time formatting reads 0.
	startedAt := started.UTC().Format(time.RFC3339)
	if got := testutil.ToFloat64(c.buildInfo.WithLabelValues("abc123def456", startedAt)); got != 1 {
		t.Fatalf("build_info{commit=abc123def456, started_at=%s} = %v, want 1", startedAt, got)
	}

	// Exactly one build_info series exists (Record did not leak duplicates).
	if n := testutil.CollectAndCount(c.buildInfo); n != 1 {
		t.Fatalf("build_info series count = %d, want 1", n)
	}
}
