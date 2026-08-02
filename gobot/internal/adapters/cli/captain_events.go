package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
)

// eventStore is the subset of captain.EventStore the events CLI needs.
type eventStore interface {
	FindUnprocessed(ctx context.Context, playerID, limit int) ([]*captain.Event, error)
	MarkProcessed(ctx context.Context, ids []int64, at time.Time) error
}

func newCaptainEventStore() (eventStore, error) {
	db, err := openDatabase()
	if err != nil {
		return nil, err
	}

	return persistence.NewGormCaptainEventRepository(db), nil
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
// selected by matches. Batch ack via --all/--before means a large wake
// backlog doesn't need a hand-built --ids CSV. No matches is a no-op,
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
// listing their unprocessed events, matching the fallback chain "captain
// report" and other captain-aware commands honor.
func runEventsListResolved(ctx context.Context, store eventStore, playerRepo player.PlayerRepository, jsonOut bool) error {
	resolved, err := resolveDefaultPlayer(ctx, playerRepo)
	if err != nil {
		return err
	}
	return runEventsList(ctx, store, resolved.ID.Value(), jsonOut)
}

func newCaptainEventsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "events",
		Short: "List and acknowledge captain events",
		Long: `Inspect and drain the strategic-event queue the autonomous captain reads
during its wake ritual. "events list" shows the unprocessed events queued for
a player; "events ack" marks them processed — by explicit IDs, or in bulk with
--all/--before — so they do not resurface on the next wake.

Player is resolved from --player-id, --agent, or the persisted default (in
that order), the same fallback chain the rest of the CLI uses.

Examples:
  spacetraders captain events list --agent TORWIND
  spacetraders captain events ack --agent TORWIND --all`,
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
