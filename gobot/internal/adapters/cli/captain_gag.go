package cli

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	watchkeeper "github.com/andrescamacho/spacetraders-go/internal/captain"
)

// gagPolicyStore is the subset of gag persistence the CLI needs. It
// exists so runGagSet/runGagShow can be tested with a fake, without touching the
// filesystem — mirroring wakePolicyStore.
type gagPolicyStore interface {
	Load() (watchkeeper.GagPolicy, error)
	Save(policy watchkeeper.GagPolicy) error
}

// fileGagPolicyStore is the production gagPolicyStore, backed by the supervisor's
// on-disk state file (the same file the supervisor re-reads every Tick — see
// internal/captain/state.go LoadGagPolicy).
type fileGagPolicyStore struct {
	path string
}

func (f fileGagPolicyStore) Load() (watchkeeper.GagPolicy, error) {
	return watchkeeper.LoadGagPolicy(f.path)
}

func (f fileGagPolicyStore) Save(policy watchkeeper.GagPolicy) error {
	return watchkeeper.SaveGagPolicy(f.path, policy)
}

func newCaptainGagPolicyStore() (gagPolicyStore, error) {
	path, err := captainWakeStatePath()
	if err != nil {
		return nil, err
	}
	return fileGagPolicyStore{path: path}, nil
}

// runGagSet writes the gag switch, stamping the toggle time. A gag-on carries an
// optional operator reason (surfaced in the supervisor's edge log + audit
// event); gag-off clears the switch outright.
func runGagSet(store gagPolicyStore, now time.Time, gagged bool, reason string) error {
	return store.Save(watchkeeper.GagPolicy{
		Gagged:        gagged,
		GagReason:     reason,
		GagDeclaredAt: now,
	})
}

// runGagShow renders the current gag switch to w.
func runGagShow(store gagPolicyStore, w io.Writer) error {
	policy, err := store.Load()
	if err != nil {
		return fmt.Errorf("failed to load gag policy: %w", err)
	}
	state := "off"
	if policy.Gagged {
		state = "on"
	}
	fmt.Fprintf(w, "gagged:      %s\n", state)
	reason := policy.GagReason
	if reason == "" {
		reason = "(none)"
	}
	fmt.Fprintf(w, "reason:      %s\n", reason)
	if policy.GagDeclaredAt.IsZero() {
		fmt.Fprintf(w, "declared_at: (never declared)\n")
	} else {
		fmt.Fprintf(w, "declared_at: %s\n", policy.GagDeclaredAt.Format(time.RFC3339))
	}
	return nil
}

func newCaptainGagCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gag",
		Short: "Stand the running supervisor down (soft, dynamic) without a restart",
		Long: `Dynamically pause the running watchkeeper supervisor's wake-eval loop
without stopping or restarting the process (sp-q9s7).

While GAGGED the supervisor keeps its process, liveness, heartbeat, and the
universe-reset safety rail, but stands down from ALL wake-eval actions: it
spawns no captain session and takes no corrective action. Clearing the gag
resumes normal operation on the very next poll. The switch is re-read live at
the top of every supervisor tick, so "gag on"/"gag off" takes effect within one
poll — no restart required.

This is the SOFT complement to the captain/DISABLED hard halt: DISABLED is a
sentinel file (also written by the universe-reset detector, cleared by the
Admiral alone) that halts the tick before anything runs; the gag is a live
config value toggled freely here and never touches DISABLED.

Examples:
  spacetraders captain gag on --reason "deploy freeze"
  spacetraders captain gag off
  spacetraders captain gag status`,
	}

	cmd.AddCommand(newCaptainGagOnCommand())
	cmd.AddCommand(newCaptainGagOffCommand())
	cmd.AddCommand(newCaptainGagStatusCommand())

	return cmd
}

func newCaptainGagOnCommand() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "on [--reason \"...\"]",
		Short: "Gag the supervisor: stand down from wake-eval, keep the process live",
		Long: `Gag the running supervisor. It keeps running and heartbeating but spawns no
captain session and takes no corrective action until "gag off". Takes effect on
the supervisor's next poll — no restart.

Examples:
  spacetraders captain gag on
  spacetraders captain gag on --reason "deploy freeze"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := newCaptainGagPolicyStore()
			if err != nil {
				return err
			}
			if err := runGagSet(store, time.Now(), true, reason); err != nil {
				return fmt.Errorf("failed to save gag policy: %w", err)
			}
			fmt.Println("Supervisor gagged — it will stand down on its next poll (still running).")
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "Operator reason, surfaced in the supervisor's gag log and audit event")
	return cmd
}

func newCaptainGagOffCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "off",
		Short: "Ungag the supervisor: resume normal wake-eval",
		Long: `Clear the gag. The supervisor resumes normal wake-eval on its next poll — no
restart.

Examples:
  spacetraders captain gag off`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := newCaptainGagPolicyStore()
			if err != nil {
				return err
			}
			if err := runGagSet(store, time.Now(), false, ""); err != nil {
				return fmt.Errorf("failed to save gag policy: %w", err)
			}
			fmt.Println("Supervisor ungagged — normal wake-eval resumes on its next poll.")
			return nil
		},
	}
	return cmd
}

func newCaptainGagStatusCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show whether the supervisor is currently gagged",
		Long: `Show the current gag switch: on/off, the operator reason, and when it was
toggled.

Examples:
  spacetraders captain gag status`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := newCaptainGagPolicyStore()
			if err != nil {
				return err
			}
			return runGagShow(store, os.Stdout)
		},
	}
	return cmd
}
