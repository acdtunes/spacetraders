// Package telemetry is the I/O adapter that feeds internal/domain/telemetry.
// It enumerates the live `gc` sessions (which map an agent alias to its claude
// SessionKey) and resolves each session's claude-code transcript on disk, so the
// pure domain parser can aggregate token usage from it. This is additive
// read-only telemetry: it never touches the watchkeeper wake path — it observes
// the transcripts those externally-run sessions already wrote.
package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	domain "github.com/andrescamacho/spacetraders-go/internal/domain/telemetry"
)

// Session is the subset of a `gc session list --json` entry the collector needs:
// the agent alias, the claude SessionKey (which names the transcript file), and
// the session working directory.
type Session struct {
	Alias      string `json:"Alias"`
	SessionKey string `json:"SessionKey"`
	WorkDir    string `json:"WorkDir"`
}

// SessionLister returns the current gc sessions. Injectable so tests exercise
// the collector without a live `gc`.
type SessionLister func(ctx context.Context) ([]Session, error)

// TranscriptOpener opens the transcript for a session key. It returns
// (nil, nil) when the session has no transcript yet — that is an expected state,
// not an error. Injectable for tests.
type TranscriptOpener func(sessionKey string) (io.ReadCloser, error)

// Collector aggregates per-session token usage from claude transcripts.
type Collector struct {
	List SessionLister
	Open TranscriptOpener
}

// Collect lists the live sessions and aggregates each one's token usage over the
// window `since`. A session with no transcript is skipped; a transcript that
// fails to parse is skipped rather than aborting the whole sweep, so one bad
// session never blinds the fleet-wide numbers. A lister error is fatal (there is
// nothing to report without the session map).
func (c Collector) Collect(ctx context.Context, since time.Time) ([]domain.SessionUsage, error) {
	sessions, err := c.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list gc sessions: %w", err)
	}

	usages := make([]domain.SessionUsage, 0, len(sessions))
	for _, s := range sessions {
		rc, err := c.Open(s.SessionKey)
		if err != nil {
			return nil, fmt.Errorf("open transcript for %s: %w", s.Alias, err)
		}
		if rc == nil {
			continue
		}
		stats, perr := domain.ParseTranscript(rc, since)
		rc.Close()
		if perr != nil {
			continue
		}
		if stats.Usage.Total() == 0 && stats.Turns == 0 {
			continue
		}
		usages = append(usages, domain.SessionUsage{
			Alias:         s.Alias,
			SessionKey:    s.SessionKey,
			Usage:         stats.Usage,
			Turns:         stats.Turns,
			FirstActivity: stats.FirstActivity,
			LastActivity:  stats.LastActivity,
		})
	}
	return usages, nil
}

// NewLiveCollector wires a Collector against the real `gc` binary (run in
// cityDir) and the claude transcript store under projectsRoot. When projectsRoot
// is empty it defaults to the standard claude-code location.
func NewLiveCollector(gcBin, cityDir, projectsRoot string) Collector {
	if projectsRoot == "" {
		projectsRoot = DefaultProjectsRoot()
	}
	return Collector{
		List: gcSessionLister(gcBin, cityDir),
		Open: globTranscriptOpener(projectsRoot),
	}
}

// DefaultProjectsRoot returns the directory claude-code writes session
// transcripts under: $CLAUDE_CONFIG_DIR/projects when set, else ~/.claude/projects.
func DefaultProjectsRoot() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "projects")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// gcSessionLister shells out to `gc session list --state all --json` in cityDir.
// --state all is used so suspended/closed sessions still contribute their
// already-spent tokens to the report.
func gcSessionLister(gcBin, cityDir string) SessionLister {
	return func(ctx context.Context) ([]Session, error) {
		cmd := exec.CommandContext(ctx, gcBin, "session", "list", "--state", "all", "--json")
		cmd.Dir = cityDir
		var out, errBuf bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errBuf
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("%s session list failed: %w (stderr: %s)", gcBin, err, strings.TrimSpace(errBuf.String()))
		}
		return parseSessionList(out.Bytes())
	}
}

// parseSessionList decodes `gc session list --json` output, tolerating any
// leading non-JSON noise `gc` may print before the array (e.g. pack-loading
// warnings) by scanning to the first '['.
func parseSessionList(raw []byte) ([]Session, error) {
	if i := bytes.IndexByte(raw, '['); i > 0 {
		raw = raw[i:]
	}
	var sessions []Session
	if err := json.Unmarshal(raw, &sessions); err != nil {
		return nil, fmt.Errorf("parse gc session list: %w", err)
	}
	return sessions, nil
}

// globTranscriptOpener resolves a transcript by session key. Claude-code names
// each transcript <projectsRoot>/<munged-cwd>/<SessionKey>.jsonl; since the
// SessionKey (a UUID) is globally unique, a glob across project dirs finds it
// without having to reproduce claude's cwd-munging rule.
func globTranscriptOpener(projectsRoot string) TranscriptOpener {
	return func(sessionKey string) (io.ReadCloser, error) {
		if projectsRoot == "" || sessionKey == "" {
			return nil, nil
		}
		matches, err := filepath.Glob(filepath.Join(projectsRoot, "*", sessionKey+".jsonl"))
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, nil
		}
		return os.Open(matches[0])
	}
}
