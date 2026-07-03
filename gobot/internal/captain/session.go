package captainsup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ErrUsageLimit means the Max-subscription window is exhausted. This is a
// normal state, not a failure: the supervisor backs off and events queue.
var ErrUsageLimit = errors.New("claude usage limit reached")

type SessionRunner interface {
	Run(ctx context.Context, prompt string) error
}

type ClaudeRunner struct {
	Bin     string
	Model   string
	WorkDir string
	Timeout time.Duration
	// ExtraArgs are appended to the claude invocation. Fix sessions in
	// throwaway worktrees pass --dangerously-skip-permissions: the paths are
	// untrusted workspaces (allowlists are ignored there) and the supervisor
	// gate, not session permissions, is the safety boundary.
	ExtraArgs []string
}

var _ SessionRunner = (*ClaudeRunner)(nil)

func NewClaudeRunner(bin, model, workDir string, timeout time.Duration) *ClaudeRunner {
	return &ClaudeRunner{Bin: bin, Model: model, WorkDir: workDir, Timeout: timeout}
}

func (r *ClaudeRunner) Run(ctx context.Context, prompt string) error {
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	args := append([]string{"-p", "--model", r.Model}, r.ExtraArgs...)
	cmd := exec.CommandContext(ctx, r.Bin, args...)
	cmd.Dir = r.WorkDir
	cmd.Stdin = strings.NewReader(prompt)

	// Scrub ANTHROPIC_API_KEY: with it set, claude bills the API instead of
	// the Max subscription (spec: LLM runtime).
	env := os.Environ()
	scrubbed := env[:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") {
			continue
		}
		scrubbed = append(scrubbed, kv)
	}
	cmd.Env = scrubbed

	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf

	err := cmd.Run()
	if err != nil {
		combined := strings.ToLower(out.String() + " " + errBuf.String())
		if strings.Contains(combined, "usage limit") || strings.Contains(combined, "rate limit") ||
			strings.Contains(combined, "session limit") || strings.Contains(combined, "hit your") {
			return fmt.Errorf("%w: %s", ErrUsageLimit, strings.TrimSpace(errBuf.String()))
		}
		stdoutTail := out.String()
		if len(stdoutTail) > 800 {
			stdoutTail = stdoutTail[len(stdoutTail)-800:]
		}
		return fmt.Errorf("claude session failed: %w (stderr: %s) (stdout tail: %s)",
			err, strings.TrimSpace(errBuf.String()), strings.TrimSpace(stdoutTail))
	}
	return nil
}
