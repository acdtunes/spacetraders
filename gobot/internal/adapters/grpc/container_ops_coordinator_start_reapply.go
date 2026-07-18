package grpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	expansionCmd "github.com/andrescamacho/spacetraders-go/internal/application/expansion/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// The SHARED coordinator-(re)start re-apply (sp-rsgc), generalizing the sp-ve3q frontier
// fix. A coordinator whose `start` verb mints a fresh container id, builds a config from
// CLI flags, and Add()s it will — on relaunch of a STOPPED coordinator — orphan the tuned
// row and come up on config-file DEFAULTS, silently wiping every operator `tune`. The
// daemon-RESTART recovery path already re-adopts the persisted config column
// (recoverContainer → buildCommandForType); only the coordinator-START verbs bypassed it.
// This makes the START verbs re-adopt the same column, closing the bug class for every
// tunable coordinator (frontier, freshness sizer, auto-outfit, scout-post) through ONE
// implementation.

// coordinatorStartSpec declares how one coordinator's `start` verb re-adopts the last
// persisted live-tuned config. It is the only per-coordinator surface: the merge and the
// safety-warning logic are shared.
type coordinatorStartSpec struct {
	// containerType is the persisted container_type FindMostRecentByType keys on.
	containerType string
	// label prefixes the operator warning line (e.g. "frontier", "freshsizer").
	label string
	// authoritativeKeys are taken from the NEW start verbatim, never carried from the prior
	// config: the new container id and the mode flags chosen for THIS start (dry_run /
	// launch-dry-run). A key ABSENT from the new base is REMOVED from the merged config so
	// the new start's mode always wins — e.g. relaunching auto-outfit without --dry-run
	// clears a prior auto_outfit_launch_dry_run and goes live.
	authoritativeKeys []string
	// overrideKeys are the numeric CLI flags the start verb carries: each overrides the
	// carried-forward value ONLY when explicitly set (>0), matching the CLI's "0 = use the
	// documented default" contract. A no-flag relaunch preserves every persisted tune.
	overrideKeys []string
	// safetyKnobs are credit-moving guards whose DISABLED sentinel is 0: the start path
	// warns loudly when one resolves permissive (effective <= 0), mirroring frontier's
	// max_probe_price=0 overpay-ceiling warning. A guard whose documented default is a
	// positive safe value never trips this — its effective floors at that default — so a
	// self-protecting coordinator carries none and stays silent (no false alarm).
	safetyKnobs []coordinatorSafetyKnob
}

// coordinatorSafetyKnob is one credit-moving guard the start path audits for a permissive
// (0 = disabled) resolution.
type coordinatorSafetyKnob struct {
	key             string // the config/tune key
	registryDefault int    // the documented default applied when the config carries no positive value
	warning         string // the operator-facing loud warning emitted when it resolves permissive
}

// coordinatorStartConfig loads the last persisted live-config for the player's coordinator
// of spec.containerType and overlays the fresh start config on top of it, so relaunching a
// previously-stopped coordinator via its `start` verb RE-ADOPTS its live-tuned knobs
// (sp-rsgc) instead of silently reverting to config-file defaults. A player with no prior
// coordinator of the type gets the base config verbatim (byte-identical fresh start). It
// also returns operator warnings for any credit-moving safety knob a (re)start resolved to
// a permissive value.
func (s *DaemonServer) coordinatorStartConfig(ctx context.Context, playerID int, base map[string]interface{}, spec coordinatorStartSpec) (map[string]interface{}, []string, error) {
	prior, err := s.containerRepo.FindMostRecentByType(ctx, spec.containerType, playerID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load prior %s config for re-apply: %w", spec.label, err)
	}
	priorConfig, err := parseContainerConfig(prior)
	if err != nil {
		return nil, nil, err
	}
	merged := mergeCoordinatorStartConfig(priorConfig, base, spec)
	return merged, coordinatorStartSafetyWarnings(merged, spec), nil
}

// mergeCoordinatorStartConfig overlays a fresh start's config on top of the last persisted
// config so a relaunch RE-ADOPTS the live-tuned knobs (sp-rsgc). With no prior config it
// returns base UNCHANGED — a fresh coordinator comes up on config-file defaults exactly as
// before. Otherwise the prior config is the base (carrying every tune the daemon-restart
// path would rebuild), with the spec's authoritative keys taken from the new start and its
// numeric override keys applied only when explicitly set (>0).
func mergeCoordinatorStartConfig(prior, base map[string]interface{}, spec coordinatorStartSpec) map[string]interface{} {
	if len(prior) == 0 {
		return base
	}
	merged := make(map[string]interface{}, len(prior)+len(base))
	for key, value := range prior {
		merged[key] = value
	}
	for _, key := range spec.authoritativeKeys {
		if value, ok := base[key]; ok {
			merged[key] = value
		} else {
			delete(merged, key)
		}
	}
	for _, key := range spec.overrideKeys {
		if value, ok := intValue(base[key]); ok && value > 0 {
			merged[key] = value
		}
	}
	return merged
}

// coordinatorStartSafetyWarnings flags every credit-moving safety knob a (re)start resolved
// to a PERMISSIVE value (sp-rsgc backstop, generalizing sp-ve3q). A knob resolves permissive
// when its effective value is <= 0 — i.e. neither the carried config nor a start flag gives
// it a positive value AND its documented default is 0 (disabled), e.g. frontier's
// max_probe_price overpay ceiling. This does NOT change any knob's 0=disabled contract; it
// only makes a start that comes up UNARMED loud instead of silent.
func coordinatorStartSafetyWarnings(config map[string]interface{}, spec coordinatorStartSpec) []string {
	var warnings []string
	for _, knob := range spec.safetyKnobs {
		if coordinatorKnobEffective(config, knob) <= 0 {
			warnings = append(warnings, knob.warning)
		}
	}
	return warnings
}

