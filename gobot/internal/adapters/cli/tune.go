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
	TuneContainerConfig(ctx context.Context, containerID, operation, key string, value int64, playerIdent *PlayerIdentifier) (*pb.TuneContainerConfigResponse, error)
	ShowTunableConfig(ctx context.Context, containerID, operation string, playerIdent *PlayerIdentifier) (*pb.ShowTunableConfigResponse, error)
}

// tuneRequest is one resolved invocation of the `tune` grammar: containerID or
// operation names the target (never both), and isShow separates a read from a write.
type tuneRequest struct {
	containerID string
	operation   string
	key         string
	value       int64
	isShow      bool
}

// runTune sets (value > 0) or reverts (value == 0) one live knob on a running
// container and formats the operator-facing old -> new report. A no-op (the knob
// already carried the value) is reported honestly rather than as a fresh change.
func runTune(ctx context.Context, client containerTuner, req tuneRequest, playerIdent *PlayerIdentifier) (string, error) {
	resp, err := client.TuneContainerConfig(ctx, req.containerID, req.operation, req.key, req.value, playerIdent)
	if err != nil {
		return "", fmt.Errorf("failed to tune %s: %w", req.key, err)
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
	marker, effect := tuneEffectNotice(resp.GetApplies())
	return fmt.Sprintf("%s %s %s: %s%s — %s\n",
		marker, resp.ContainerId, resp.Key, transition, suffix, effect), nil
}

// tuneEffectNotice phrases what the write HAS and has NOT achieved, from the tuned
// knob's own registry entry rather than one sentence assumed to fit every target. The
// tick marker is reserved for the live case: a tune that cannot bite yet is not
// finished work, and undeclared is not permission to claim it is.
func tuneEffectNotice(applies pb.TuneApplies) (marker, effect string) {
	switch applies {
	case pb.TuneApplies_TUNE_APPLIES_LIVE:
		return "✓", "the coordinator re-reads its config live and applies it on the next tick; no restart."
	case pb.TuneApplies_TUNE_APPLIES_ON_REBUILD:
		return "⚠", "PERSISTED, NOT YET IN EFFECT — this knob binds when the coordinator is built, so the running loop keeps its old value. Restart or relaunch the coordinator to apply."
	default:
		return "⚠", "PERSISTED. This knob does not declare when it reaches the running loop, so do not assume it is in effect — restart or relaunch the coordinator to be certain."
	}
}

// runTuneShow renders a running container's live-tunable knobs — effective value, source
// (live-config — launch values share that store — or default), bounds, unit, and description
// (sp-pvw3 readable tune). With filterKey set it narrows to that ONE knob (the `tune <target> <knob>`
// no-value form); with asJSON it emits a machine-readable object for scripts. The full listing is the
// default when neither a knob nor a value is given.
func runTuneShow(ctx context.Context, client containerTuner, req tuneRequest, asJSON bool, playerIdent *PlayerIdentifier) (string, error) {
	resp, err := client.ShowTunableConfig(ctx, req.containerID, req.operation, playerIdent)
	if err != nil {
		return "", fmt.Errorf("failed to list tunable knobs: %w", err)
	}
	knobs := resp.Knobs
	if req.key != "" {
		knobs = filterTunableKnobs(resp.Knobs, req.key)
		if len(knobs) == 0 {
			return "", fmt.Errorf("%q is not a tunable knob of %s (%s) — run the command with no knob to list them", req.key, resp.ContainerId, resp.ContainerType)
		}
	}
	if asJSON {
		return renderTuneJSON(resp, knobs)
	}
	return renderTuneTable(resp, knobs, req.key != ""), nil
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
	Applies     string `json:"applies"`
	Description string `json:"description"`
}

// tuneAppliesLabel is the --json spelling: a stable token to gate on, not prose.
func tuneAppliesLabel(applies pb.TuneApplies) string {
	switch applies {
	case pb.TuneApplies_TUNE_APPLIES_LIVE:
		return "live"
	case pb.TuneApplies_TUNE_APPLIES_ON_REBUILD:
		return "rebuild"
	default:
		return "unspecified"
	}
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
			Min: k.Min, Max: k.Max, Default: k.DefaultValue, Unit: k.Unit,
			Applies: tuneAppliesLabel(k.GetApplies()), Description: k.Description,
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
func parseTuneArgs(args []string, operation string, reset, show bool) (tuneRequest, error) {
	req := tuneRequest{operation: operation}
	rest := args
	if operation == "" {
		if len(rest) == 0 {
			return tuneRequest{}, fmt.Errorf("a container id (or --operation) is required")
		}
		req.containerID = rest[0]
		rest = rest[1:]
	}
	if len(rest) > 0 {
		req.key = rest[0]
		rest = rest[1:]
	}
	if reset {
		if req.key == "" {
			return tuneRequest{}, fmt.Errorf("--reset needs a knob key (use no knob to list the tunable keys)")
		}
		if len(rest) != 0 {
			return tuneRequest{}, fmt.Errorf("--reset takes no value argument")
		}
		return req, nil
	}
	if show || len(rest) == 0 {
		// READ: no value token (or an explicit --show). key "" lists every knob; else one knob.
		if len(rest) != 0 {
			return tuneRequest{}, fmt.Errorf("--show takes no value argument")
		}
		req.isShow = true
		return req, nil
	}
	if len(rest) != 1 {
		return tuneRequest{}, fmt.Errorf("expected a single value after the knob (or omit it to read, or --reset to revert)")
	}
	value, err := strconv.ParseInt(rest[0], 10, 64)
	if err != nil {
		return tuneRequest{}, fmt.Errorf("value %q is not an integer: %w", rest[0], err)
	}
	if value < 0 {
		return tuneRequest{}, fmt.Errorf("value must be >= 0 (0 reverts the knob to its documented default)")
	}
	req.value = value
	return req, nil
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
amends just the container's persisted config. Every tune survives daemon restarts
(the config column is the recovery source), and every effective tune is recorded
as a config.tuned captain audit event.

WHEN A TUNE BITES IS A PER-KNOB FACT, not a per-coordinator one. Most knobs are
re-read from the config column at each tick start and need no restart. A few bind
when the coordinator is BUILT — sensing's inflight_cap, value_clamp_r and
pressure_half_life_secs — so they persist immediately but the RUNNING loop keeps
its launch value until a daemon restart or relaunch. The write confirmation says
which case it was for the knob you tuned, and --json reports it as "applies".
Anything reported as anything other than live is NOT yet in effect: re-check
rather than assuming a spending switch has taken hold.

A value of 0 (or --reset) reverts the knob to its documented default.
Tunable operations include the probe-sensing coordinator ("sensing"), the
scout-post reconciler ("scoutpost"), bootstrap ("bootstrap"), and more — the
daemon lists every supported alias when given an unknown one.

Examples:
  spacetraders tune --operation sensing                        # read all sensing knobs
  spacetraders tune --operation sensing probe_budget            # read one knob's value + metadata
  spacetraders tune --operation sensing probe_budget 120        # size the probe fleet budget
  spacetraders tune --operation sensing --json                  # read all, as JSON
  spacetraders tune --operation sensing purchase_cooldown_secs 60
  spacetraders tune --operation sensing purchase_cooldown_secs --reset`,
		Args: cobra.RangeArgs(0, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			req, err := parseTuneArgs(args, operation, reset, show)
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
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			var msg string
			if req.isShow {
				msg, err = runTuneShow(ctx, client, req, asJSON, playerIdent)
			} else {
				msg, err = runTune(ctx, client, req, playerIdent)
			}
			if err != nil {
				return err
			}
			fmt.Print(msg)
			return nil
		},
	}

	cmd.Flags().StringVar(&operation, "operation", "", "Resolve the target by coordinator type instead of container id (sensing, scoutpost, bootstrap, ...)")
	cmd.Flags().BoolVar(&reset, "reset", false, "Revert the knob to its documented default (same as value 0)")
	cmd.Flags().BoolVar(&show, "show", false, "Force the read/list form (equivalent to omitting the value)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Render the read/list output as JSON for scripts")

	return cmd
}
