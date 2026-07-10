package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// newWorkflowTourRunCommand creates the workflow tour-run subcommand: a ONE-SHOT,
// captain-directed, guarded multi-hop trade tour (sp-1ek0) — arb-run's twin, but the
// planner chooses the route instead of the captain naming a single lane.
//
// Like arb-run this is a THIN CLIENT: it asks the daemon to start a recovery-safe
// tour-run CONTAINER rather than flying in-process. The container asks the depth-aware
// planner for a tour over the hull's system + fresh gate neighbors, flies it leg by leg
// (buy/sell through the RouteExecutor-backed handlers), re-verifies every price live at
// the dock, re-plans on drift (bounded), and stops — no loop.
//
// Guards fail CLOSED: every buy is live-checked against the working-capital floor and
// the cumulative spend cap; a leg whose live price has moved past tolerance is skipped
// and re-planned; a tour that ends holding cargo it bought reports FAILED (never a
// false success). Run this only on a genuinely idle hull — the daemon refuses one it is
// actively flying. If the planner is unreachable or finds no tour, the run exits cleanly
// ("tour unavailable") and the single-lane trade-route remains the fallback.
func newWorkflowTourRunCommand() *cobra.Command {
	var (
		shipSymbol  string
		maxHops     int
		maxSpend    int64
		minMargin   int
		replanLimit int
		reserve     int64
		iterations  int
	)

	cmd := &cobra.Command{
		Use:   "tour-run",
		Short: "Fly one idle hull through planner-chosen, guarded multi-hop trade tours (as a daemon container)",
		Long: `Ask the daemon to fly ONE idle hull through a planner-chosen multi-hop trade tour:
the depth-aware planner picks a tour over the hull's system and its fresh gate
neighbors, and the container flies it leg by leg — buying and selling in tranches,
re-verifying every price live at the dock, and re-planning when reality drifts. This
is the tour twin of 'workflow arb-run' (which flies one captain-named lane); here the
planner chooses the route.

Continuous mode (--iterations -1): on manifest completion the container re-plans from
the hull's CURRENT position and live market and flies the NEXT tour immediately — no
captain in the loop — until margins die (no profitable tour) or it is stopped. This
turns capital velocity from captain-cadence into engine-cadence: one launch, earns
until the market is exhausted. A laden hull's held cargo is fed to the planner as
sell-legs, so a tour that ends holding stock is liquidated by the next one rather than
needing a manual rescue. --iterations N flies exactly N tours; 0 (default) flies one.

Guards (each fails CLOSED):
  - every buy is live-checked against the working-capital floor and the per-tour spend
    cap (default 25% of live treasury, re-resolved each tour in continuous mode);
  - a leg whose live price has moved past tolerance is skipped and re-planned (bounded);
  - a run that ends holding cargo it bought reports FAILED, never a false success.

Execution model: the tour runs INSIDE the daemon as a container (single-writer,
claim-release-on-death, RouteExecutor-backed travel, restart-safe — a restart re-plans
from current position/cargo, and a continuous run resumes continuous). This command
only starts it and returns the container id; follow it with 'container logs'. The
daemon must be running. Run only on an idle hull.

Examples:
  spacetraders workflow tour-run --ship TORWIND-19 --agent TORWIND
  spacetraders workflow tour-run --ship TORWIND-19 --iterations -1 --agent TORWIND
  spacetraders workflow tour-run --ship TORWIND-19 --max-hops 4 --max-spend 300000 --iterations -1 --agent TORWIND`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
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

			// Optional knobs: 0 (flag unset) → nil, letting the coordinator apply its own
			// default per knob (max_hops→6, max_spend→25% of treasury, replan_limit→2,
			// iterations→one tour). --iterations -1 (continuous) is non-zero, so it maps
			// through to &(-1) and is honored.
			result, err := client.StartTourRun(ctx, shipSymbol, playerIdent.PlayerID, &playerIdent.AgentSymbol,
				optionalInt32(maxHops), optionalInt64(maxSpend), optionalInt32(minMargin), optionalInt32(replanLimit), optionalInt64(reserve), optionalInt32(iterations))
			if err != nil {
				return fmt.Errorf("failed to start tour-run: %w", err)
			}

			fmt.Println("✓ Tour-run started")
			fmt.Printf("  Container ID:  %s\n", result.ContainerID)
			fmt.Printf("  Ship:          %s\n", result.ShipSymbol)
			fmt.Printf("  Status:        %s\n", result.Status)
			if result.Message != "" {
				fmt.Printf("  Message:       %s\n", result.Message)
			}
			fmt.Println("\n  The tour executes as a daemon container (single-writer, claim-release-on-death, one-shot).")
			fmt.Printf("  Use 'spacetraders container logs %s' to follow it.\n", result.ContainerID)
			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Idle hull to fly the tour (required)")
	cmd.Flags().IntVar(&maxHops, "max-hops", 0, "Cap the tour to this many hops (0 = planner default, 6)")
	cmd.Flags().Int64Var(&maxSpend, "max-spend", 0, "Per-tour spend cap in credits (0 = 25% of live treasury, re-resolved each tour when --iterations != 0/1)")
	cmd.Flags().IntVar(&minMargin, "min-margin", 0, "Per-unit margin floor passed to the planner (0 = planner default)")
	cmd.Flags().IntVar(&replanLimit, "replan-limit", 0, "Max live re-plans on price drift, per tour (0 = coordinator default, 2)")
	cmd.Flags().Int64Var(&reserve, "working-capital-reserve", 0, "Hard spend floor: never drop live treasury below this (0 = coordinator default)")
	cmd.Flags().IntVar(&iterations, "iterations", 0, "Tour count: -1 = CONTINUOUS (re-plan+fly from the new position until margins die), N>0 = N tours, 0 = one tour")

	return cmd
}

// optionalInt64 maps a CLI int64 guard flag to the proto's optional int64: 0 (unset) →
// nil, so the daemon coordinator applies its own default for that knob.
func optionalInt64(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}
