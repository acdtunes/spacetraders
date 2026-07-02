package captainsup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const sampleReport = `---
title: Refuel loop crashes on zero-fuel markets
status: new
kind: fix
---

## Failure signature
container command_type=refuel, error class=divide-by-zero
`

func TestScanReportsParsesFrontmatter(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "2026-07-02-refuel-crash.md"), []byte(sampleReport), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "no-frontmatter.md"), []byte("just prose"), 0o644))

	reports, err := ScanReports(dir)
	require.NoError(t, err)
	require.Len(t, reports, 2)

	byName := map[string]BugReport{}
	for _, r := range reports {
		byName[filepath.Base(r.Path)] = r
	}
	r := byName["2026-07-02-refuel-crash.md"]
	require.Equal(t, "new", r.Status)
	require.Equal(t, "fix", r.Kind)
	require.Equal(t, "Refuel loop crashes on zero-fuel markets", r.Title)
	require.Equal(t, "2026-07-02-refuel-crash", r.Slug)

	require.Equal(t, "new", byName["no-frontmatter.md"].Status, "missing frontmatter defaults to new")
	require.Equal(t, "fix", byName["no-frontmatter.md"].Kind)
}

func TestSetReportStatusRewritesInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r.md")
	require.NoError(t, os.WriteFile(path, []byte(sampleReport), 0o644))

	require.NoError(t, SetReportStatus(path, "in_progress"))

	reports, err := ScanReports(dir)
	require.NoError(t, err)
	require.Equal(t, "in_progress", reports[0].Status)
	data, _ := os.ReadFile(path)
	require.Contains(t, string(data), "## Failure signature", "body must be preserved")
}

func TestScanReportsMissingDirIsEmpty(t *testing.T) {
	reports, err := ScanReports(filepath.Join(t.TempDir(), "nope"))
	require.NoError(t, err)
	require.Empty(t, reports)
}
