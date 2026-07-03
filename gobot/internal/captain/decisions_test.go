package captainsup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReadDecisionsSkipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "decisions.jsonl")
	lines := `{"id":"d-1","action":"buy hauler","expectation":"utilization +10%","review_after":"2026-07-01T00:00:00Z"}
not json at all
{"id":"d-2","action":"start arbitrage","expectation":"+40k in 3h","review_after":"2099-01-01T00:00:00Z"}
{"id":"d-3","action":"done thing","expectation":"x","review_after":"2026-07-01T00:00:00Z","outcome":"worked"}
`
	require.NoError(t, os.WriteFile(path, []byte(lines), 0o644))

	ds, err := ReadDecisions(path)
	require.NoError(t, err)
	require.Len(t, ds, 3)

	now := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	due := DueForReview(ds, now)
	require.Len(t, due, 1) // d-1: past review_after, no outcome. d-2 future. d-3 has outcome.
	require.Equal(t, "d-1", due[0].ID)
}

func TestReadDecisionsMissingFileIsEmpty(t *testing.T) {
	ds, err := ReadDecisions(filepath.Join(t.TempDir(), "nope.jsonl"))
	require.NoError(t, err)
	require.Empty(t, ds)
}

func TestDueForReviewHonorsAppendedClosures(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "decisions.jsonl")
	// Contract: closures are APPENDED lines reusing the id. The original
	// null-outcome line must not keep the decision due forever.
	lines := `{"id":"d-1","action":"open then closed","expectation":"x","review_after":"2020-01-01T00:00:00Z"}
{"id":"d-1","action":"close d-1","expectation":"x","review_after":"2020-01-01T00:00:00Z","outcome":"worked"}
{"id":"d-2","action":"still open","expectation":"x","review_after":"2020-01-01T00:00:00Z"}
`
	require.NoError(t, os.WriteFile(path, []byte(lines), 0o644))
	ds, err := ReadDecisions(path)
	require.NoError(t, err)

	due := DueForReview(ds, time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC))
	require.Len(t, due, 1, "closed d-1 re-listed: only d-2 is genuinely due")
	require.Equal(t, "d-2", due[0].ID)
}
