package watchkeeper

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestParseBeadID is exercised against real subject lines from this repo's own
// git log (see `git log --oneline` on shipwright/sp-ess3's base) so the regex
// is proven against the actual commit-message convention, not an invented one.
func TestParseBeadID(t *testing.T) {
	cases := []struct {
		name    string
		subject string
		want    string
	}{
		{
			name:    "real subject: trade-route self-collision fix",
			subject: "fix(daemon): trade-route navigates claimed hull in-process (no self-collision) + stale-ask guard + self-diagnosing abort (sp-2sam)",
			want:    "sp-2sam",
		},
		{
			name:    "real subject: trade-route discipline floor fix",
			subject: "fix(daemon): trade-route selects a lane that clears the discipline floor — no silent zero-visit runs (sp-sh6w)",
			want:    "sp-sh6w",
		},
		{
			name:    "real subject: trade-route FK-safe persistence fix",
			subject: "fix(daemon): trade-route persists its container before the ship claim — FK-safe + recovery-safe release (sp-r3cl)",
			want:    "sp-r3cl",
		},
		{
			name:    "real subject: pure-arbitrage trade engine feature",
			subject: "feat(cli): pure-arbitrage trade engine — market spreads verb + disciplined trade-route workflow (sp-s7c2)",
			want:    "sp-s7c2",
		},
		{
			name:    "real subject: per-wake telemetry feature",
			subject: "feat(cli): per-wake token/usage telemetry + captain tokens verb (sp-593x)",
			want:    "sp-593x",
		},
		{
			name:    "no bead id at all",
			subject: "chore: bump dependency versions",
			want:    "",
		},
		{
			name:    "empty subject",
			subject: "",
			want:    "",
		},
		{
			name:    "trailing whitespace after the parens is tolerated",
			subject: "fix: something (sp-abcd)  \n",
			want:    "sp-abcd",
		},
		{
			name:    "a parenthetical that isn't trailing must not match",
			subject: "fix(daemon): handle the (edge case) properly",
			want:    "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, parseBeadID(c.subject))
		})
	}
}

// TestBeadIDFromHEADDegradesGracefullyOutsideAGitRepo proves the "never block
// or fail the daemon boot" contract independent of this repo's own history: a
// directory that is guaranteed not to be inside any git repo (a fresh
// t.TempDir()) must yield an empty bead id, not a panic or a blocked call.
func TestBeadIDFromHEADDegradesGracefullyOutsideAGitRepo(t *testing.T) {
	dir := t.TempDir()
	fn := BeadIDFromHEAD(dir)

	require.NotPanics(t, func() {
		got := fn()
		require.Empty(t, got, "no git repo at all must degrade to an empty bead id, never panic or error out")
	})
}
