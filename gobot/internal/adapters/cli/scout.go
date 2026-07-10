package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// NewScoutCommand builds the `scout` verb family for the standing scout-post
// system (sp-cxpq): `scout start` launches the coordinator; `scout posts
// add/list/remove` edit the desired-state posts table the coordinator keeps
// manned. Post state lives in the daemon (RULINGS #3) — these verbs reach it
// only through the RPC, never a config file.
func NewScoutCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scout",
		Short: "Standing scout posts: keep systems' market data fresh",
		Long: `Manage the standing scout-post coordinator (sp-cxpq).

A scout post is a desired-state assignment — "keep this system's markets scanned"
— that the coordinator reconciles every tick: it claims an idle satellite for
each unmanned post, respawns any tour that dies, and retires sweep-once posts
after one pass. Posts and their hull assignments survive daemon restarts.`,
	}

	cmd.AddCommand(newScoutStartCommand())
	cmd.AddCommand(newScoutPostsCommand())
	return cmd
}

func newScoutStartCommand() *cobra.Command {
	var tickInterval time.Duration

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the standing scout-post coordinator",
		Long: `Start the scout-post coordinator for a player. It reconciles the posts
table every tick — manning unmanned posts with idle satellites, respawning dead
tours, retiring completed sweep-once posts — and re-adopts its posts and
assignments after a daemon restart.

Examples:
  spacetraders scout start --agent ENDURANCE
  spacetraders scout start --player-id 1 --tick 30s`,
		RunE: func(cmd *cobra.Command, args []string) error {
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			containerID, err := client.ScoutPostCoordinator(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol, int(tickInterval.Seconds()))
			if err != nil {
				return fmt.Errorf("scout post coordinator failed: %w", err)
			}

			fmt.Println("✓ Scout post coordinator started")
			fmt.Printf("  Container ID: %s\n", containerID)
			fmt.Printf("  Agent:        %s (player %d)\n", playerIdent.AgentSymbol, playerIdent.PlayerID)
			fmt.Println("\n  Add posts with 'spacetraders scout posts add <SYSTEM>'.")
			fmt.Println("  Stop with 'spacetraders container stop " + containerID + "'.")
			return nil
		},
	}

	cmd.Flags().DurationVar(&tickInterval, "tick", 0, "Reconcile cadence (e.g. 30s); 0 uses the coordinator default")
	return cmd
}

func newScoutPostsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "posts",
		Short: "Manage desired-state scout posts",
	}
	cmd.AddCommand(newScoutPostsAddCommand())
	cmd.AddCommand(newScoutPostsListCommand())
	cmd.AddCommand(newScoutPostsRemoveCommand())
	return cmd
}

func newScoutPostsAddCommand() *cobra.Command {
	var (
		freshness time.Duration
		kind      string
	)

	cmd := &cobra.Command{
		Use:   "add <SYSTEM>",
		Short: "Add or update a scout post for a system",
		Long: `Add (or update) a desired-state scout post for a system. The coordinator
mans it with the nearest idle satellite on its next tick. Re-adding an existing
post updates its freshness/kind without evicting the hull already manning it.

--kind standing (default) keeps the system fresh forever; --kind sweep-once runs
a single tour then auto-removes the post, freeing its hull for the next one — the
shape the captain seeds frontier-census systems with.

Examples:
  spacetraders scout posts add X1-GZ7 --agent ENDURANCE
  spacetraders scout posts add X1-JP61 --freshness 45m --agent ENDURANCE
  spacetraders scout posts add X1-KA42 --kind sweep-once --agent ENDURANCE`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			systemSymbol := args[0]

			postKind, err := normalizePostKind(kind)
			if err != nil {
				return err
			}

			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			post, err := client.AddScoutPost(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol, systemSymbol, int(freshness.Seconds()), postKind)
			if err != nil {
				return fmt.Errorf("failed to add scout post: %w", err)
			}

			fmt.Printf("✓ Scout post added: %s (%s, freshness %s)\n", post.SystemSymbol, post.Kind, formatSeconds(post.FreshnessSeconds))
			if post.AssignedHull != "" {
				fmt.Printf("  Currently manned by %s\n", post.AssignedHull)
			} else {
				fmt.Println("  Unmanned — the coordinator will claim an idle satellite next tick.")
			}
			return nil
		},
	}

	cmd.Flags().DurationVar(&freshness, "freshness", 60*time.Minute, "Target market-scan freshness (e.g. 60m)")
	cmd.Flags().StringVar(&kind, "kind", "standing", "Post kind: standing or sweep-once")
	return cmd
}

func newScoutPostsListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List active scout posts",
		RunE: func(cmd *cobra.Command, args []string) error {
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			posts, err := client.ListScoutPosts(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("failed to list scout posts: %w", err)
			}

			if len(posts) == 0 {
				fmt.Println("No scout posts configured.")
				return nil
			}

			fmt.Printf("%-16s  %-11s  %-10s  %s\n", "SYSTEM", "KIND", "FRESHNESS", "MANNED BY")
			for _, p := range posts {
				manned := p.AssignedHull
				if manned == "" {
					manned = "(unmanned)"
				}
				fmt.Printf("%-16s  %-11s  %-10s  %s\n", p.SystemSymbol, p.Kind, formatSeconds(p.FreshnessSeconds), manned)
			}
			return nil
		},
	}
	return cmd
}

func newScoutPostsRemoveCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <SYSTEM>",
		Short: "Remove a scout post and release its hull",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			systemSymbol := args[0]

			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			if err := client.RemoveScoutPost(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol, systemSymbol); err != nil {
				return fmt.Errorf("failed to remove scout post: %w", err)
			}

			fmt.Printf("✓ Scout post removed: %s\n", systemSymbol)
			return nil
		},
	}
	return cmd
}

// normalizePostKind maps the CLI --kind flag to the wire kind string, accepting
// the hyphenated form operators type.
func normalizePostKind(kind string) (string, error) {
	switch kind {
	case "", "standing":
		return "standing", nil
	case "sweep-once", "sweep_once":
		return "sweep_once", nil
	default:
		return "", fmt.Errorf("invalid --kind %q (want standing or sweep-once)", kind)
	}
}

// formatSeconds renders a freshness target in a compact human form.
func formatSeconds(seconds int) string {
	return (time.Duration(seconds) * time.Second).String()
}
