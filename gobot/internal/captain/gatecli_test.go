package watchkeeper

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// cleanWorktree is the injected dirty-check stub for tests that exercise the
// post-gate chain: the worktree is committed/clean, so gating proceeds.
func cleanWorktree(string) (bool, string, error) { return false, "", nil }

func TestGateAndMergeRefusesMergeWhenGateFails(t *testing.T) {
	r := gateAndMergeWith(
		func(string, time.Duration) (bool, string) { return false, "boom" },
		cleanWorktree,
		func(string) (bool, error) { return false, nil },
		func(string, string, string) error { t.Fatal("must not merge"); return nil },
		"repo", "wt", "b", "msg", time.Minute, true)
	require.False(t, r.GatePassed)
	require.False(t, r.Merged)
	require.Equal(t, "boom", r.Log)
}

func TestGateAndMergeRefusesMergeOnStaleBase(t *testing.T) {
	r := gateAndMergeWith(
		func(string, time.Duration) (bool, string) { return true, "ok" },
		cleanWorktree,
		func(string) (bool, error) { return true, nil },
		func(string, string, string) error { t.Fatal("must not merge stale"); return nil },
		"repo", "wt", "b", "msg", time.Minute, true)
	require.True(t, r.GatePassed)
	require.True(t, r.Stale)
	require.False(t, r.Merged)
}

func TestGateAndMergeMergesWhenGatePassesAndFresh(t *testing.T) {
	var merged [][]string
	r := gateAndMergeWith(
		func(string, time.Duration) (bool, string) { return true, "ok" },
		cleanWorktree,
		func(string) (bool, error) { return false, nil },
		func(repo, branch, msg string) error { merged = append(merged, []string{repo, branch, msg}); return nil },
		"repo", "wt", "b", "msg", time.Minute, true)
	require.True(t, r.GatePassed)
	require.False(t, r.Stale)
	require.True(t, r.Merged)
	require.Equal(t, [][]string{{"repo", "b", "msg"}}, merged)
}

func TestGateAndMergeSkipsMergeWhenNotRequested(t *testing.T) {
	r := gateAndMergeWith(
		func(string, time.Duration) (bool, string) { return true, "ok" },
		cleanWorktree,
		func(string) (bool, error) { return false, nil },
		func(string, string, string) error { t.Fatal("must not merge when merge=false"); return nil },
		"repo", "wt", "b", "msg", time.Minute, false)
	require.True(t, r.GatePassed)
	require.False(t, r.Merged)
}

// sp-k0di check 1: a dirty worktree is refused BEFORE the gate runs — the gate
// merges commits, not files, so it must never build (let alone merge) a tree with
// uncommitted source changes. runGate and squashMerge must not be reached.
func TestGateAndMergeRefusesDirtyWorktree(t *testing.T) {
	r := gateAndMergeWith(
		func(string, time.Duration) (bool, string) { t.Fatal("must not gate a dirty worktree"); return false, "" },
		func(string) (bool, string, error) { return true, " M fix.go\n?? new.go", nil },
		func(string) (bool, error) { t.Fatal("must not reach staleness check"); return false, nil },
		func(string, string, string) error { t.Fatal("must not merge a dirty worktree"); return nil },
		"repo", "wt", "b", "msg", time.Minute, true)
	require.True(t, r.Dirty)
	require.False(t, r.GatePassed)
	require.False(t, r.Merged)
	require.Contains(t, r.Log, "uncommitted changes")
	require.Contains(t, r.Log, "fix.go")
}

// A dirty-check that itself errors is fail-closed: no gate, no merge, GatePassed
// stays false so the CLI exits non-zero rather than proceeding blind.
func TestGateAndMergeFailsClosedWhenDirtyCheckErrors(t *testing.T) {
	r := gateAndMergeWith(
		func(string, time.Duration) (bool, string) { t.Fatal("must not gate when dirtiness is unknown"); return false, "" },
		func(string) (bool, string, error) { return false, "", fmt.Errorf("git blew up") },
		func(string) (bool, error) { return false, nil },
		func(string, string, string) error { t.Fatal("must not merge"); return nil },
		"repo", "wt", "b", "msg", time.Minute, true)
	require.False(t, r.GatePassed)
	require.False(t, r.Merged)
	require.Contains(t, r.Log, "dirty-check failed")
}

// sp-k0di check 2/3: an errEmptyMerge from the squash surfaces on the result as
// EmptyMerge so the orchestrator sees "nothing merged" explicitly, and Merged
// stays false.
func TestGateAndMergeSurfacesEmptyMerge(t *testing.T) {
	r := gateAndMergeWith(
		func(string, time.Duration) (bool, string) { return true, "ok" },
		cleanWorktree,
		func(string) (bool, error) { return false, nil },
		func(string, string, string) error { return fmt.Errorf("%w: branch b has no commits ahead of main", errEmptyMerge) },
		"repo", "wt", "b", "msg", time.Minute, true)
	require.True(t, r.GatePassed)
	require.True(t, r.EmptyMerge)
	require.False(t, r.Merged)
}
