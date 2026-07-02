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

func TestTailReturnsLastBytesAndEmptyWhenMissing(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "state"), 0o755))
	ws := NewWorkspace(dir)
	require.Equal(t, "", ws.Tail("captain-log.md", 100))

	content := "OLD-OLD-OLD\nNEW-TAIL"
	require.NoError(t, os.WriteFile(ws.StatePath("captain-log.md"), []byte(content), 0o644))
	require.Equal(t, "NEW-TAIL", ws.Tail("captain-log.md", 8))
	require.Equal(t, content, ws.Tail("captain-log.md", 10000))
}
