package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The live config.yaml carries player_id under the captain: block with a 3-space
// indent and an inline comment (config.yaml:104). The repoint MUST preserve that
// formatting byte-for-byte, changing only the numeric value (sp-nax3 / sp-m602).
const captainBlockFixture = `daemon:
   max_containers: 100

captain:
   enabled: true
   max_feature_diff_lines: 1500           # master switch
   player_id: 2             # which player the captain commands (repoint at every era reset)
   workspace_dir: ../captain

contract:
   player_id: 99   # unrelated block — must NOT be touched
`

func TestSetCaptainPlayerIDReplacesValuePreservingCommentAndIndent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(captainBlockFixture), 0644))

	changed, err := SetCaptainPlayerID(path, 3)
	require.NoError(t, err)
	require.True(t, changed)

	out, err := os.ReadFile(path)
	require.NoError(t, err)
	got := string(out)

	// Only the captain block's player_id digit changed; indentation + inline comment intact.
	require.Contains(t, got, "   player_id: 3             # which player the captain commands (repoint at every era reset)")
	require.NotContains(t, got, "player_id: 2 ")
	// The unrelated contract: block is scoped out and untouched.
	require.Contains(t, got, "   player_id: 99   # unrelated block — must NOT be touched")
	// Every other line survives verbatim.
	require.Contains(t, got, "   max_feature_diff_lines: 1500           # master switch")
	require.Contains(t, got, "   workspace_dir: ../captain")
}

func TestSetCaptainPlayerIDNoopWhenAlreadySet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(captainBlockFixture), 0644))

	changed, err := SetCaptainPlayerID(path, 2)
	require.NoError(t, err)
	require.False(t, changed, "already at target — must be a byte-identical no-op")

	out, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, captainBlockFixture, string(out))
}

func TestSetCaptainPlayerIDErrorsWhenNoCaptainPlayerID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("daemon:\n   max_containers: 100\ncaptain:\n   enabled: true\n"), 0644))

	_, err := SetCaptainPlayerID(path, 3)
	require.Error(t, err, "must fail loud when there is no captain.player_id to repoint")
}

func TestResolveConfigFilePathHonorsExplicitEnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("daemon:\n   max_containers: 1\n"), 0644))

	t.Setenv(configPathEnvVar, path)
	require.Equal(t, path, ResolveConfigFilePath())
}
