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

// NewCaptainCommand creates the captain command with subcommands.
func NewCaptainCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "captain",
		Short: "Autonomous captain operations",
		Long: `Inspect and acknowledge the strategic-event queue the autonomous
captain consumes during its wake ritual.

Examples:
  spacetraders captain events list --player-id 1
  spacetraders captain events list --player-id 1 --json
  spacetraders captain events ack --player-id 1 --ids 12,13,14`,
	}

	cmd.AddCommand(newCaptainEventsCommand())

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

Examples:
  spacetraders captain events list --player-id 1
  spacetraders captain events list --player-id 1 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if playerID <= 0 {
				return fmt.Errorf("--player-id flag is required")
			}

			store, err := newCaptainEventStore()
			if err != nil {
				return err
			}

			return runEventsList(context.Background(), store, playerID, jsonOut)
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")

	return cmd
}

func newCaptainEventsAckCommand() *cobra.Command {
	var ids string

	cmd := &cobra.Command{
		Use:   "ack",
		Short: "Acknowledge captain events by ID",
		Long: `Mark captain events processed by their IDs (comma-separated).

Examples:
  spacetraders captain events ack --player-id 1 --ids 12,13,14`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if ids == "" {
				return fmt.Errorf("--ids flag is required")
			}

			store, err := newCaptainEventStore()
			if err != nil {
				return err
			}

			return runEventsAck(context.Background(), store, ids)
		},
	}

	cmd.Flags().StringVar(&ids, "ids", "", "Comma-separated event IDs to acknowledge (required)")

	return cmd
}