// coordinatorKnobEffective resolves the value a knob will actually run at, the same way the
// tune registry (tuneEffective) does: a positive config value is the live value; otherwise
// the documented default applies.
func coordinatorKnobEffective(config map[string]interface{}, knob coordinatorSafetyKnob) int {
	if value, ok := intValue(config[knob.key]); ok && value > 0 {
		return value
	}
	return knob.registryDefault
}

// parseContainerConfig decodes a prior container's persisted config column into a map, or
// nil when there is no prior container (a fresh start). A present-but-unparseable config is
// a hard error rather than a silent fall-back to defaults: dropping a real coordinator's
// tunes is the very failure sp-ve3q / sp-rsgc fix, so we surface it instead of masking it.
func parseContainerConfig(model *persistence.ContainerModel) (map[string]interface{}, error) {
	if model == nil || model.Config == "" {
		return nil, nil
	}
	config := map[string]interface{}{}
	if err := json.Unmarshal([]byte(model.Config), &config); err != nil {
		return nil, fmt.Errorf("failed to parse prior container %s config: %w", model.ID, err)
	}
	return config, nil
}

// printCoordinatorStartWarnings emits each re-apply safety warning on the coordinator's
// start path, prefixed with the coordinator label and player — the shared voice of the
// sp-ve3q loud-warn backstop.
func printCoordinatorStartWarnings(label string, playerID int, warnings []string) {
	for _, warning := range warnings {
		fmt.Printf("⚠️  WARNING [%s start player %d]: %s\n", label, playerID, warning)
	}
}

// --- per-coordinator specs ---------------------------------------------------

// frontierStartSpec is the frontier-expansion re-apply spec (sp-ve3q, now expressed through
// the shared machinery). container_id + dry_run are the new start's; the numeric CLI flags
// override only when explicitly set; and max_probe_price — the sp-3u5d overpay ceiling whose
// default is 0 (disabled = buy at any price) — is the credit-moving guard the start warns on
// when it comes up unarmed.
func frontierStartSpec() coordinatorStartSpec {
	return coordinatorStartSpec{
		containerType:     string(container.ContainerTypeFrontierExpansion),
		label:             "frontier",
		authoritativeKeys: []string{"container_id", "dry_run"},
		overrideKeys: []string{
			"tick_interval_secs",
			"max_probe_fleet",
			"max_spend_per_cycle",
			"purchase_cooldown_secs",
			"expansion_max_hops",
		},
		safetyKnobs: []coordinatorSafetyKnob{{
			key:             "max_probe_price",
			registryDefault: expansionCmd.FrontierTunableDefaults()["max_probe_price"],
			warning: "max_probe_price is 0 (DISABLED — the frontier will buy probes at ANY price); the sp-3u5d overpay " +
				"ceiling is UNARMED. Re-arm it: spacetraders tune --operation frontier max_probe_price <credits>",
		}},
	}
}

// marketFreshnessSizerStartSpec is the freshness-sizer re-apply spec (sp-rsgc). container_id
// + dry_run are the new start's; every numeric probe/SLA flag overrides only when explicitly
// set. No safety knob: max_spend_per_cycle (and the cooldown/window) floor at positive
// defaults in resolveSizerConfig, so the sizer can never come up uncapped — the re-adopt of
// the operator's tighter cap is the protection, and a warning would be a false alarm.
func marketFreshnessSizerStartSpec() coordinatorStartSpec {
	return coordinatorStartSpec{
		containerType:     string(container.ContainerTypeMarketFreshnessSizer),
		label:             "freshsizer",
		authoritativeKeys: []string{"container_id", "dry_run"},
		overrideKeys: []string{
			"tick_interval_secs",
			"sla_seconds",
			"max_probes_per_system",
			"max_probe_fleet",
			"max_spend_per_cycle",
			"purchase_cooldown_secs",
		},
	}
}

// autoOutfitStartSpec is the guarded auto-outfit re-apply spec (sp-rsgc). The launch config
// is identity-only — every tunable knob defaults in the coordinator and is live-tunable — so
// there are NO numeric start flags; the only authoritative keys are the new container id and
// the launch-time dry-run mode (auto_outfit_launch_dry_run), which a live relaunch clears.
// No safety knob: price_ceiling (default 500000) and treasury_reserve (default 50000) both
// floor at positive defaults in resolveAutoOutfitConfig, so neither can resolve permissive.
func autoOutfitStartSpec() coordinatorStartSpec {
	return coordinatorStartSpec{
		containerType:     string(container.ContainerTypeAutoOutfitCoordinator),
		label:             "auto-outfit",
		authoritativeKeys: []string{"container_id", "auto_outfit_launch_dry_run"},
	}
}

// scoutPostStartSpec is the scout-post-coordinator re-apply spec (sp-rsgc). container_id is
// the new start's; tick_interval_secs overrides only when explicitly set. No safety knob:
// the scout-post tunes (manning_stall_cycles, cross-system relay switch/hops) are manning /
// relay behavior, none credit-moving. The [scouting] config.yaml knobs are re-injected from
// config.yaml on every build (resolveScoutingConfig), so carrying a stale copy forward here
// is harmless — it is cleared and refreshed — while these tune-only knobs are re-adopted.
func scoutPostStartSpec() coordinatorStartSpec {
	return coordinatorStartSpec{
		containerType:     string(container.ContainerTypeScoutPostCoordinator),
		label:             "scoutpost",
		authoritativeKeys: []string{"container_id"},
		overrideKeys:      []string{"tick_interval_secs"},
	}
}
