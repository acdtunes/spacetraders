package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
	"github.com/spf13/cobra"
)

// NewFrontierCommand builds the `frontier` verb family for the standing frontier
// expansion coordinator (sp-8w89): `frontier start` launches the coordinator that
// closes the manual expansion loop — measuring coverage demand, declaring frontier
// sweep-once posts, and buying probes under the money guards, while the scout-post
// reconciler does all movement and manning.
func NewFrontierCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "frontier",
		Short: "Standing frontier expansion: auto-buy probes and seed frontier scouts",
		Long: `Manage the standing frontier expansion coordinator (sp-8w89).

The coordinator closes the manual frontier loop: every tick it ranks the gate-
reachable, uncovered frontier (by known-market count, hop distance, and a virgin
bonus), declares the top system as a sweep-once scout post, and — when the probe
fleet is short of open coverage demand and every money guard passes (price <= 25%
of live treasury, fleet cap, spend cap, purchase cooldown) — buys one probe. The
bought probe lands undedicated in the pool; the scout-post reconciler and its jump
relays claim and move it. The coordinator itself moves and claims nothing, and it
re-derives every decision from persisted state, so it survives daemon restarts.`,
	}

	cmd.AddCommand(newFrontierStartCommand())
	cmd.AddCommand(newFrontierStatusCommand())
	return cmd
}

// newFrontierStatusCommand builds `frontier status` (sp-pvw3): one view of the running coordinator's
// live state — the effective discovery/scan split, discovery frontier depth, the honest dark-market
// backlog, probe allocation, the last probe buy, and the current fail-closed blockers.
func newFrontierStatusCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the frontier coordinator's live state (split, backlog, probes, blockers)",
		Long: `Show the running frontier expansion coordinator's live state in one view (sp-pvw3):
the effective discovery/scan split (and any graceful-degradation redirect), the
discovery frontier depth, the HONEST dark-market backlog (charted markets with no
or stale price data), probe allocation, the last probe buy, and the current
fail-closed blockers. Use --json for scripts.

Examples:
  spacetraders frontier status --agent ENDURANCE
  spacetraders frontier status --json`,
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

			playerID, agentSymbol := playerPointers(playerIdent)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			resp, err := client.GetFrontierStatus(ctx, playerID, agentSymbol)
			if err != nil {
				return fmt.Errorf("frontier status failed: %w", err)
			}
			out, err := formatFrontierStatus(resp, asJSON)
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Render the status as JSON for scripts")
	return cmd
}

// formatFrontierStatus renders the frontier status view — a compact operator table, or JSON with
// --json. Pure over the response so it is unit-testable without a daemon.
func formatFrontierStatus(resp *pb.GetFrontierStatusResponse, asJSON bool) (string, error) {
	if asJSON {
		encoded, err := json.MarshalIndent(resp, "", "  ")
		if err != nil {
			return "", fmt.Errorf("failed to render frontier status as JSON: %w", err)
		}
		return string(encoded) + "\n", nil
	}

	lastBuy := "none recorded"
	if resp.LastBuyAgeSeconds >= 0 {
		lastBuy = fmt.Sprintf("%d cr, %s ago", resp.LastBuyPrice, (time.Duration(resp.LastBuyAgeSeconds) * time.Second).Round(time.Second))
	}
	blockers := "none"
	if len(resp.Blockers) > 0 {
		blockers = strings.Join(resp.Blockers, "; ")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Frontier expansion — %s\n", resp.ContainerId)
	fmt.Fprintf(&b, "  Split:      %s\n", resp.SplitSummary)
	fmt.Fprintf(&b, "  Discovery:  %d reachable virgin system(s) queued\n", resp.VirginQueueDepth)
	fmt.Fprintf(&b, "  Backlog:    %d dark-market system(s), %d unscanned marketplace(s)\n", resp.DarkSystems, resp.DarkMarketplaces)
	fmt.Fprintf(&b, "  Probes:     %d/%d satellites (%d idle), %d post(s) in flight\n", resp.ProbeFleet, resp.ProbeCap, resp.ProbesIdle, resp.PostsInFlight)
	fmt.Fprintf(&b, "  Last buy:   %s\n", lastBuy)
	fmt.Fprintf(&b, "  Blockers:   %s\n", blockers)
	return b.String(), nil
}

func newFrontierStartCommand() *cobra.Command {
	var (
		tickInterval     time.Duration
		dryRun           bool
		maxProbeFleet    int
		maxSpendPerCycle int
		purchaseCooldown time.Duration
		expansionMaxHops int
	)

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the standing frontier expansion coordinator",
		Long: `Start the frontier expansion coordinator for a player. Run it with --dry-run
first to watch a cycle's decisions (ranking, would-declare, would-buy) without
buying or declaring anything, then start it for real.

Examples:
  spacetraders frontier start --agent ENDURANCE --dry-run
  spacetraders frontier start --agent ENDURANCE
  spacetraders frontier start --player-id 1 --tick 60s --max-probe-fleet 40 --max-spend-per-cycle 100000`,
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

			containerID, err := client.FrontierExpansionCoordinator(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol, FrontierExpansionCoordinatorParams{
				TickIntervalSecs:     int(tickInterval.Seconds()),
				DryRun:               dryRun,
				MaxProbeFleet:        maxProbeFleet,
				MaxSpendPerCycle:     maxSpendPerCycle,
				PurchaseCooldownSecs: int(purchaseCooldown.Seconds()),
				ExpansionMaxHops:     expansionMaxHops,
			})
			if err != nil {
				return fmt.Errorf("frontier expansion coordinator failed: %w", err)
			}

			mode := "live"
			if dryRun {
				mode = "DRY-RUN (buys/declares nothing)"
			}
			fmt.Println("✓ Frontier expansion coordinator started")
			fmt.Printf("  Container ID: %s\n", containerID)
			fmt.Printf("  Agent:        %s (player %d)\n", playerIdent.AgentSymbol, playerIdent.PlayerID)
			fmt.Printf("  Mode:         %s\n", mode)
			fmt.Println("\n  Watch decisions with 'spacetraders container logs " + containerID + "'.")
			fmt.Println("  Stop with 'spacetraders container stop " + containerID + "'.")
			return nil
		},
	}

	cmd.Flags().DurationVar(&tickInterval, "tick", 0, "Reconcile cadence (e.g. 60s); 0 uses the coordinator default")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Log decisions without buying or declaring anything")
	cmd.Flags().IntVar(&maxProbeFleet, "max-probe-fleet", 0, "Total satellite cap; 0 uses the default (40)")
	cmd.Flags().IntVar(&maxSpendPerCycle, "max-spend-per-cycle", 0, "Max probe spend per trailing window; 0 uses the default (100000)")
	cmd.Flags().DurationVar(&purchaseCooldown, "purchase-cooldown", 0, "Min time between probe buys (e.g. 10m); 0 uses the default")
	cmd.Flags().IntVar(&expansionMaxHops, "expansion-max-hops", 0, "Gate-graph reach for the expansion queue; 0 uses the default (3)")
	return cmd
}
