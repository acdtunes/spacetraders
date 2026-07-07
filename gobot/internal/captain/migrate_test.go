package watchkeeper

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func recordingMigrateExec(calls *[][]string, out string) Execer {
	return func(ctx context.Context, name string, args ...string) (string, error) {
		*calls = append(*calls, append([]string{name}, args...))
		return out, nil
	}
}

func writeMigrateFixture(t *testing.T) (stateDir, reportsDir string) {
	t.Helper()
	root := t.TempDir()
	stateDir = filepath.Join(root, "state")
	reportsDir = filepath.Join(root, "reports", "bugs")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	require.NoError(t, os.MkdirAll(reportsDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "strategy.md"),
		[]byte("# Strategy\n\nHold posture; grow the contract loop.\n"), 0o644))

	decisions := `{"id":"d-1","ts":"2026-07-02T21:57:24Z","action":"Deploy TORWIND-2 to scout all markets","outcome":"scouted 12 markets"}
{"id":"d-2","ts":"2026-07-02T21:57:24Z","action":"Run batch-contract on TORWIND-1"}
{"id":"d-3","ts":"2026-07-02T22:00:00Z","lesson":"solar scouts pay for themselves","verdict":"confirmed","outcome":"cached markets"}
`
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "decisions.jsonl"), []byte(decisions), 0o644))

	lessons := "# Lessons (max 50)\n\nFormat: whatever.\n\n" +
		"L1 [seed] — Probes are cheap: keep one probe per markets.\n" +
		"L2 [d-1] — Buy at exporters, sell at importers.\n" +
		"  continuation line for L2.\n"
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "lessons.md"), []byte(lessons), 0o644))

	friction := "# Friction\n\n- friction: (s1) ledger list demands --player-id.\n" +
		"- friction: (s2) goods produce has no --dry-run.\n"
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "friction.md"), []byte(friction), 0o644))

	newReport := "---\ntitle: pipeline crashes on launch\nstatus: new\nkind: fix\n---\n\n## Failure\nboom\n"
	require.NoError(t, os.WriteFile(filepath.Join(reportsDir, "2026-01-01-new.md"), []byte(newReport), 0o644))
	mergedReport := "---\ntitle: old bug already fixed\nstatus: merged\nkind: fix\n---\n\n## Failure\nfixed\n"
	require.NoError(t, os.WriteFile(filepath.Join(reportsDir, "2026-01-02-merged.md"), []byte(mergedReport), 0o644))

	return stateDir, reportsDir
}

func cmdMatches(c []string, tokens ...string) bool {
	joined := strings.Join(c, "\x00")
	for _, tok := range tokens {
		if !strings.Contains(joined, tok) {
			return false
		}
	}
	return true
}

func findCmd(cmds [][]string, tokens ...string) []string {
	for _, c := range cmds {
		if cmdMatches(c, tokens...) {
			return c
		}
	}
	return nil
}

func TestMigrateDryRunExecutesNothing(t *testing.T) {
	var calls [][]string
	b := &BeadsClient{BDBin: "bd", RigDir: "/rig", Exec: recordingMigrateExec(&calls, "sp-new")}
	stateDir, reportsDir := writeMigrateFixture(t)

	rep, err := Migrate(context.Background(), b, stateDir, reportsDir, false)
	require.NoError(t, err)
	require.Empty(t, calls, "dry-run must execute nothing")
	require.NotEmpty(t, rep.Commands, "dry-run must still plan commands")
}

func TestMigrateApplyExecutesEachPlannedCommand(t *testing.T) {
	var calls [][]string
	b := &BeadsClient{BDBin: "bd", RigDir: "/rig", Exec: recordingMigrateExec(&calls, "sp-new")}
	stateDir, reportsDir := writeMigrateFixture(t)

	rep, err := Migrate(context.Background(), b, stateDir, reportsDir, true)
	require.NoError(t, err)
	require.Equal(t, len(rep.Commands), len(calls), "apply must execute every planned command")
	require.NotEmpty(t, calls)
}

func TestMigrateDecisionsCreateNoteClose(t *testing.T) {
	var calls [][]string
	b := &BeadsClient{BDBin: "bd", RigDir: "/rig", Exec: recordingMigrateExec(&calls, "sp-new")}
	stateDir, reportsDir := writeMigrateFixture(t)

	rep, err := Migrate(context.Background(), b, stateDir, reportsDir, false)
	require.NoError(t, err)
	require.Equal(t, 3, rep.Decisions)

	require.NotNil(t, findCmd(rep.Commands, "create", "-t", "decision", "-l", "migrated", "Deploy TORWIND-2 to scout all markets"))
	require.NotNil(t, findCmd(rep.Commands, "note", "outcome: scouted 12 markets"))
	require.NotNil(t, findCmd(rep.Commands, "close", "--reason", "historical"))

	// d-3 has no action: title falls back to the lesson field, never empty.
	require.NotNil(t, findCmd(rep.Commands, "create", "-t", "decision", "solar scouts pay for themselves"))

	// d-1 and d-3 carry outcomes (2 notes); d-2 has none.
	notes := 0
	for _, c := range rep.Commands {
		if len(c) > 1 && c[1] == "note" {
			notes++
		}
	}
	require.Equal(t, 2, notes, "only decisions with an outcome get a note")
}

func TestMigrateLessonsRemember(t *testing.T) {
	var calls [][]string
	b := &BeadsClient{BDBin: "bd", RigDir: "/rig", Exec: recordingMigrateExec(&calls, "sp-new")}
	stateDir, reportsDir := writeMigrateFixture(t)

	rep, err := Migrate(context.Background(), b, stateDir, reportsDir, false)
	require.NoError(t, err)
	require.Equal(t, 2, rep.Lessons)
	require.NotNil(t, findCmd(rep.Commands, "remember", "Probes are cheap"))
	require.NotNil(t, findCmd(rep.Commands, "remember", "continuation line for L2"))
}

func TestMigrateBugReportsSkipsTerminalStatus(t *testing.T) {
	var calls [][]string
	b := &BeadsClient{BDBin: "bd", RigDir: "/rig", Exec: recordingMigrateExec(&calls, "sp-new")}
	stateDir, reportsDir := writeMigrateFixture(t)

	rep, err := Migrate(context.Background(), b, stateDir, reportsDir, false)
	require.NoError(t, err)
	require.Equal(t, 1, rep.Bugs, "only the non-terminal report is migrated")
	require.NotNil(t, findCmd(rep.Commands, "create", "-t", "bug", "-l", "shipwright", "pipeline crashes on launch"))
	require.Nil(t, findCmd(rep.Commands, "old bug already fixed"), "merged report must be skipped")
}

func TestMigrateStrategyAndBacklogCounts(t *testing.T) {
	var calls [][]string
	b := &BeadsClient{BDBin: "bd", RigDir: "/rig", Exec: recordingMigrateExec(&calls, "sp-new")}
	stateDir, reportsDir := writeMigrateFixture(t)

	rep, err := Migrate(context.Background(), b, stateDir, reportsDir, false)
	require.NoError(t, err)
	require.Equal(t, 1, rep.Strategy)
	require.Equal(t, 2, rep.Backlog, "two friction bullets")
	require.NotNil(t, findCmd(rep.Commands, "create", "Fleet strategy", "-t", "design", "-l", "strategy", "--body-file"))
	require.NotNil(t, findCmd(rep.Commands, "create", "-t", "feature", "-l", "friction", "-p", "3"))
}
