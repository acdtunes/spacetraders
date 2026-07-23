package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// newWorkflowLongHaulCoordinatorCommand creates the workflow long-haul-coordinator subcommand:
// it arms the STANDING long-haul arb fleet coordinator (sp-mepj) — the out-of-horizon
// single-good arb engine that captures the exotic lanes the 1-gate-hop discovery horizon hides
// from BOTH the tour solver and the arb scanner.
//
// Like the trade-fleet / fleet-autosizer coordinators it is a THIN CLIENT: it asks the daemon to
// start one recovery-safe coordinator container and returns its id. The coordinator survives
// daemon restarts (it re-adopts from its persisted launch config) and the arm is idempotent — a
// re-run returns the live coordinator's id rather than spawning a rival.
//
// It is ARMED but INERT on arm: the engine touches ONLY hulls tagged `dedicated_fleet=long-haul`
// (populated by the operator with `fleet add --operation long-haul --ship X`), so an empty
// long-haul fleet yields zero launches. The operator's per-hull tag IS the on/off seam — there
// is no feature flag. The Admiral-authorized aggressive money envelope (~1M/haul, ~2M exposure)
// is fail-closed and fenced above the 200k contract cushion.
func newWorkflowLongHaulCoordinatorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "long-haul-coordinator",
		Short: "Arm the standing long-haul arb coordinator (captures out-of-horizon exotic lanes; inert until hulls are long-haul-tagged)",
		Long: `Arm the STANDING long-haul arb fleet coordinator for a player (sp-mepj) — the out-of-horizon
single-good arb engine. sp-mtvg proved the exotic 'blind spot' is a 1-gate-hop DISCOVERY horizon
shared by the tour solver and the arb scanner: source and sink of the richest exotic lanes sit 3+
gate hops apart, so neither optimizer ever sees both in one snapshot. This engine prices the REAL
multi-hop route and runs the top lanes by realized $/hr.

Each tick, per idle long-haul-tagged hull, it launches a per-hull worker that:
  DISCOVER  reads the global-best cross-system sink + cheapest source per good (out-of-horizon lanes)
  PRICE     computes realized both-side price-impact economics over the REAL gate-graph route
  SIZE      buys only the optimal tranche (marginal unit still clears the floor; never oversells)
  EXECUTE   buy@source -> multi-jump -> per-tranche sell@sink -> opportunistic backhaul or deadhead
  GUARD     every buy is fail-closed: within the per-haul cap AND leaving the 200k contract cushion
            intact AND within the total in-flight exposure cap (all Admiral-authorized, aggressive)

It is ARMED but INERT on arm: it touches ONLY hulls you tag long-haul. Populate the fleet with
'fleet add --operation long-haul --ship X'; untag to remove a hull (no restart). There is NO
feature flag — the tag is the on/off seam. The shared sp-m3122 liveness watchdog kills+relaunches
a stalled worker; a legitimately-long multi-hop leg is IN_TRANSIT and never false-killed.

Examples:
  spacetraders workflow long-haul-coordinator --agent TORWIND
  spacetraders workflow long-haul-coordinator --player-id 1`,
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

			containerID, err := client.LongHaulArbCoordinator(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("failed to arm long-haul arb coordinator: %w", err)
			}

			fmt.Println("✓ Long-haul arb coordinator armed")
			fmt.Printf("  Container ID: %s\n", containerID)
			fmt.Printf("  Agent:        %s (player %d)\n", playerIdent.AgentSymbol, playerIdent.PlayerID)
			fmt.Println("\n  It is INERT until you tag hulls: 'fleet add --operation long-haul --ship <SYMBOL>'.")
			fmt.Println("  Untag a hull to remove it (no restart). There is no feature flag — the tag is the seam.")
			fmt.Println("  Stop with 'spacetraders container stop " + containerID + "'.")
			return nil
		},
	}

	return cmd
}
