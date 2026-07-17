package cli

import (
	"context"
	"encoding/json"
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

// runTuneShow renders a running container's live-tunable knobs — effective value, source
// (live-config — launch values share that store — or default), bounds, unit, and description
// (sp-pvw3 readable tune). With filterKey set it narrows to that ONE knob (the `tune <target> <knob>`
// no-value form); with asJSON it emits a machine-readable object for scripts. The full listing is the
// default when neither a knob nor a value is given.
func runTuneShow(ctx context.Context, client containerTuner, containerID, operation, filterKey string, asJSON bool, playerID *int32, agentSymbol *string) (string, error) {
	resp, err := client.ShowTunableConfig(ctx, containerID, operation, playerID, agentSymbol)
	if err != nil {
		return "", fmt.Errorf("failed to list tunable knobs: %w", err)
	}
	knobs := resp.Knobs
	if filterKey != "" {
		knobs = filterTunableKnobs(resp.Knobs, filterKey)
		if len(knobs) == 0 {
			return "", fmt.Errorf("%q is not a tunable knob of %s (%s) — run the command with no knob to list them", filterKey, resp.ContainerId, resp.ContainerType)
		}
	}
	if asJSON {
		return renderTuneJSON(resp, knobs)
	}
	return renderTuneTable(resp, knobs, filterKey != ""), nil
}

// filterTunableKnobs narrows a knob listing to the one matching key (empty when unknown).
func filterTunableKnobs(knobs []*pb.TunableKnobStatus, key string) []*pb.TunableKnobStatus {
	for _, k := range knobs {
		if k.Key == key {
			return []*pb.TunableKnobStatus{k}
		}
	}
	return nil
}

// renderTuneTable formats the knob listing as the operator-facing table: name, effective value,
// unit, source, bounds, default, and description — one row per knob, sorted daemon-side by key.
func renderTuneTable(resp *pb.ShowTunableConfigResponse, knobs []*pb.TunableKnobStatus, single bool) string {
	var b strings.Builder
	heading := "Tunable knobs"
	if single {
		heading = "Tunable knob"
	}
	fmt.Fprintf(&b, "%s of %s (%s):\n", heading, resp.ContainerId, resp.ContainerType)
	for _, k := range knobs {
		fmt.Fprintf(&b, "  %-24s %10d %-8s  source=%-11s bounds=[%d, %d]  default=%d  %s\n",
			k.Key, k.Effective, k.Unit, k.Source, k.Min, k.Max, k.DefaultValue, k.Description)
	}
	b.WriteString("\nSet: spacetraders tune <container-id|--operation <op>> <key> <value>   Revert: ... <key> 0\n")
	return b.String()
}

// tuneKnobJSON / tuneShowJSON are the stable --json shapes for scripts (sp-pvw3): explicit snake_case
// keys, not the proto's omitempty JSON tags (which would drop a legitimate 0 effective value).
type tuneKnobJSON struct {
	Key         string `json:"key"`
	Effective   int64  `json:"effective"`
	Source      string `json:"source"`
	Min         int64  `json:"min"`
	Max         int64  `json:"max"`
	Default     int64  `json:"default"`
	Unit        string `json:"unit"`
	Description string `json:"description"`
}

type tuneShowJSON struct {
	ContainerID   string         `json:"container_id"`
	ContainerType string         `json:"container_type"`
	Knobs         []tuneKnobJSON `json:"knobs"`
}

// renderTuneJSON serializes the knob listing for scripts.
func renderTuneJSON(resp *pb.ShowTunableConfigResponse, knobs []*pb.TunableKnobStatus) (string, error) {
	out := tuneShowJSON{ContainerID: resp.ContainerId, ContainerType: resp.ContainerType, Knobs: []tuneKnobJSON{}}
	for _, k := range knobs {
		out.Knobs = append(out.Knobs, tuneKnobJSON{
			Key: k.Key, Effective: k.Effective, Source: k.Source,
			Min: k.Min, Max: k.Max, Default: k.DefaultValue, Unit: k.Unit, Description: k.Description,
		})
	}
	encoded, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to render tunable knobs as JSON: %w", err)
	}
	return string(encoded) + "\n", nil
}

