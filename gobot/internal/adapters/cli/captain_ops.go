package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	watchkeeper "github.com/andrescamacho/spacetraders-go/internal/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// eventStore is the subset of captain.EventStore the events CLI needs.
type eventStore interface {
	FindUnprocessed(ctx context.Context, playerID, limit int) ([]*captain.Event, error)
	MarkProcessed(ctx context.Context, ids []int64, at time.Time) error
}

// runEventsAck parses the CSV of event IDs (all-or-nothing) and marks them
// processed. Any malformed token aborts before any write.
func runEventsAck(ctx context.Context, store eventStore, csv string) error {
	tokens := strings.Split(csv, ",")
	ids := make([]int64, 0, len(tokens))
	for _, token := range tokens {
		trimmed := strings.TrimSpace(token)
		id, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid event id %q: %w", trimmed, err)
		}
		ids = append(ids, id)
	}
	return store.MarkProcessed(ctx, ids, time.Now())
}

// runEventsAckMatching acks the subset of playerID's unprocessed events
// selected by matches (sp-yr3f: batch ack via --all/--before, so a large
// wake backlog doesn't need a hand-built --ids CSV). No matches is a no-op,
// not an error — acking an already-clear backlog is harmless.
func runEventsAckMatching(ctx context.Context, store eventStore, playerID int, matches func(*captain.Event) bool) error {
	events, err := store.FindUnprocessed(ctx, playerID, 0)
	if err != nil {
		return fmt.Errorf("failed to list events: %w", err)
	}
	ids := make([]int64, 0, len(events))
	for _, e := range events {
		if matches(e) {
			ids = append(ids, e.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return store.MarkProcessed(ctx, ids, time.Now())
}

// runEventsAckAll marks every unprocessed event for playerID as processed.
func runEventsAckAll(ctx context.Context, store eventStore, playerID int) error {
	return runEventsAckMatching(ctx, store, playerID, func(*captain.Event) bool { return true })
}

// runEventsAckBefore marks unprocessed events for playerID created before
// cutoff as processed, leaving newer pending events untouched.
func runEventsAckBefore(ctx context.Context, store eventStore, playerID int, cutoff time.Time) error {
	return runEventsAckMatching(ctx, store, playerID, func(e *captain.Event) bool {
		return e.CreatedAt.Before(cutoff)
	})
}

// runEventsList prints the unprocessed events for a player, as a table or JSON.
func runEventsList(ctx context.Context, store eventStore, playerID int, jsonOut bool) error {
	events, err := store.FindUnprocessed(ctx, playerID, 0)
	if err != nil {
		return fmt.Errorf("failed to list events: %w", err)
	}

	if jsonOut {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(events)
	}

	if len(events) == 0 {
		fmt.Println("No unprocessed events.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tTYPE\tSHIP\tCREATED_AT")
	for _, e := range events {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", e.ID, e.Type, e.Ship, e.CreatedAt.Format(time.RFC3339))
	}
	return w.Flush()
}

// runEventsListResolved resolves the effective player — --player-id,
// --agent, or the persisted default, via the shared resolver — before
// listing their unprocessed events. Replaces a hard "--player-id is
// required" error with the same fallback chain "captain report" and other
// captain-aware commands already honor (sp-yr3f).
func runEventsListResolved(ctx context.Context, store eventStore, playerRepo player.PlayerRepository, jsonOut bool) error {
	resolved, err := resolveDefaultPlayer(ctx, playerRepo)
	if err != nil {
		return err
	}
	return runEventsList(ctx, store, resolved.ID.Value(), jsonOut)
}

// wakePolicyStore is the subset of captain wake-policy persistence the CLI
// needs (spec: sp-sk68 wake model). It exists so runWakeSet/runWakeShow can
// be tested with a fake, without touching the filesystem.
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

func newCaptainEventStore() (eventStore, error) {
	cfg, err := config.LoadConfig("")
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	db, err := database.NewConnection(&cfg.Database)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	return persistence.NewGormCaptainEventRepository(db), nil
}

// newCaptainPlayerRepo connects to the database and returns a player
// repository, so captain events/report commands can resolve --player-id/
// --agent (sp-yr3f) via the shared resolveDefaultPlayer helper instead of
// hard-requiring --player-id. It opens its own connection independent of
// newCaptainEventStore/newReportEventSource, matching this package's
// established one-connection-per-factory convention (see ledger.go).
func newCaptainPlayerRepo() (player.PlayerRepository, error) {
	cfg, err := config.LoadConfig("")
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	db, err := database.NewConnection(&cfg.Database)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	return persistence.NewGormPlayerRepository(db), nil
}

// NewCaptainCommand creates the captain command with subcommands.
func NewCaptainCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "captain",
		Short: "Autonomous captain operations",
		Long: `Inspect and acknowledge the strategic-event queue the autonomous
captain consumes during its wake ritual.

Player is resolved the same way everywhere: --player-id, or --agent (which
survives across era resets, unlike --player-id), or the persisted default.

Examples:
  spacetraders captain events list --player-id 1
  spacetraders captain events list --agent TORWIND --json
  spacetraders captain events ack --player-id 1 --ids 12,13,14
  spacetraders captain events ack --agent TORWIND --all`,
	}

	cmd.AddCommand(newCaptainEventsCommand())
	cmd.AddCommand(newCaptainReportCommand())
	cmd.AddCommand(newCaptainTokensCommand())
	cmd.AddCommand(newCaptainWakeCommand())
	cmd.AddCommand(newCaptainRegimeCommand())

	return cmd
}

func newCaptainWakeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wake",
		Short: "Inspect or declare the captain's wake policy",
	}

	cmd.AddCommand(newCaptainWakeSetCommand())
	cmd.AddCommand(newCaptainWakeShowCommand())

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

func newCaptainEventsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "events",
		Short: "List and acknowledge captain events",
	}

	cmd.AddCommand(newCaptainEventsListCommand())
	cmd.AddCommand(newCaptainEventsAckCommand())

	return cmd
}

