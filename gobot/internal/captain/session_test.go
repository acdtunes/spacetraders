package captainsup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func writeStub(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude-stub")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755))
	return path
}

func TestClaudeRunnerPassesPromptAndScrubsAPIKey(t *testing.T) {
	out := filepath.Join(t.TempDir(), "capture")
	stub := writeStub(t, `cat > `+out+`.prompt; echo "$ANTHROPIC_API_KEY" > `+out+`.key; echo "args: $@" > `+out+`.args`)
	t.Setenv("ANTHROPIC_API_KEY", "sk-secret")

	r := NewClaudeRunner(stub, "opus", t.TempDir(), time.Minute)
	require.NoError(t, r.Run(context.Background(), "HELLO CAPTAIN"))

	prompt, _ := os.ReadFile(out + ".prompt")
	require.Equal(t, "HELLO CAPTAIN", string(prompt))
	key, _ := os.ReadFile(out + ".key")
	require.Equal(t, "\n", string(key), "ANTHROPIC_API_KEY must be scrubbed")
	args, _ := os.ReadFile(out + ".args")
	require.Contains(t, string(args), "-p")
	require.Contains(t, string(args), "--model opus")
}

func TestClaudeRunnerDetectsUsageLimit(t *testing.T) {
	stub := writeStub(t, `echo "You have reached your usage limit" >&2; exit 1`)
	r := NewClaudeRunner(stub, "opus", t.TempDir(), time.Minute)
	err := r.Run(context.Background(), "x")
	require.ErrorIs(t, err, ErrUsageLimit)
}

func TestClaudeRunnerTimesOut(t *testing.T) {
	stub := writeStub(t, `sleep 5`)
	r := NewClaudeRunner(stub, "opus", t.TempDir(), 100*time.Millisecond)
	err := r.Run(context.Background(), "x")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrUsageLimit)
}
