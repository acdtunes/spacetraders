package captainsup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDisabledKillSwitch(t *testing.T) {
	dir := t.TempDir()
	ws := NewWorkspace(dir)
	require.False(t, ws.Disabled())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "DISABLED"), nil, 0o644))
	require.True(t, ws.Disabled())
}

func TestDisabledFixesKillSwitch(t *testing.T) {
	dir := t.TempDir()
	ws := NewWorkspace(dir)
	require.False(t, ws.DisabledFixes())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "DISABLED_FIXES"), nil, 0o644))
	require.True(t, ws.DisabledFixes())
}
