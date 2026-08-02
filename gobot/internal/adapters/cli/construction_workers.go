package cli

import (
	"context"
	"fmt"
	"time"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
	"github.com/spf13/cobra"
)

// constructionWorkerCapMutator is the narrow daemon surface the `construction workers` verb needs.
// By construction it exposes ONLY the ConstructionWorkerCap RPC — no pipeline restart/stop — so "no
// restart" is guaranteed by the surface this verb can reach, exactly as the goods-factory worker-cap
// verb guarantees it for the factory fan-out.
type constructionWorkerCapMutator interface {
	ConstructionWorkerCap(ctx context.Context, constructionSite string, count int, playerIdent *PlayerIdentifier) (*pb.ConstructionWorkerCapResponse, error)
}

// runConstructionWorkers sets a RUNNING construction pipeline's concurrent supplyTask-worker cap live
// via the daemon, then formats the operator-facing result. resolveWorkerCap re-reads the cap off the
// pipeline row every drain tick and converges its fan-out to count on the next tick — no
// pipeline/daemon restart. A no-op (the cap already equalled count) is reported honestly.
func runConstructionWorkers(ctx context.Context, client constructionWorkerCapMutator, constructionSite string, count int, playerIdent *PlayerIdentifier) (string, error) {
	resp, err := client.ConstructionWorkerCap(ctx, constructionSite, count, playerIdent)
	if err != nil {
		return "", fmt.Errorf("failed to set worker cap %d on construction pipeline %s: %w", count, constructionSite, err)
	}
	if !resp.Changed {
		return fmt.Sprintf("• construction pipeline %s worker cap is already %d — unchanged\n", constructionSite, resp.WorkerCap), nil
	}
	return fmt.Sprintf("✓ construction pipeline %s worker cap set to %d — the drain re-reads it live and converges to at most %d concurrent hauler(s) next tick; no restart.\n", constructionSite, resp.WorkerCap, resp.WorkerCap), nil
}

// newConstructionWorkersCommand creates the `construction workers <site>` subcommand — the live
// concurrent supplyTask-worker cap on a RUNNING construction pipeline, the construction
// analogue of `goods factory workers`. No restart: resolveWorkerCap re-reads max_workers off the
// pipeline row each tick, and the value survives a daemon bounce (RULINGS #2). The positional site
// matches `construction start/status/stop`; --count matches `goods factory workers`.
func newConstructionWorkersCommand() *cobra.Command {
	var count int

	cmd := &cobra.Command{
		Use:   "workers <construction-site>",
		Short: "Set a running construction pipeline's concurrent worker cap live (no restart)",
		Long: `Set the maximum number of haulers a RUNNING construction pipeline drains concurrently,
without a restart. The construction drain re-reads its cap (max_workers) off the pipeline row every
tick, so it converges the fan-out to the new count on the next tick — a hull already mid-haul finishes
first, never force-killed. The cap is per-pipeline and persists across daemon restarts (RULINGS #2).

This is the live way to scale a running pipeline's throughput: unlike stopping and restarting the
pipeline, it never aborts in-flight hauls or risks the restart-wedge.

Examples:
  spacetraders construction workers X1-FB5-I56 --count 10
  spacetraders construction workers X1-FB5-I56 --count 4`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			constructionSite := args[0]
			if count < 1 {
				return fmt.Errorf("--count must be at least 1 (got %d) — raise it to widen the drain fan-out", count)
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

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			msg, err := runConstructionWorkers(ctx, client, constructionSite, count, playerIdent)
			if err != nil {
				return err
			}
			fmt.Print(msg)
			return nil
		},
	}

	cmd.Flags().IntVar(&count, "count", 0, "Maximum number of haulers the drain runs concurrently (required, >= 1)")

	return cmd
}
