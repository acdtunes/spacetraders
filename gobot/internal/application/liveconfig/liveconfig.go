// Package liveconfig gives a standing coordinator a PER-TICK live view of its own
// persisted container config — the seam that makes a `spacetraders tune` take
// effect on the NEXT reconcile tick with no container restart.
//
// Background: buildCommandForType runs only at container CREATE and at restart
// RECOVERY, never per tick. A coordinator's resolveConfig(cmd) re-derives its knobs
// every tick but from that FROZEN command, so a config-column write was invisible to
// a running loop: reads must come from persisted DB config, not launch-frozen
// metadata. This package replaces the SOURCE of the per-tick resolve, not its
// cadence: the coordinator snapshots the live column once at tick start and runs
// the whole tick on that snapshot; the next tick sees any newer value. No polling
// thread, no notify bus — the tick IS the poll.
//
// Fallback chain per tunable knob (mirroring FactoryWorkerCapConfigProvider):
//   - snapshot read SUCCEEDED → the config column is AUTHORITATIVE: a positive value
//     is the live value (launch values live in the same column, so this is also the
//     launch value until a tune amends it); an absent/non-positive key means "the
//     documented default applies" — the `tune <key> 0` revert semantics.
//   - snapshot read FAILED (reader unwired, container row missing, transient DB
//     error) → the tick runs entirely on the launch command's values — the fail-safe
//     launch behavior, never a half-applied config.
package liveconfig

import "context"

// Snapshot is one tick's frozen view of a container's persisted config column,
// taken at tick start. A tick runs entirely on the snapshot it took; the NEXT
// tick sees any value tuned in between.
type Snapshot map[string]interface{}

// Reader snapshots a container's persisted config at tick start. Implemented by
// the grpc adapter over the container repository (the same read path the live
// worker-cap and standby-hub providers use); coordinators receive it via
// optional injection and treat a nil reader as "no live config" (launch-frozen
// behavior).
type Reader interface {
	// Snapshot returns the container's current persisted config. A missing row or
	// unreadable config errors, so the caller falls back to its launch command
	// rather than silently running on an empty config.
	Snapshot(ctx context.Context, containerID string, playerID int) (Snapshot, error)
}

// PositiveInt returns the key's value when it is present, numeric, and positive —
// the definition of "a live override is set". It decodes every numeric shape a
// container config carries: native int/int64/int32 on the fresh-launch path and
// float64 on the JSON-recovery path (persisted numbers round-trip through
// float64; omitting a shape would silently drop a live override).
func (s Snapshot) PositiveInt(key string) (int, bool) {
	if s == nil {
		return 0, false
	}
	value, ok := numericInt(s[key])
	if !ok || value <= 0 {
		return 0, false
	}
	return value, true
}

// PositiveIntOrZero returns the live override when one is set, else 0 — so the
// coordinator's existing "<= 0 → documented default" resolve chain applies. This
// is exactly the `tune <key> 0` revert: a zeroed/deleted key makes the
// documented default the effective value on the next tick.
func (s Snapshot) PositiveIntOrZero(key string) int {
	value, _ := s.PositiveInt(key)
	return value
}

func numericInt(raw interface{}) (int, bool) {
	switch v := raw.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case int32:
		return int(v), true
	case float64:
		return int(v), true
	}
	return 0, false
}
