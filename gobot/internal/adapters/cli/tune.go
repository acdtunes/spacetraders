package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
	"github.com/spf13/cobra"
)

// containerTuner is the subset of daemon operations the `tune` verb needs (sp-vwek),
// narrowed to an interface so the verb logic is unit-testable without a live daemon.
// *DaemonClient satisfies it. By construction it exposes ONLY the tune/show RPCs —
// no container-restart method — so "the coordinator is never restarted" is
// guaranteed by the surface this verb can reach, exactly as the `fleet hub` and
// `goods factory workers` verbs guarantee it. The daemon is the SOLE writer of the
// persisted knob (RULINGS #3) and validates every tune against its bounds registry.
type containerTuner interface {
	TuneContainerConfig(ctx context.Context, containerID, operation, key string, value int64, playerID *int32, agentSymbol *string) (*pb.TuneContainerConfigResponse, error)
	ShowTunableConfig(ctx context.Context, containerID, operation string, playerID *int32, agentSymbol *string) (*pb.ShowTunableConfigResponse, error)
}

// runTune sets (value > 0) or reverts (value == 0) one live knob on a running
// container and formats the operator-facing old -> new report. The coordinator
// re-reads its config at each tick start, so the change lands on the NEXT tick —
// no container restart. A no-op (the knob already carried the value) is reported
// honestly rather than as a fresh change.
func runTune(ctx context.Context, client containerTuner, containerID, operation, key string, value int64, playerID *int32, agentSymbol *string) (string, error) {
	resp, err := client.TuneContainerConfig(ctx, containerID, operation, key, value, playerID, agentSymbol)
	if err != nil {
		return "", fmt.Errorf("failed to tune %s: %w", key, err)
	}
	if !resp.Changed {
		return fmt.Sprintf("• %s %s is already %d %s (%s) — unchanged\n",
			resp.ContainerId, resp.Key, resp.NewEffective, resp.Unit, resp.NewSource), nil
	}
	transition := fmt.Sprintf("%d -> %d %s", resp.OldEffective, resp.NewEffective, resp.Unit)
	suffix := ""
	if resp.NewSource == "default" {
		suffix = fmt.Sprintf(" (reverted to the documented default %d)", resp.DefaultValue)
	}
	return fmt.Sprintf("✓ %s %s: %s%s — the coordinator re-reads its config live and applies it on the next tick; no restart.\n",
		resp.ContainerId, resp.Key, transition, suffix), nil
}

// runTuneShow lists every live-tunable knob of a running container: effective value,
// source (live-config — launch values share that store — or default), and bounds.
func runTuneShow(ctx context.Context, client containerTuner, containerID, operation string, playerID *int32, agentSymbol *string) (string, error) {
	resp, err := client.ShowTunableConfig(ctx, containerID, operation, playerID, agentSymbol)
	if err != nil {
		return "", fmt.Errorf("failed to list tunable knobs: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Tunable knobs of %s (%s):\n", resp.ContainerId, resp.ContainerType)
	for _, k := range resp.Knobs {
		fmt.Fprintf(&b, "  %-24s %10d %-8s  source=%-11s bounds=[%d, %d]  default=%d  %s\n",
			k.Key, k.Effective, k.Unit, k.Source, k.Min, k.Max, k.DefaultValue, k.Description)
	}
	b.WriteString("\nSet: spacetraders tune <container-id|--operation <op>> <key> <value>   Revert: ... <key> 0\n")
	return b.String(), nil
}

// parseTuneArgs resolves the flexible positional grammar:
//
//	tune <container-id> <key> <value>        (no --operation)
//	tune --operation <op> <key> <value>      (target by coordinator type)
//	tune <container-id> <key> --reset        (value omitted)
//	tune <container-id> --show               (list knobs)
//	tune --operation <op> --show
func parseTuneArgs(args []string, operation string, reset, show bool) (containerID, key string, value int64, err error) {
	rest := args
	if operation == "" {
		if len(rest) == 0 {
			return "", "", 0, fmt.Errorf("a container id (or --operation) is required")
		}
		containerID = rest[0]
		rest = rest[1:]
	}
	if show {
		if len(rest) != 0 {
			return "", "", 0, fmt.Errorf("--show takes no key/value arguments")
		}
		return containerID, "", 0, nil
	}
	if len(rest) == 0 {
		return "", "", 0, fmt.Errorf("a knob key is required (use --show to list the tunable keys)")
	}
	key = rest[0]
	rest = rest[1:]
	switch {
	case reset:
		if len(rest) != 0 {
			return "", "", 0, fmt.Errorf("--reset takes no value argument")
		}
		return containerID, key, 0, nil
	case len(rest) != 1:
		return "", "", 0, fmt.Errorf("exactly one value is required (or --reset to revert to the default)")
	}
	value, err = strconv.ParseInt(rest[0], 10, 64)
	if err != nil {
		return "", "", 0, fmt.Errorf("value %q is not an integer: %w", rest[0], err)
	}
	if value < 0 {
		return "", "", 0, fmt.Errorf("value must be >= 0 (0 reverts the knob to its documented default)")
	}
	return containerID, key, value, nil
}

// NewTuneCommand creates the `tune` command (sp-vwek): the generic live runtime
// knob tuner over running containers.
func NewTuneCommand() *cobra.Command {
	var (
		operation string
		reset     bool
		show      bool
	)

	cmd := &cobra.Command{
		Use:   "tune [container-id] <key> <value>",
		Short: "Tune a running container's live knob (no restart)",
		Long: `Tune one live knob of a RUNNING container without restarting it (sp-vwek).

The daemon validates the (key, value) against its static bounds registry — an
out-of-bounds or unknown-key tune is rejected before anything is written — then
amends just the container's persisted config. The running coordinator re-reads
its config at each tick start, so the change takes effect on the NEXT reconcile
tick; it also survives daemon restarts (the config column is the recovery
source). Every effective tune is recorded as a config.tuned captain audit event.

A value of 0 (or --reset) reverts the knob to its documented default.
Currently tunable engines: the market-freshness sizer ("freshsizer") and the
frontier expansion coordinator ("frontier").

Examples:
  spacetraders tune --operation freshsizer purchase_cooldown_secs 60
  spacetraders tune --operation freshsizer max_spend_per_cycle 500000
  spacetraders tune market_freshness_sizer_coordinator-player-1-abcd sla_seconds 1800
  spacetraders tune --operation frontier purchase_cooldown_secs --reset
  spacetraders tune --operation freshsizer --show`,
		Args: cobra.RangeArgs(0, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			containerID, key, value, err := parseTuneArgs(args, operation, reset, show)
			if err != nil {
				return err
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
			playerID, agentSymbol := playerPointers(playerIdent)

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			var msg string
			if show {
				msg, err = runTuneShow(ctx, client, containerID, operation, playerID, agentSymbol)
			} else {
				msg, err = runTune(ctx, client, containerID, operation, key, value, playerID, agentSymbol)
			}
			if err != nil {
				return err
			}
			fmt.Print(msg)
			return nil
		},
	}

	cmd.Flags().StringVar(&operation, "operation", "", "Resolve the target by coordinator type instead of container id (freshsizer, frontier)")
	cmd.Flags().BoolVar(&reset, "reset", false, "Revert the knob to its documented default (same as value 0)")
	cmd.Flags().BoolVar(&show, "show", false, "List the container's tunable knobs with effective values, sources, and bounds")

	return cmd
}
