package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// scanDedupMutator is the subset of daemon operations the `fleet scan-dedup`
// verbs need, narrowed to an interface so the verb logic is unit-testable
// without a live daemon. *DaemonClient satisfies it. The daemon remains the
// SOLE writer of the persisted allowlist (RULINGS #3).
type scanDedupMutator interface {
	ScanDedupAllowlist(ctx context.Context, action, shipSymbol string, playerIdent *PlayerIdentifier) (*pb.ScanDedupAllowlistResponse, error)
}

// runScanDedupAdd arms shipSymbol for the sp-7q61f scan-dedup A/B test, live.
// Its trade-route circuit's earning-class guards may then reuse a visit's own
// arrival scan instead of spending a second live GetMarket call, from the next
// circuit run onward — no container restart. A no-op (already armed) is
// reported honestly.
func runScanDedupAdd(ctx context.Context, client scanDedupMutator, shipSymbol string, playerIdent *PlayerIdentifier) (string, error) {
	resp, err := client.ScanDedupAllowlist(ctx, "add", shipSymbol, playerIdent)
	if err != nil {
		return "", fmt.Errorf("failed to arm %s for scan-dedup: %w", shipSymbol, err)
	}
	if !resp.Changed {
		return fmt.Sprintf("• %s is already armed for scan-dedup — allowlist unchanged: %v\n", shipSymbol, resp.ShipSymbols), nil
	}
	return fmt.Sprintf("✓ %s armed for scan-dedup — its next trade-route run may reuse an arrival scan for the pre-buy guard instead of a second live GetMarket call. No container restart. Allowlist now: %v\n", shipSymbol, resp.ShipSymbols), nil
}

// runScanDedupRemove disarms shipSymbol, live. Removing an unarmed ship is
// reported as a no-op.
func runScanDedupRemove(ctx context.Context, client scanDedupMutator, shipSymbol string, playerIdent *PlayerIdentifier) (string, error) {
	resp, err := client.ScanDedupAllowlist(ctx, "remove", shipSymbol, playerIdent)
	if err != nil {
		return "", fmt.Errorf("failed to disarm %s for scan-dedup: %w", shipSymbol, err)
	}
	if !resp.Changed {
		return fmt.Sprintf("• %s was not armed for scan-dedup — nothing to remove. Allowlist: %v\n", shipSymbol, resp.ShipSymbols), nil
	}
	return fmt.Sprintf("✓ %s disarmed — its trade-route guards always take the live scan again from the next circuit run. No container restart. Allowlist now: %v\n", shipSymbol, resp.ShipSymbols), nil
}

// runScanDedupList reports the current armed set.
func runScanDedupList(ctx context.Context, client scanDedupMutator, playerIdent *PlayerIdentifier) (string, error) {
	resp, err := client.ScanDedupAllowlist(ctx, "list", "", playerIdent)
	if err != nil {
		return "", fmt.Errorf("failed to list the scan-dedup allowlist: %w", err)
	}
	if len(resp.ShipSymbols) == 0 {
		return "No ships armed for scan-dedup — every trade-route circuit runs its unconditional live guard scan (default, byte-identical).\n", nil
	}
	return fmt.Sprintf("Armed for scan-dedup: %v\n", resp.ShipSymbols), nil
}

// newFleetScanDedupCommand creates the `fleet scan-dedup` subcommand group —
// the live add/remove/list surface over the sp-7q61f scan-dedup A/B test's
// ship-symbol allowlist.
func newFleetScanDedupCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scan-dedup",
		Short: "Arm, disarm, or list ships on the scan-dedup A/B test's live allowlist (no restart)",
		Long: `Manage the sp-7q61f scan-dedup A/B test's live, operator-settable ship-symbol
allowlist. An armed ship's trade-route circuit may reuse a visit's own arrival
scan for its pre-buy earning-class guards (the stale-ask check, the buy-ceiling
check) instead of spending a second live GetMarket call on the identical
waypoint — but ONLY when the reuse is provably safe (same visit, negligible
elapsed time, no intervening action). Any doubt falls back to today's
unconditional live scan; the guard is never weakened (RULINGS #4).

Default: no ship is armed, so this feature is completely inert until you add
one. Changes take effect on that hull's NEXT trade-route circuit run — no
daemon restart.

Examples:
  spacetraders fleet scan-dedup add --ship TORWIND-5
  spacetraders fleet scan-dedup remove --ship TORWIND-5
  spacetraders fleet scan-dedup list`,
	}
	cmd.AddCommand(newFleetScanDedupAddCommand())
	cmd.AddCommand(newFleetScanDedupRemoveCommand())
	cmd.AddCommand(newFleetScanDedupListCommand())
	return cmd
}

func newFleetScanDedupAddCommand() *cobra.Command {
	var shipSymbol string

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Arm a ship for the scan-dedup A/B test, live (no restart)",
		Long: `Arm a ship for the sp-7q61f scan-dedup A/B test without a daemon restart. Its
trade-route circuit reads the live allowlist on its NEXT run — no container
restart, no interruption to a circuit already in flight.

Examples:
  spacetraders fleet scan-dedup add --ship TORWIND-5
  spacetraders fleet scan-dedup add --ship TORWIND-5 --agent TORWIND`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}

			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			msg, err := runScanDedupAdd(ctx, client, shipSymbol, playerIdent)
			if err != nil {
				return err
			}
			fmt.Print(msg)
			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol to arm (required)")
	return cmd
}

func newFleetScanDedupRemoveCommand() *cobra.Command {
	var shipSymbol string

	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Disarm a ship for the scan-dedup A/B test, live (no restart)",
		Long: `Disarm a ship for the sp-7q61f scan-dedup A/B test without a daemon restart.
Its trade-route circuit's guards always take the live scan again from the
NEXT run — no container restart.

Examples:
  spacetraders fleet scan-dedup remove --ship TORWIND-5`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}

			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			msg, err := runScanDedupRemove(ctx, client, shipSymbol, playerIdent)
			if err != nil {
				return err
			}
			fmt.Print(msg)
			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol to disarm (required)")
	return cmd
}

func newFleetScanDedupListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List ships currently armed for the scan-dedup A/B test",
		Long: `List every ship currently armed for the sp-7q61f scan-dedup A/B test.

Examples:
  spacetraders fleet scan-dedup list
  spacetraders fleet scan-dedup list --agent TORWIND`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			msg, err := runScanDedupList(ctx, client, playerIdent)
			if err != nil {
				return err
			}
			fmt.Print(msg)
			return nil
		},
	}
	return cmd
}
