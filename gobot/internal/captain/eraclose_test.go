package captainsup

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func eraCloseFixtureExec(t *testing.T, calls *[][]string) Execer {
	t.Helper()
	beadsJSON := `[
		{"id":"sp-1","issue_type":"decision","labels":[],"status":"open","created_at":"2026-06-01T00:00:00Z"},
		{"id":"sp-2","issue_type":"decision","labels":["era:torwind"],"status":"closed","created_at":"2026-06-02T00:00:00Z"},
		{"id":"sp-3","issue_type":"consult","labels":[],"status":"open","created_at":"2026-06-15T00:00:00Z"},
		{"id":"sp-4","issue_type":"decision","labels":[],"status":"open","created_at":"2020-01-01T00:00:00Z"},
		{"id":"sp-5","issue_type":"feature","labels":["shipwright"],"status":"open","created_at":"2026-06-10T00:00:00Z"},
		{"id":"sp-s2q","issue_type":"design","labels":["strategy"],"status":"open","created_at":"2026-06-01T00:00:00Z"}
	]`
	memoriesJSON := `{
		"L1": "L1 [seed] — Probes are cheap: keep one probe per market.",
		"L47": "L47 [d-9] — phantom cache recurs after each contract; ship refresh is the first move. TORWIND-3 cached 44/80 IRON_ORE.",
		"L60": "D45 is the only ADVANCED_CIRCUITRY exporter."
	}`
	return func(_ context.Context, name string, args ...string) (string, error) {
		*calls = append(*calls, append([]string{name}, args...))
		if len(args) > 0 && args[0] == "list" {
			require.Contains(t, args, "--all", "sweep must read the full corpus, closed beads included")
			require.Contains(t, args, "0", "sweep must lift bd's default 50-row limit")
			return beadsJSON, nil
		}
		if len(args) > 0 && args[0] == "memories" {
			return memoriesJSON, nil
		}
		return "", nil
	}
}

func eraCloseWindow() (time.Time, time.Time) {
	start, _ := time.Parse("2006-01-02", "2026-06-01")
	end, _ := time.Parse("2006-01-02", "2026-06-30")
	return start, end
}

func TestEraCloseDryRunExecutesNoWrites(t *testing.T) {
	var calls [][]string
	b := &BeadsClient{BDBin: "bd", RigDir: "/rig", Exec: eraCloseFixtureExec(t, &calls)}
	start, end := eraCloseWindow()

	rep, err := EraClose(context.Background(), b, "torwind", "2026-07-05", start, end, "TORWIND", false)
	require.NoError(t, err)
	require.NotEmpty(t, rep.Commands, "dry-run must still plan commands")

	for _, c := range calls {
		joined := strings.Join(c, " ")
		require.NotContains(t, joined, "label")
		require.NotContains(t, joined, "close")
	}
}

func TestEraCloseApplyExecutesExactlyPlannedWrites(t *testing.T) {
	var calls [][]string
	b := &BeadsClient{BDBin: "bd", RigDir: "/rig", Exec: eraCloseFixtureExec(t, &calls)}
	start, end := eraCloseWindow()

	rep, err := EraClose(context.Background(), b, "torwind", "2026-07-05", start, end, "TORWIND", true)
	require.NoError(t, err)

	var writes [][]string
	for _, c := range calls {
		if len(c) > 1 && (c[1] == "label" || c[1] == "close") {
			writes = append(writes, c)
		}
	}
	require.Equal(t, len(rep.Commands), len(writes), "apply must execute exactly the planned write commands")

	for _, c := range calls {
		joined := strings.Join(c, " ")
		require.NotContains(t, joined, "forget")
		require.NotContains(t, joined, "remember")
	}
}

func TestEraCloseLabelsUnlabeledScopedBeadsInWindow(t *testing.T) {
	var calls [][]string
	b := &BeadsClient{BDBin: "bd", RigDir: "/rig", Exec: eraCloseFixtureExec(t, &calls)}
	start, end := eraCloseWindow()

	rep, err := EraClose(context.Background(), b, "torwind", "2026-07-05", start, end, "TORWIND", false)
	require.NoError(t, err)

	require.Equal(t, []string{"sp-1", "sp-3"}, rep.Labeled, "sp-2 already labeled, sp-4 out of window, sp-5 not scoped type")
	require.NotNil(t, findCmd(rep.Commands, "label", "add", "sp-1", "sp-3", "era:torwind"))
}

