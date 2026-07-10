package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	watchkeeper "github.com/andrescamacho/spacetraders-go/internal/captain"
)

// watchPolicyStore is the subset of one-shot wake-watch persistence the CLI
// needs (spec: sp-oyer). It exists so runWatchAdd/runWatchList/runWatchClear
// can be tested with a fake, without touching the filesystem.
type watchPolicyStore interface {
	Load() (watchkeeper.WatchPolicy, error)
	Save(policy watchkeeper.WatchPolicy) error
}

// fileWatchPolicyStore is the production watchPolicyStore, backed by the same
// supervisor state file the WakePolicy and RegimePolicy use (see
// internal/captain/state.go) — the wake watches are an independent owner of
// that one file, so a watch write never clobbers a wake-policy or regime
// write, or vice versa.
type fileWatchPolicyStore struct {
	path string
}

func (f fileWatchPolicyStore) Load() (watchkeeper.WatchPolicy, error) {
	return watchkeeper.LoadWatchPolicy(f.path)
}

func (f fileWatchPolicyStore) Save(policy watchkeeper.WatchPolicy) error {
	return watchkeeper.SaveWatchPolicy(f.path, policy)
}

// newCaptainWatchPolicyStore resolves the production store via the same
// state-path lookup the wake-policy and regime stores use
// (captainWakeStatePath, defined in captain_ops.go).
func newCaptainWatchPolicyStore() (watchPolicyStore, error) {
	path, err := captainWakeStatePath()
	if err != nil {
		return nil, err
	}
	return fileWatchPolicyStore{path: path}, nil
}

// parseWatchDeadline parses the --by flag value, accepting either a relative
// duration applied to now ("+20m") or an absolute RFC3339 timestamp — the same
// dual-mode idiom as parseNextWakeAt, with --by-specific error text.
func parseWatchDeadline(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("--by: empty value")
	}
	if strings.HasPrefix(s, "+") {
		d, err := time.ParseDuration(s[1:])
		if err != nil {
			return time.Time{}, fmt.Errorf("--by: invalid relative duration %q: %w", s, err)
		}
		return now.Add(d), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("--by: %q must be a relative duration like \"+20m\" or an RFC3339 timestamp: %w", s, err)
	}
	return t, nil
}

// runWatchAdd arms a new one-shot watch. This is additive (like `regime set`,
// unlike `wake set`'s full-replace): multiple watches coexist independently, so
// a second `watch add` must not drop the first. When --by is omitted the
// deadline defaults to now+defaultDeadline so every evented wait is deadline'd
// at the sensing layer (sp-oyer design note) — a watch whose match event is
// lost still auto-fires and disarms rather than arming forever.
func runWatchAdd(store watchPolicyStore, now time.Time, spec, by string, defaultDeadline time.Duration) error {
	watch, err := watchkeeper.ParseWatchSpec(spec)
	if err != nil {
		return err
	}
	deadline := now.Add(defaultDeadline)
	if strings.TrimSpace(by) != "" {
		d, err := parseWatchDeadline(by, now)
		if err != nil {
			return err
		}
		deadline = d
	}
	watch.Deadline = deadline
	watch.ArmedAt = now

	policy, err := store.Load()
	if err != nil {
		return fmt.Errorf("failed to load watch policy: %w", err)
	}
	policy.Watches = append(policy.Watches, watch)
	return store.Save(policy)
}

// runWatchList renders every currently-armed watch to w.
func runWatchList(store watchPolicyStore, w io.Writer) error {
	policy, err := store.Load()
	if err != nil {
		return fmt.Errorf("failed to load watch policy: %w", err)
	}
	if len(policy.Watches) == 0 {
		_, err := fmt.Fprintln(w, "No wake watches armed.")
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SUBJECT\tID\tPREDICATE\tDEADLINE\tARMED_AT")
	for _, watch := range policy.Watches {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			watch.Subject, watch.ID, watch.Predicate,
			watch.Deadline.Format(time.RFC3339), watch.ArmedAt.Format(time.RFC3339))
	}
	return tw.Flush()
}