func newCaptainEventsListCommand() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List unprocessed captain events for a player",
		Long: `List the unprocessed strategic events queued for the captain.

Player is resolved from --player-id, --agent, or the persisted default (in
that order) — the same fallback chain "player info" and "ledger" use.

Examples:
  spacetraders captain events list --player-id 1
  spacetraders captain events list --agent TORWIND
  spacetraders captain events list --agent TORWIND --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := newCaptainEventStore()
			if err != nil {
				return err
			}
			playerRepo, err := newCaptainPlayerRepo()
			if err != nil {
				return err
			}

			return runEventsListResolved(context.Background(), store, playerRepo, jsonOut)
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")

	return cmd
}

func newCaptainEventsAckCommand() *cobra.Command {
	var ids string
	var all bool
	var before string

	cmd := &cobra.Command{
		Use:   "ack",
		Short: "Acknowledge captain events by ID, or in bulk with --all/--before",
		Long: `Mark captain events processed, either by explicit IDs or in bulk.

Exactly one of --ids, --all, or --before is required. --all and --before
resolve the player from --player-id, --agent, or the persisted default (in
that order), same as "captain events list".

Examples:
  spacetraders captain events ack --player-id 1 --ids 12,13,14
  spacetraders captain events ack --agent TORWIND --all
  spacetraders captain events ack --agent TORWIND --before 2026-07-08T00:00:00Z`,
		RunE: func(cmd *cobra.Command, args []string) error {
			modes := 0
			if ids != "" {
				modes++
			}
			if all {
				modes++
			}
			if before != "" {
				modes++
			}
			if modes == 0 {
				return fmt.Errorf("one of --ids, --all, or --before is required")
			}
			if modes > 1 {
				return fmt.Errorf("--ids, --all, and --before are mutually exclusive")
			}

			store, err := newCaptainEventStore()
			if err != nil {
				return err
			}
			ctx := context.Background()

			if ids != "" {
				return runEventsAck(ctx, store, ids)
			}

			playerRepo, err := newCaptainPlayerRepo()
			if err != nil {
				return err
			}
			resolved, err := resolveDefaultPlayer(ctx, playerRepo)
			if err != nil {
				return err
			}

			if all {
				return runEventsAckAll(ctx, store, resolved.ID.Value())
			}

			cutoff, err := time.Parse(time.RFC3339, before)
			if err != nil {
				return fmt.Errorf("--before: %q must be an RFC3339 timestamp: %w", before, err)
			}
			return runEventsAckBefore(ctx, store, resolved.ID.Value(), cutoff)
		},
	}

	cmd.Flags().StringVar(&ids, "ids", "", "Comma-separated event IDs to acknowledge")
	cmd.Flags().BoolVar(&all, "all", false, "Acknowledge every pending event for the resolved player")
	cmd.Flags().StringVar(&before, "before", "", "Acknowledge pending events created before this RFC3339 timestamp")

	return cmd
}
