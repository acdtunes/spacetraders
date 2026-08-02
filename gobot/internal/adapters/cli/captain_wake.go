package cli

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	watchkeeper "github.com/andrescamacho/spacetraders-go/internal/captain"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// wakePolicyStore is the subset of captain wake-policy persistence the CLI
// needs. It exists so runWakeSet/runWakeShow can be tested with a fake,
// without touching the filesystem.
type wakePolicyStore interface {
	Load() (watchkeeper.WakePolicy, error)
	Save(policy watchkeeper.WakePolicy) error
}

// fileWakePolicyStore is the production wakePolicyStore, backed by the
// supervisor's on-disk state file (the same file the supervisor itself
// reads every Tick — see internal/captain/state.go).
type fileWakePolicyStore struct {
	path string
}

func (f fileWakePolicyStore) Load() (watchkeeper.WakePolicy, error) {
	return watchkeeper.LoadWakePolicy(f.path)
}

func (f fileWakePolicyStore) Save(policy watchkeeper.WakePolicy) error {
	return watchkeeper.SaveWakePolicy(f.path, policy)
}

// captainWakeStatePath resolves the supervisor state file path from the
// loaded config's Captain.WorkspaceDir, mirroring how the supervisor itself
// (internal/captain.Workspace.StatePath) locates it.
func captainWakeStatePath() (string, error) {
	cfg, err := config.LoadConfig("")
	if err != nil {
		return "", fmt.Errorf("failed to load config: %w", err)
	}
	return watchkeeper.NewWorkspace(cfg.Captain.WorkspaceDir).StatePath(), nil
}

func newCaptainWakePolicyStore() (wakePolicyStore, error) {
	path, err := captainWakeStatePath()
	if err != nil {
		return nil, err
	}
	return fileWakePolicyStore{path: path}, nil
}

// parseNextWakeAt parses the --next-wake-at flag value, accepting either a
// relative duration applied to now (e.g. "+3h", "+30m") or an absolute
// RFC3339 timestamp.
func parseNextWakeAt(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("--next-wake-at: empty value")
	}
	if strings.HasPrefix(s, "+") {
		d, err := time.ParseDuration(s[1:])
		if err != nil {
			return time.Time{}, fmt.Errorf("--next-wake-at: invalid relative duration %q: %w", s, err)
		}
		return now.Add(d), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("--next-wake-at: %q must be a relative duration like \"+3h\" or an RFC3339 timestamp: %w", s, err)
	}
	return t, nil
}

// parseInterruptTypes splits a comma-separated --interrupt-types value into
// a trimmed, non-empty slice. An empty (or all-whitespace) input yields nil,
// meaning "no override — the default interrupt set applies."
func parseInterruptTypes(csv string) []string {
	if strings.TrimSpace(csv) == "" {
		return nil
	}
	tokens := strings.Split(csv, ",")
	out := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		trimmed := strings.TrimSpace(tok)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

// runWakeSet stamps DeclaredAt and persists policy. Each call fully replaces
// whatever wake policy was previously declared (full-replace semantics): the
// caller is responsible for populating only the fields this invocation's
// flags provided, leaving the rest nil/empty.
func runWakeSet(store wakePolicyStore, now time.Time, policy watchkeeper.WakePolicy) error {
	policy.DeclaredAt = now
	return store.Save(policy)
}

// runWakeShow renders the currently-declared wake policy to w.
func runWakeShow(store wakePolicyStore, w io.Writer) error {
	policy, err := store.Load()
	if err != nil {
		return fmt.Errorf("failed to load wake policy: %w", err)
	}

	fmt.Fprintf(w, "next_wake_at:    %s\n", formatOptionalTime(policy.NextWakeAt))
	fmt.Fprintf(w, "credits_above:   %s\n", formatOptionalInt(policy.CreditsAbove))
	fmt.Fprintf(w, "credits_below:   %s\n", formatOptionalInt(policy.CreditsBelow))
	fmt.Fprintf(w, "interrupt_types: %s\n", formatInterruptTypes(policy.InterruptTypes))
	if policy.DeclaredAt.IsZero() {
		fmt.Fprintf(w, "declared_at:     (never declared — defaults apply)\n")
	} else {
		fmt.Fprintf(w, "declared_at:     %s\n", policy.DeclaredAt.Format(time.RFC3339))
	}
	return nil
}

func formatOptionalTime(t *time.Time) string {
	if t == nil {
		return "(not set)"
	}
	return t.Format(time.RFC3339)
}

func formatOptionalInt(v *int) string {
	if v == nil {
		return "(not set)"
	}
	return strconv.Itoa(*v)
}

func formatInterruptTypes(types []string) string {
	if len(types) == 0 {
		return "(not set)"
	}
	return strings.Join(types, ",")
}

func newCaptainWakeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wake",
		Short: "Inspect or declare the captain's wake policy",
		Long: `Inspect or declare when the supervisor wakes the captain outside its default
heartbeat cadence (spec: sp-sk68 wake model).

"wake set" declares the standing policy — the next scheduled wake, credit
thresholds that force a wake, and which event types interrupt immediately —
with each call fully replacing the prior policy. "wake show" prints the
currently declared policy (or the defaults if none). "wake watch" arms
one-shot wake watches on a specific ship arrival or container terminal state,
which fire once and auto-disarm independently of the standing policy.

A declaration takes effect on the very next supervisor poll — no restart
required.

Examples:
  spacetraders captain wake set --next-wake-at +3h
  spacetraders captain wake show`,
	}

	cmd.AddCommand(newCaptainWakeSetCommand())
	cmd.AddCommand(newCaptainWakeShowCommand())
	cmd.AddCommand(newCaptainWatchCommand())

	return cmd
}

