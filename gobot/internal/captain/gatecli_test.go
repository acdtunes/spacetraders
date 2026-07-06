package captainsup

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGateAndMergeRefusesMergeWhenGateFails(t *testing.T) {
	r := gateAndMergeWith(
		func(string, time.Duration) (bool, string) { return false, "boom" },
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
		func(string) (bool, error) { return false, nil },
		func(string, string, string) error { t.Fatal("must not merge when merge=false"); return nil },
		"repo", "wt", "b", "msg", time.Minute, false)
	require.True(t, r.GatePassed)
	require.False(t, r.Merged)
}