// parseTuneArgs resolves the flexible positional grammar (sp-pvw3 makes the value OPTIONAL — a
// missing value is a READ, not an error):
//
//	tune <container-id>                      (READ: table of every knob)
//	tune --operation <op>                    (READ: table of every knob)
//	tune <container-id> <key>                (READ: that knob's value + metadata)
//	tune --operation <op> <key>              (READ: that knob's value + metadata)
//	tune <container-id> <key> <value>        (WRITE: set the knob)
//	tune --operation <op> <key> <value>      (WRITE: set the knob)
//	tune <container-id> <key> --reset        (WRITE: revert to the default)
//	tune --operation <op> --show             (READ, explicit; equivalent to omitting the value)
//
// isShow is true for every READ form; key is "" for the whole-container table and the knob name for a
// single-knob read. A negative value or an explicit --show paired with a value is rejected.
func parseTuneArgs(args []string, operation string, reset, show bool) (containerID, key string, value int64, isShow bool, err error) {
	rest := args
	if operation == "" {
		if len(rest) == 0 {
			return "", "", 0, false, fmt.Errorf("a container id (or --operation) is required")
		}
		containerID = rest[0]
		rest = rest[1:]
	}
	if len(rest) > 0 {
		key = rest[0]
		rest = rest[1:]
	}
	if reset {
		if key == "" {
			return "", "", 0, false, fmt.Errorf("--reset needs a knob key (use no knob to list the tunable keys)")
		}
		if len(rest) != 0 {
			return "", "", 0, false, fmt.Errorf("--reset takes no value argument")
		}
		return containerID, key, 0, false, nil
	}
	if show || len(rest) == 0 {
		// READ: no value token (or an explicit --show). key "" lists every knob; else one knob.
		if len(rest) != 0 {
			return "", "", 0, false, fmt.Errorf("--show takes no value argument")
		}
		return containerID, key, 0, true, nil
	}
	if len(rest) != 1 {
		return "", "", 0, false, fmt.Errorf("expected a single value after the knob (or omit it to read, or --reset to revert)")
	}
	value, err = strconv.ParseInt(rest[0], 10, 64)
	if err != nil {
		return "", "", 0, false, fmt.Errorf("value %q is not an integer: %w", rest[0], err)
	}
	if value < 0 {
		return "", "", 0, false, fmt.Errorf("value must be >= 0 (0 reverts the knob to its documented default)")
	}
	return containerID, key, value, false, nil
}

// NewTuneCommand creates the `tune` command (sp-vwek): the generic live runtime
// knob tuner over running containers.
func NewTuneCommand() *cobra.Command {
	var (
		operation string
		reset     bool
		show      bool
		asJSON    bool
	)

	cmd := &cobra.Command{
		Use:   "tune [container-id] [key] [value]",
		Short: "Read or tune a running container's live knobs (no restart)",
		Long: `Read or tune the live knobs of a RUNNING container without restarting it (sp-vwek/sp-pvw3).

READ (omit the value):
  tune --operation <op>            table of EVERY knob: value, default, min/max, unit, description
  tune --operation <op> <key>      that one knob's current value + metadata
  (add --json to emit a machine-readable object for scripts)

WRITE (give a value):
  tune --operation <op> <key> <value>   set the knob (or by <container-id> instead of --operation)

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
  spacetraders tune --operation frontier                       # read all frontier knobs
  spacetraders tune --operation frontier discovery_share        # read one knob's value + metadata
  spacetraders tune --operation frontier discovery_share 60     # 60% discover / 40% scan
  spacetraders tune --operation frontier --json                 # read all, as JSON
  spacetraders tune --operation freshsizer purchase_cooldown_secs 60
  spacetraders tune --operation frontier purchase_cooldown_secs --reset`,
		Args: cobra.RangeArgs(0, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			containerID, key, value, isShow, err := parseTuneArgs(args, operation, reset, show)
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
			if isShow {
				msg, err = runTuneShow(ctx, client, containerID, operation, key, asJSON, playerID, agentSymbol)
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
	cmd.Flags().BoolVar(&show, "show", false, "Force the read/list form (equivalent to omitting the value)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Render the read/list output as JSON for scripts")

	return cmd
}
