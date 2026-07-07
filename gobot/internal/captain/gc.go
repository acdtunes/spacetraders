package captainsup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type Execer func(ctx context.Context, name string, args ...string) (stdout string, err error)

// scrubbedExec runs binaries with cwd set to dir and ANTHROPIC_API_KEY plus
// every GC_*-prefixed env var removed from the child env. ANTHROPIC_API_KEY
// must go: with it set, claude bills the API instead of the Max subscription.
// GC_* vars must go too: the gateway must behave identically whether it's
// launched by launchd (a clean env) or from a manual shell that carries
// another city's GC_DIR/GC_SESSION_ID/etc. cwd (dir) is the only city pin we
// want the child to see — an inherited GC_DIR would make the child `gc`
// resolve a different city than the one this gateway was constructed for,
// and inherited GC_SESSION_ID/GC_ALIAS would misattribute mail sender
// identity to the caller instead of this gateway. The match is a prefix, not
// an enumerated list, so future GC_ vars are covered automatically.
func scrubbedExec(dir string) Execer {
	return func(ctx context.Context, name string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = dir

		env := os.Environ()
		scrubbed := env[:0]
		for _, kv := range env {
			if strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") || strings.HasPrefix(kv, "GC_") {
				continue
			}
			scrubbed = append(scrubbed, kv)
		}
		cmd.Env = scrubbed

		var out, errBuf bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errBuf
		if err := cmd.Run(); err != nil {
			return out.String(), fmt.Errorf("%s failed: %w (stderr: %s)", name, err, strings.TrimSpace(errBuf.String()))
		}
		return out.String(), nil
	}
}

type CityGateway struct {
	GCBin   string
	CityDir string
	Exec    Execer
}

func NewCityGateway(gcBin, cityDir string) *CityGateway {
	return &CityGateway{GCBin: gcBin, CityDir: cityDir, Exec: scrubbedExec(cityDir)}
}

func (g *CityGateway) SendMail(ctx context.Context, to, subject, body string) error {
	_, err := g.Exec(ctx, g.GCBin, "mail", "send", to, "-s", subject, "-m", body)
	return err
}

func (g *CityGateway) Nudge(ctx context.Context, alias, text string) error {
	_, err := g.Exec(ctx, g.GCBin, "session", "nudge", alias, text)
	return err
}

func (g *CityGateway) SessionAlive(ctx context.Context, alias string) (bool, error) {
	out, err := g.Exec(ctx, g.GCBin, "session", "list", "--json")
	if err != nil {
		return false, err
	}
	var sessions []struct {
		Alias string
		State string
	}
	if err := json.Unmarshal([]byte(out), &sessions); err != nil {
		return false, fmt.Errorf("parse session list: %w", err)
	}
	for _, s := range sessions {
		if s.Alias == alias && (s.State == "active" || s.State == "running") {
			return true, nil
		}
	}
	return false, nil
}

type BeadsClient struct {
	BDBin  string
	RigDir string
	Exec   Execer
}

func NewBeadsClient(bdBin, rigDir string) *BeadsClient {
	return &BeadsClient{BDBin: bdBin, RigDir: rigDir, Exec: scrubbedExec(rigDir)}
}

type PipelineBead struct{ ID, Type, Assignee string }

func (b *BeadsClient) ListInProgressPipeline(ctx context.Context) ([]PipelineBead, error) {
	out, err := b.Exec(ctx, b.BDBin, "list", "--label", "shipwright", "--status", "in_progress", "--json")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		ID        string `json:"id"`
		IssueType string `json:"issue_type"`
		Owner     string `json:"owner"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("parse bd list: %w", err)
	}
	beads := make([]PipelineBead, 0, len(raw))
	for _, r := range raw {
		if r.IssueType != "bug" && r.IssueType != "feature" {
			continue
		}
		beads = append(beads, PipelineBead{ID: r.ID, Type: r.IssueType, Assignee: r.Owner})
	}
	return beads, nil
}

func (b *BeadsClient) Reopen(ctx context.Context, id, reason string) error {
	_, err := b.Exec(ctx, b.BDBin, "update", id, "--status", "open", "--append-notes", reason)
	return err
}