// runWatchClear disarms every watch.
func runWatchClear(store watchPolicyStore) error {
	return store.Save(watchkeeper.WatchPolicy{})
}

func newCaptainWatchCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Arm, list, or clear one-shot wake watches",
		Long: `Arm one-shot wake watches that fire a single captain wake the first time a
specific ship arrives or a specific container reaches a terminal state — or at
a deadline, whichever comes first — then auto-disarm (spec: sp-oyer).

Watches are operational ephemera, SEPARATE from the standing wake policy: they
have their own store, multiple coexist independently, and "captain wake set"'s
full-replace semantics never touch them (and vice versa).

A fired watch reaches the captain as an interrupt-class wake mail tagged
"matched" (the arrival/terminal event was seen) or "deadline-fired" (the
deadline passed first — e.g. the arrival event was lost), then clears itself.`,
	}

	cmd.AddCommand(newCaptainWatchAddCommand())
	cmd.AddCommand(newCaptainWatchListCommand())
	cmd.AddCommand(newCaptainWatchClearCommand())

	return cmd
}

func newCaptainWatchAddCommand() *cobra.Command {
	var by string
	var defaultDeadline time.Duration

	cmd := &cobra.Command{
		Use:   "add <ship:SYMBOL:arrival | container:ID:terminal> [--by +20m|<RFC3339>]",
		Short: "Arm a one-shot wake watch (adds to, does not replace, existing watches)",
		Long: `Arm a one-shot wake watch on a ship arrival or a container terminal state.

The target is "ship:<SYMBOL>:arrival" or "container:<ID>:terminal". The watch
fires a single wake the first time a matching event is seen OR at its deadline,
whichever comes first, then auto-disarms.

--by sets the deadline: a relative duration ("+20m") applied from now, or an
absolute RFC3339 timestamp. When omitted, the deadline defaults to
--default-deadline from now, so every watch is deadline'd — a watch whose
match event is lost still fires (tagged deadline-fired) and clears itself
rather than arming forever.

Each "watch add" ADDS a watch; run "captain wake watch clear" for a clean
slate. Multiple watches coexist and fire independently.

Examples:
  spacetraders captain wake watch add ship:TORWIND-E:arrival
  spacetraders captain wake watch add ship:TORWIND-E:arrival --by +20m
  spacetraders captain wake watch add container:c-9f2a:terminal --by 2026-07-10T18:00:00Z`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := newCaptainWatchPolicyStore()
			if err != nil {
				return err
			}
			if err := runWatchAdd(store, time.Now(), args[0], by, defaultDeadline); err != nil {
				return err
			}
			fmt.Println("Wake watch armed.")
			return nil
		},
	}

	cmd.Flags().StringVar(&by, "by", "", `Deadline: relative ("+20m") or an RFC3339 absolute timestamp (default: --default-deadline from now)`)
	cmd.Flags().DurationVar(&defaultDeadline, "default-deadline", watchkeeper.DefaultWatchDeadline,
		"Deadline applied when --by is omitted, measured from now")

	return cmd
}

func newCaptainWatchListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the currently-armed one-shot wake watches",
		Long: `List every one-shot wake watch currently armed via "captain wake watch add".

Examples:
  spacetraders captain wake watch list`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := newCaptainWatchPolicyStore()
			if err != nil {
				return err
			}
			return runWatchList(store, os.Stdout)
		},
	}
	return cmd
}

func newCaptainWatchClearCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Disarm all one-shot wake watches",
		Long: `Disarm every currently-armed one-shot wake watch. The standing wake policy
("captain wake set") is unaffected.

Examples:
  spacetraders captain wake watch clear`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := newCaptainWatchPolicyStore()
			if err != nil {
				return err
			}
			if err := runWatchClear(store); err != nil {
				return fmt.Errorf("failed to clear wake watches: %w", err)
			}
			fmt.Println("Wake watches cleared.")
			return nil
		},
	}
	return cmd
}
