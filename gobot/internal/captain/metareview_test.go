package captainsup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMetaReviewDueOncePerDay(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "state"), 0o755))
	ws := NewWorkspace(dir)
	now := time.Now()

	require.True(t, MetaReviewDue(ws, now), "missing marker file means due")
	require.NoError(t, MarkMetaReviewDone(ws, now))
	require.False(t, MetaReviewDue(ws, now.Add(2*time.Hour)))
	require.True(t, MetaReviewDue(ws, now.Add(25*time.Hour)))
}

func TestComposeMetaReviewGathersFrictionAndBacklog(t *testing.T) {
	db, playerID, _ := setupDB(t)
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "state"), 0o755))
	ws := NewWorkspace(dir)
	require.NoError(t, os.WriteFile(ws.StatePath("captain-log.md"), []byte(
		"## 2026-07-01\ndecided things\nfriction: no arbitrage scan command, chained 3 CLIs by hand\n"+
			"## 2026-07-02\nfriction: snapshot lacks cargo value estimates\nother line\n"), 0o644))
	require.NoError(t, os.WriteFile(ws.StatePath("improvement-backlog.md"),
		[]byte("# Backlog\n- P1: arbitrage scan command"), 0o644))
	require.NoError(t, os.WriteFile(ws.StatePath("lessons.md"), []byte("L1 — probes are cheap"), 0o644))

	prompt, err := ComposeMetaReview(context.Background(), db, ws, playerID, time.Now())
	require.NoError(t, err)

	require.Contains(t, prompt, "no arbitrage scan command")
	require.Contains(t, prompt, "snapshot lacks cargo value estimates")
	require.NotContains(t, prompt, "other line", "only friction: lines are extracted from the log")
	require.Contains(t, prompt, "P1: arbitrage scan command")
	require.Contains(t, prompt, "L1 — probes are cheap")
	require.Contains(t, prompt, "at most ONE proposal")
}