func TestEraCloseBulkClosesOpenScopedBeadsWithReason(t *testing.T) {
	var calls [][]string
	b := &BeadsClient{BDBin: "bd", RigDir: "/rig", Exec: eraCloseFixtureExec(t, &calls)}
	start, end := eraCloseWindow()

	rep, err := EraClose(context.Background(), b, "torwind", "2026-07-05", start, end, "TORWIND", false)
	require.NoError(t, err)

	require.Equal(t, []string{"sp-1", "sp-3"}, rep.Closed)
	require.NotNil(t, findCmd(rep.Commands, "close", "sp-1", "sp-3", "--reason", "era torwind ended (universe reset 2026-07-05)"))
}

func TestEraCloseDemotesOpenStrategyBead(t *testing.T) {
	var calls [][]string
	b := &BeadsClient{BDBin: "bd", RigDir: "/rig", Exec: eraCloseFixtureExec(t, &calls)}
	start, end := eraCloseWindow()

	rep, err := EraClose(context.Background(), b, "torwind", "2026-07-05", start, end, "TORWIND", false)
	require.NoError(t, err)

	require.Equal(t, "sp-s2q", rep.StrategyBead)
	require.NotNil(t, findCmd(rep.Commands, "close", "sp-s2q", "--reason", "demoted to retrospective input"))
}

func TestEraCloseMemoryProposalsClassifyPerHeuristic(t *testing.T) {
	var calls [][]string
	b := &BeadsClient{BDBin: "bd", RigDir: "/rig", Exec: eraCloseFixtureExec(t, &calls)}
	start, end := eraCloseWindow()

	rep, err := EraClose(context.Background(), b, "torwind", "2026-07-05", start, end, "TORWIND", false)
	require.NoError(t, err)

	require.Len(t, rep.MemoryProposals, 3)
	byKey := map[string]MemoryProposal{}
	for _, p := range rep.MemoryProposals {
		byKey[p.Key] = p
	}
	require.Equal(t, "KEEP", byKey["L1"].Action)
	require.Equal(t, "REWRITE", byKey["L47"].Action)
	require.Equal(t, "RETIRE", byKey["L60"].Action)
}

func TestEraCloseWindowEndIncludesEntireEndDate(t *testing.T) {
	var calls [][]string
	beadsJSON := `[
		{"id":"sp-9","issue_type":"decision","labels":[],"status":"open","created_at":"2026-06-30T15:00:00Z"}
	]`
	exec := func(_ context.Context, name string, args ...string) (string, error) {
		calls = append(calls, append([]string{name}, args...))
		if len(args) > 0 && args[0] == "list" {
			return beadsJSON, nil
		}
		if len(args) > 0 && args[0] == "memories" {
			return `{}`, nil
		}
		return "", nil
	}
	b := &BeadsClient{BDBin: "bd", RigDir: "/rig", Exec: exec}
	start, _ := time.Parse("2006-01-02", "2026-06-01")
	end, _ := time.Parse("2006-01-02", "2026-06-30")

	rep, err := EraClose(context.Background(), b, "torwind", "2026-07-05", start, end, "TORWIND", false)
	require.NoError(t, err)

	require.Equal(t, []string{"sp-9"}, rep.Labeled,
		"--window-end is documented as inclusive (YYYY-MM-DD); a bead created during the daytime of the end date must still be in-window, not just one created at its midnight instant")
}

func TestClassifyMemoryTableDriven(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		agent  string
		action string
	}{
		{"universal seed", "Probes are cheap: keep one probe per market.", "TORWIND", "KEEP"},
		{"rule with trailing evidence", "phantom cache recurs after each contract; ship refresh is the first move. TORWIND-3 cached 44/80 IRON_ORE.", "TORWIND", "REWRITE"},
		{"pure instance fact", "D45 is the only ADVANCED_CIRCUITRY exporter.", "TORWIND", "RETIRE"},
		{"leading ship instance", "TORWIND-3 always ran the contract loop.", "TORWIND", "RETIRE"},
		{"empty text", "", "TORWIND", "KEEP"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			action, reason := classifyMemory(tc.text, tc.agent)
			require.Equal(t, tc.action, action)
			require.NotEmpty(t, reason)
		})
	}
}
