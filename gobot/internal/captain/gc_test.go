package captainsup

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func recordingExec(calls *[][]string, out string) Execer {
	return func(_ context.Context, name string, args ...string) (string, error) {
		*calls = append(*calls, append([]string{name}, args...))
		return out, nil
	}
}

// TestScrubbedExecDropsAnthropicKeyAndGCPrefixedEnvVars guards against the
// child process resolving a different city than the one pinned by cwd: if a
// caller (human or agent) has GC_* env vars set for another city, those must
// never leak into the child even though we don't enumerate every GC_ var by
// name.
func TestScrubbedExecDropsAnthropicKeyAndGCPrefixedEnvVars(t *testing.T) {
	t.Setenv("GC_DIR", "/tmp/other-city")
	t.Setenv("GC_SESSION_ID", "xx")
	t.Setenv("GC_SOME_FUTURE_VAR_NOT_YET_INVENTED", "future")
	t.Setenv("ANTHROPIC_API_KEY", "k")
	t.Setenv("KEEP_ME", "1")

	run := scrubbedExec(t.TempDir())
	out, err := run(context.Background(), "env")
	require.NoError(t, err)

	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		require.Falsef(t, strings.HasPrefix(line, "GC_"), "GC_ env var leaked to child: %q", line)
	}
	require.NotContains(t, out, "ANTHROPIC_API_KEY=k")
	require.Contains(t, out, "KEEP_ME=1")
}

func TestNudgeInvokesGCSessionNudge(t *testing.T) {
	var calls [][]string
	g := &CityGateway{GCBin: "gc", CityDir: "/city", Exec: recordingExec(&calls, "")}
	require.NoError(t, g.Nudge(context.Background(), "captain", "3 events pending"))
	require.Len(t, calls, 1)
	require.Equal(t, "gc", calls[0][0])
	require.Contains(t, calls[0], "nudge")
	require.Contains(t, calls[0], "captain")
	require.Contains(t, calls[0], "3 events pending")
}

func TestSendMailInvokesGCMailSend(t *testing.T) {
	var calls [][]string
	g := &CityGateway{GCBin: "gc", CityDir: "/city", Exec: recordingExec(&calls, "")}
	require.NoError(t, g.SendMail(context.Background(), "human", "wake: 2 events", "body text"))
	require.Len(t, calls, 1)
	require.Equal(t, "gc", calls[0][0])
	require.Contains(t, calls[0], "mail")
	require.Contains(t, calls[0], "send")
	require.Contains(t, calls[0], "human")
	require.Contains(t, calls[0], "wake: 2 events")
	require.Contains(t, calls[0], "body text")
}

func TestSessionAliveParsesListOutput(t *testing.T) {
	cases := []struct {
		name  string
		out   string
		alias string
		want  bool
	}{
		{"active alias present", `[{"alias":"captain","state":"active"}]`, "captain", true},
		{"closed alias absent", `[{"alias":"captain","state":"closed"}]`, "captain", false},
		{"different alias", `[{"alias":"mayor","state":"active"}]`, "captain", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls [][]string
			g := &CityGateway{GCBin: "gc", Exec: recordingExec(&calls, tc.out)}
			alive, err := g.SessionAlive(context.Background(), tc.alias)
			require.NoError(t, err)
			require.Equal(t, tc.want, alive)
			require.Contains(t, calls[0], "list")
			require.Contains(t, calls[0], "--json")
		})
	}
}

func TestSpawnSessionCreatesAndPrimes(t *testing.T) {
	var calls [][]string
	g := &CityGateway{GCBin: "gc", CityDir: "/city", Exec: recordingExec(&calls, "primed prompt")}
	require.NoError(t, g.SpawnSession(context.Background(), "captain", "captain"))
	require.Equal(t, "gc", calls[0][0])
	require.Contains(t, calls[0], "new")
	require.Contains(t, calls[0], "captain")
	require.Contains(t, calls[0], "--no-attach")
}

func TestListInProgressPipelineParsesBeads(t *testing.T) {
	var calls [][]string
	out := `[{"id":"sp-1","issue_type":"bug","owner":"shipwright"},{"id":"sp-2","issue_type":"feature","owner":"shipwright"},{"id":"sp-3","issue_type":"task","owner":"shipwright"}]`
	b := &BeadsClient{BDBin: "bd", RigDir: "/rig", Exec: recordingExec(&calls, out)}
	beads, err := b.ListInProgressPipeline(context.Background())
	require.NoError(t, err)
	require.Equal(t, []PipelineBead{
		{ID: "sp-1", Type: "bug", Assignee: "shipwright"},
		{ID: "sp-2", Type: "feature", Assignee: "shipwright"},
	}, beads)
	require.Len(t, calls, 1)
	require.Contains(t, calls[0], "list")
	require.Contains(t, calls[0], "in_progress")
	require.Contains(t, calls[0], "shipwright")
	require.NotContains(t, calls[0], "bug,feature")
}

func TestReopenRunsBdUpdate(t *testing.T) {
	var calls [][]string
	b := &BeadsClient{BDBin: "bd", RigDir: "/rig", Exec: recordingExec(&calls, "")}
	require.NoError(t, b.Reopen(context.Background(), "sp-abc", "shipwright session died"))
	require.Contains(t, calls[0], "update")
	require.Contains(t, calls[0], "sp-abc")
	require.Contains(t, calls[0], "open")
}
