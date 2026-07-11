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
		Long: `Manage the desired-state scout posts the standing scout coordinator
reconciles (spec: sp-cxpq). A post is a per-system "keep these markets scanned"
assignment; the coordinator mans each post with an idle satellite, respawns
tours that die, and retires sweep-once posts after one pass.

"posts add" declares or updates a post (freshness target, standing vs
sweep-once, probe budget); "posts list" shows every post and how many of its
hull slots are currently manned; "posts remove" deletes a post and releases
its hull. Posts and their assignments survive daemon restarts.

Examples:
  spacetraders scout posts add X1-GZ7 --agent ENDURANCE
  spacetraders scout posts list --agent ENDURANCE
  spacetraders scout posts remove X1-GZ7 --agent ENDURANCE`,
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
		hulls     int
	)

	cmd := &cobra.Command{
		Use:   "add <SYSTEM>",
		Short: "Add or update a scout post for a system",
		Long: `Add (or update) a desired-state scout post for a system. The coordinator
mans it with the nearest idle satellite on its next tick. Re-adding an existing
post updates its freshness/kind/hulls without evicting the hulls already manning it.

--kind standing (default) keeps the system fresh forever; --kind sweep-once runs
a single tour then auto-removes the post, freeing its hull for the next one — the
shape the captain seeds frontier-census systems with.

--hulls N (default 1) deploys N probes on DISJOINT tours: the system's markets are
partitioned into N per-probe circuits via the routing VRP, so freshness per market
improves ~N× at the SAME per-probe API rate (more probes = smaller partitions =
fresher data, not more API calls). Only standing posts partition; sweep-once is
always single-hull. Changing N re-partitions on the next reconcile tick.

Examples:
  spacetraders scout posts add X1-GZ7 --agent ENDURANCE
  spacetraders scout posts add X1-JP61 --freshness 45m --agent ENDURANCE
  spacetraders scout posts add X1-KA42 --hulls 3 --freshness 30m --agent ENDURANCE
  spacetraders scout posts add X1-KA42 --kind sweep-once --agent ENDURANCE`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			systemSymbol := args[0]

			postKind, err := normalizePostKind(kind)
			if err != nil {
				return err
			}
			if hulls < 1 {
				return fmt.Errorf("invalid --hulls %d (want a positive probe budget)", hulls)
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

			post, err := client.AddScoutPost(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol, systemSymbol, int(freshness.Seconds()), postKind, hulls)
			if err != nil {
				return fmt.Errorf("failed to add scout post: %w", err)
			}

			fmt.Printf("✓ Scout post added: %s (%s, freshness %s, %d hull(s))\n", post.SystemSymbol, post.Kind, formatSeconds(post.FreshnessSeconds), post.Hulls)
			if post.MannedCount > 0 {
				fmt.Printf("  Currently manned: %d/%d slot(s)\n", post.MannedCount, post.Hulls)
			} else {
				fmt.Println("  Unmanned — the coordinator will claim idle satellites next tick.")
			}
			return nil
		},
	}

	cmd.Flags().DurationVar(&freshness, "freshness", 60*time.Minute, "Target market-scan freshness (e.g. 60m)")
	cmd.Flags().StringVar(&kind, "kind", "standing", "Post kind: standing or sweep-once")
	cmd.Flags().IntVar(&hulls, "hulls", 1, "Probe budget N: deploy N probes on disjoint partitioned tours (standing posts only)")
	return cmd
}

func newScoutPostsListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List active scout posts",
		Long: `List the scout posts declared for a player, one row per post: system symbol,
kind (standing or sweep-once), target scan freshness, configured probe/hull
budget, and how many of those hull slots the coordinator currently has manned
("(unmanned)" when none). Prints "No scout posts configured." when the player
has none.

Player is resolved from --player-id, --agent, or the persisted default. Reads
live daemon state, so the daemon must be running.

Examples:
  spacetraders scout posts list --agent ENDURANCE`,
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

			fmt.Printf("%-16s  %-11s  %-10s  %-6s  %s\n", "SYSTEM", "KIND", "FRESHNESS", "HULLS", "MANNED")
			for _, p := range posts {
				hulls := p.Hulls
				if hulls < 1 {
					hulls = 1
				}
				manned := fmt.Sprintf("%d/%d", p.MannedCount, hulls)
				if p.MannedCount == 0 {
					manned = "(unmanned)"
				}
				fmt.Printf("%-16s  %-11s  %-10s  %-6d  %s\n", p.SystemSymbol, p.Kind, formatSeconds(p.FreshnessSeconds), hulls, manned)
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
		Long: `Remove the scout post for a system and release the satellite(s) manning it
back to the idle pool for reassignment. Takes the system symbol (as shown in
the SYSTEM column of "scout posts list") as its sole argument.

Player is resolved from --player-id, --agent, or the persisted default. Reads
and mutates live daemon state, so the daemon must be running.

Examples:
  spacetraders scout posts remove X1-GZ7 --agent ENDURANCE`,
		Args: cobra.ExactArgs(1),
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