func newCaptainWakeSetCommand() *cobra.Command {
	var nextWakeAt string
	var creditsAbove int
	var creditsBelow int
	var interruptTypes string

	cmd := &cobra.Command{
		Use:   "set [--next-wake-at +3h|<RFC3339>] [--credits-above N] [--credits-below N] [--interrupt-types a,b,c]",
		Short: "Declare the captain's wake policy (replaces any previously declared policy)",
		Long: `Declare when the supervisor should wake the captain outside its default
heartbeat cadence (spec: sp-sk68 wake model).

Each invocation fully replaces the previously declared policy: flags omitted
from this call are NOT carried over from a prior "wake set" call. Declare
every override you want active in a single invocation.

--next-wake-at accepts either a relative duration ("+3h", "+30m") applied
from the moment this command runs, or an absolute RFC3339 timestamp. It is
always capped at the supervisor's never-wake safety ceiling
(MaxWakeIntervalMinutes past the last session), so it can delay a wake but
can never suppress one indefinitely.

--interrupt-types REPLACES (not extends) the default set of event types that
force an immediate wake.

The declaration takes effect on the very next supervisor poll — no restart
required.

Examples:
  spacetraders captain wake set --next-wake-at +3h
  spacetraders captain wake set --next-wake-at 2026-07-06T18:00:00Z
  spacetraders captain wake set --credits-above 500000
  spacetraders captain wake set --credits-below 10000
  spacetraders captain wake set --interrupt-types workflow.failed,container.crashed`,
		RunE: func(cmd *cobra.Command, args []string) error {
			now := time.Now()
			var policy watchkeeper.WakePolicy

			if cmd.Flags().Changed("next-wake-at") {
				t, err := parseNextWakeAt(nextWakeAt, now)
				if err != nil {
					return err
				}
				policy.NextWakeAt = &t
			}
			if cmd.Flags().Changed("credits-above") {
				v := creditsAbove
				policy.CreditsAbove = &v
			}
			if cmd.Flags().Changed("credits-below") {
				v := creditsBelow
				policy.CreditsBelow = &v
			}
			if cmd.Flags().Changed("interrupt-types") {
				policy.InterruptTypes = parseInterruptTypes(interruptTypes)
			}

			store, err := newCaptainWakePolicyStore()
			if err != nil {
				return err
			}
			if err := runWakeSet(store, now, policy); err != nil {
				return fmt.Errorf("failed to save wake policy: %w", err)
			}
			fmt.Println("Wake policy updated.")
			return nil
		},
	}

	cmd.Flags().StringVar(&nextWakeAt, "next-wake-at", "", `Next wake time: relative ("+3h") or an RFC3339 absolute timestamp`)
	cmd.Flags().IntVar(&creditsAbove, "credits-above", 0, "Force a wake once credits rise to or above this amount")
	cmd.Flags().IntVar(&creditsBelow, "credits-below", 0, "Force a wake once credits fall to or below this amount")
	cmd.Flags().StringVar(&interruptTypes, "interrupt-types", "", "Comma-separated event types that force an immediate wake (replaces the default set)")

	return cmd
}

func newCaptainWakeShowCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show the captain's currently declared wake policy",
		Long: `Show the wake policy currently declared via "captain wake set", or the
defaults if none has been declared.

Examples:
  spacetraders captain wake show`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := newCaptainWakePolicyStore()
			if err != nil {
				return err
			}
			return runWakeShow(store, os.Stdout)
		},
	}

	return cmd
}
