package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// newWorkflowTradeRouteCommand creates the workflow trade-route subcommand.
//
// This is a THIN CLIENT: it asks the daemon to start a trade-route
// CONTAINER, rather than running the circuit in-process. The container runs the same
// disciplined pure-arbitrage circuit (buy at the exporter, sell at the importer, in
// tranches of at most 18 units per visit, only while the destination bid clears
// basis+1000), but under the daemon's proven container lifecycle:
//
//   - single-writer safety: the container claims its hull through the normal lifecycle,
//     so it cannot dual-write ship state with the daemon;
//   - claim-release-on-death: the hull is force-released on completion, crash, or daemon
//     shutdown — never stranded;
//   - RouteExecutor-backed navigation: each leg orbits → refuels → NavigateDirect →
//     waits on arrival events, with no re-claiming child navigate container;
//   - restart recovery: the container is RUNNING and rebuildable from its launch config,
//     so a daemon restart mid-circuit resumes it or cleanly releases the hull.
//
// Run this on a genuinely idle hull; the daemon refuses a hull it is actively flying.
func newWorkflowTradeRouteCommand() *cobra.Command {
	var (
		shipSymbol   string
		systemSymbol string
		maxVisits    int
		destWaypoint string
	)

	cmd := &cobra.Command{
		Use:   "trade-route",
		Short: "Fly one idle hull through the top-ranked arbitrage circuit (as a daemon container)",
		Long: `Ask the daemon to fly a single idle hull through the top-ranked pure-arbitrage
circuit in a system, as a recovery-safe daemon container under trade-analyst
discipline: buy at the exporter, sell at the importer, in tranches of at most 18
units per visit, and keep looping only while the destination bid clears basis+1000
(the acquisition cost plus the bid-floor). The circuit stops the moment the margin
dies and the hull is released back to idle.

This complements the mfg coordinator, which only trades its own fabrication
targets: trade-route exploits the standing buy-export/sell-import spreads nobody
else works, using idle-gap hulls (a contract-pool hauler between contracts, a
factory hauler between tasks) as free capacity.

Execution model: the circuit runs INSIDE the daemon as a container (single-writer,
claim-release-on-death, RouteExecutor-backed navigation, restart-safe). This command
only starts it and returns the container id; follow it with 'container logs'. The
daemon must be running. Run this only on a genuinely idle hull — the daemon refuses a
hull it is actively flying.

Examples:
  spacetraders workflow trade-route --ship ENDURANCE-7 --system X1-GZ7 --agent ENDURANCE
  spacetraders workflow trade-route --ship ENDURANCE-7 --system X1-GZ7 --max-visits 20 --player-id 1
  spacetraders workflow trade-route --ship ENDURANCE-7 --system X1-GZ7 --dest X1-ABC-STATION --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}
			if systemSymbol == "" {
				return fmt.Errorf("--system flag is required")
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

			// nil (unset) uses the daemon's default of 50 visits.
			var maxVisitsArg *int32
			if maxVisits != 0 {
				mv := int32(maxVisits)
				maxVisitsArg = &mv
			}

			// --dest pins the circuit to a destination waypoint or system instead of the
			// ranker's auto-selected lane; nil (flag unset) preserves auto-scan behavior.
			var destWaypointArg *string
			if destWaypoint != "" {
				destWaypointArg = &destWaypoint
			}

			result, err := client.StartTradeRoute(ctx, shipSymbol, systemSymbol, playerIdent.PlayerID, &playerIdent.AgentSymbol, maxVisitsArg, destWaypointArg)
			if err != nil {
				return fmt.Errorf("failed to start trade-route: %w", err)
			}

			fmt.Println("✓ Trade-route circuit started")
			fmt.Printf("  Container ID:  %s\n", result.ContainerID)
			fmt.Printf("  Ship:          %s\n", result.ShipSymbol)
			fmt.Printf("  System:        %s\n", result.SystemSymbol)
			fmt.Printf("  Status:        %s\n", result.Status)
			if result.Message != "" {
				fmt.Printf("  Message:       %s\n", result.Message)
			}
			fmt.Println("\n  The circuit runs as a daemon container (single-writer, claim-release-on-death, restart-safe).")
			fmt.Printf("  Use 'spacetraders container logs %s' to follow it.\n", result.ContainerID)
			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Idle hull to fly the circuit (required)")
	cmd.Flags().StringVar(&systemSymbol, "system", "", "System to scan for arbitrage lanes (required)")
	cmd.Flags().IntVar(&maxVisits, "max-visits", 0, "The RUN's total visit budget across every lane it commits to (0 = default 50); the run only stops early on a margin/starvation/error exit, and always at a leg boundary with the hold empty (sp-1hj5)")
	cmd.Flags().StringVar(&destWaypoint, "dest", "", "Pin the circuit to this destination waypoint or system, instead of auto-selecting a lane (waives the cross-system gate penalty for the targeted lane only)")

	return cmd
}
