package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// sp-686e round-trip pin: the stranded-hull detector threshold must travel from
// config.yaml's [trade_fleet] section into the loaded config unchanged (the seam the
// stranded-detection knob depends on), and an ABSENT key must resolve to the sentinel 0 —
// never a silent config-layer default. The tour coordinator's resolveStrandedThreshold
// turns 0/absent into the documented default 3, so the default lives in ONE place (the
// consumer), not smeared across the config layer. Exercises the REAL viper mapstructure
// pipeline (trade_fleet.stranded_consecutive_threshold -> TradeFleetConfig).

func TestLoadConfig_StrandedConsecutiveThreshold_RoundTrips(t *testing.T) {
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
		"trade_fleet:\n"+
			"  enabled: true\n"+
			"  stranded_consecutive_threshold: 5\n"), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.Equal(t, 5, cfg.TradeFleet.StrandedConsecutiveThreshold,
		"stranded_consecutive_threshold must reach the config struct so the captain can retune the stranded page threshold by editing config.yaml + restarting")
}

func TestLoadConfig_StrandedConsecutiveThreshold_AbsentIsZeroSentinel(t *testing.T) {
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	// enabled but NO stranded_consecutive_threshold — the default config.yaml shape.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
		"trade_fleet:\n"+
			"  enabled: true\n"), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.Equal(t, 0, cfg.TradeFleet.StrandedConsecutiveThreshold,
		"an absent threshold must be the sentinel 0 (the consumer resolves 0 -> default 3), never a config-layer default")
}

// closed_tours round-trip pin: the closed-tour arming knob (im74 solver support, this
// bead's config plumbing) must travel from config.yaml's [trade_fleet] section into the
// loaded config unchanged, so a captain arms closed-circuit tours by editing config.yaml
// + restarting — no code redeploy. This exercises the REAL viper mapstructure pipeline
// (trade_fleet.closed_tours -> TradeFleetConfig.ClosedTours), the ONE seam the grpc
// stamp/rebuild tests cannot cover (they set the struct field directly). A typo in the
// mapstructure tag would ship a silently-inert knob; this test catches it.
func TestLoadConfig_ClosedTours_RoundTrips(t *testing.T) {
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
		"trade_fleet:\n"+
			"  enabled: true\n"+
			"  closed_tours: true\n"), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.True(t, cfg.TradeFleet.ClosedTours,
		"closed_tours must reach the config struct so the captain can arm closed-circuit tours by editing config.yaml + restarting")
}

// closed_tours default-safety companion: an ABSENT closed_tours key resolves to false —
// the Go zero value viper leaves untouched — so a daemon that never sets the knob runs
// OPEN tours, byte-identical to today. This is the config-layer half of the default-safe
// proof (the grpc rebuild test proves the false reaches cmd.ClosedTours).
func TestLoadConfig_ClosedTours_AbsentIsFalse(t *testing.T) {
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	// enabled but NO closed_tours — the default config.yaml shape.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
		"trade_fleet:\n"+
			"  enabled: true\n"), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.False(t, cfg.TradeFleet.ClosedTours,
		"an absent closed_tours must default false (OPEN tours), never a config-layer default that silently arms closed mode")
}

// sp-uf64 round-trip pin: the reposition-reach knobs (the default-OFF flag + the two int tunables)
// must travel from config.yaml's [trade_fleet] section into the loaded config unchanged, so a
// captain arms the reach improvement by editing config.yaml + restarting — no code redeploy. This
// exercises the REAL viper mapstructure pipeline (the ONE seam the grpc stamp/rebuild tests cannot
// cover — they set the struct fields directly). A typo in any mapstructure tag would ship a
// silently-inert knob; this test catches it.
func TestLoadConfig_RepositionReach_RoundTrips(t *testing.T) {
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
		"trade_fleet:\n"+
			"  enabled: true\n"+
			"  reposition_reach_enabled: true\n"+
			"  reposition_reach_hop_decay_pct: 70\n"+
			"  reposition_reach_max_hulls_per_system: 3\n"), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.True(t, cfg.TradeFleet.RepositionReachEnabled,
		"reposition_reach_enabled must reach the config struct so the captain can arm the reach improvement by editing config.yaml + restarting")
	require.Equal(t, 70, cfg.TradeFleet.RepositionReachHopDecayPct,
		"reposition_reach_hop_decay_pct must round-trip so the per-hop deadhead decay is operator-tunable")
	require.Equal(t, 3, cfg.TradeFleet.RepositionReachMaxHullsPerSystem,
		"reposition_reach_max_hulls_per_system must round-trip so the anti-herd cap is operator-tunable")
}

// sp-uf64 default-safety companion: an ABSENT reposition_reach block resolves to the Go zero values
// viper leaves untouched (false + 0 + 0) — so a daemon that never sets the knobs runs the legacy
// 1-hop-first reposition, byte-identical to today. The two int sentinels (0) are the consumer's
// "resolve to default" signal (resolveRepositionReachHopDecay → 85, resolveRepositionReachMaxHulls
// → 5), so the defaults live in ONE place (the coordinator), never smeared across the config layer.
func TestLoadConfig_RepositionReach_AbsentIsDefaults(t *testing.T) {
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	// enabled but NO reposition_reach keys — the default config.yaml shape.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
		"trade_fleet:\n"+
			"  enabled: true\n"), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.False(t, cfg.TradeFleet.RepositionReachEnabled,
		"an absent reposition_reach_enabled must default false (legacy reposition), never a config-layer default that silently arms the reach path")
	require.Equal(t, 0, cfg.TradeFleet.RepositionReachHopDecayPct,
		"an absent hop_decay_pct must be the sentinel 0 (the consumer resolves 0 -> default 85), never a config-layer default")
	require.Equal(t, 0, cfg.TradeFleet.RepositionReachMaxHullsPerSystem,
		"an absent max_hulls_per_system must be the sentinel 0 (the consumer resolves 0 -> default 5), never a config-layer default")
}

// epic sp-fguo Part 2 round-trip pin: the rate-floor early-reposition knobs must travel from
// config.yaml's [trade_fleet] section into the loaded config unchanged, so the captain can arm and
// tune the trigger by editing config.yaml + restarting (no code redeploy). Exercises the REAL viper
// mapstructure pipeline.
func TestLoadConfig_RepositionRateFloor_RoundTrips(t *testing.T) {
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
		"trade_fleet:\n"+
			"  enabled: true\n"+
			"  reposition_rate_floor_enabled: true\n"+
			"  reposition_rate_floor_pct: 35\n"+
			"  reposition_rate_floor_improvement_pct: 250\n"+
			"  reposition_rate_floor_dwell_minutes: 20\n"), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.True(t, cfg.TradeFleet.RepositionRateFloorEnabled,
		"reposition_rate_floor_enabled must reach the config struct so the captain can arm the rate-floor trigger by editing config.yaml + restarting")
	require.Equal(t, 35, cfg.TradeFleet.RepositionRateFloorPct,
		"reposition_rate_floor_pct must round-trip so the under-earner threshold is operator-tunable")
	require.Equal(t, 250, cfg.TradeFleet.RepositionRateFloorImprovementPct,
		"reposition_rate_floor_improvement_pct must round-trip so the anti-thrash improvement bar is operator-tunable")
	require.Equal(t, 20, cfg.TradeFleet.RepositionRateFloorDwellMinutes,
		"reposition_rate_floor_dwell_minutes must round-trip so the per-hull dwell cadence is operator-tunable")
}

// epic sp-fguo Part 2 default-safety companion: an ABSENT reposition_rate_floor block resolves to the
// Go zero values viper leaves untouched (false + 0 + 0 + 0) — so a daemon that never sets the knobs
// runs with the trigger DORMANT, byte-identical to today. The three int sentinels (0) are the
// consumer's "resolve to default" signal (resolveRateFloorPct -> 40, resolveRateFloorImprovementPct
// -> 200, resolveRateFloorDwellMinutes -> 15), so the defaults live in ONE place (the coordinator).
func TestLoadConfig_RepositionRateFloor_AbsentIsDefaults(t *testing.T) {
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
		"trade_fleet:\n"+
			"  enabled: true\n"), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.False(t, cfg.TradeFleet.RepositionRateFloorEnabled,
		"an absent reposition_rate_floor_enabled must default false (trigger dormant), never a config-layer default that silently arms the trigger")
	require.Equal(t, 0, cfg.TradeFleet.RepositionRateFloorPct,
		"an absent reposition_rate_floor_pct must be the sentinel 0 (the consumer resolves 0 -> default 40)")
	require.Equal(t, 0, cfg.TradeFleet.RepositionRateFloorImprovementPct,
		"an absent reposition_rate_floor_improvement_pct must be the sentinel 0 (the consumer resolves 0 -> default 200)")
	require.Equal(t, 0, cfg.TradeFleet.RepositionRateFloorDwellMinutes,
		"an absent reposition_rate_floor_dwell_minutes must be the sentinel 0 (the consumer resolves 0 -> default 15)")
}

// sp-jsng round-trip pin: the candidate-widening knobs (candidate_hop_depth +
// candidate_shortlist_top_n) must travel from config.yaml's [trade_fleet] section into the
// loaded config unchanged, so a captain arms the wider candidate set — the #1 fleet-$/hr lever
// (sp-7q5t) — by editing config.yaml + restarting, no code redeploy. This exercises the REAL
// viper mapstructure pipeline (the ONE seam the grpc stamp/rebuild tests cannot cover — they set
// the struct fields directly). A typo in either mapstructure tag would ship a silently-inert
// knob (the widening stays unreachable); this test catches it.
func TestLoadConfig_CandidateWidening_RoundTrips(t *testing.T) {
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
		"trade_fleet:\n"+
			"  enabled: true\n"+
			"  candidate_hop_depth: 2\n"+
			"  candidate_shortlist_top_n: 8\n"), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.Equal(t, 2, cfg.TradeFleet.CandidateHopDepth,
		"candidate_hop_depth must reach the config struct so the captain can arm the wider candidate set by editing config.yaml + restarting")
	require.Equal(t, 8, cfg.TradeFleet.CandidateShortlistTopN,
		"candidate_shortlist_top_n must round-trip so the profitable-edge shortlist bound is operator-tunable")
}

// sp-jsng default-safety companion: an ABSENT candidate-widening block resolves to the Go zero
// values viper leaves untouched (0 + 0) — so a daemon that never sets the knobs runs the exact
// 1-hop candidate set, byte-identical to today. The two int sentinels (0) are the consumer's
// "resolve to default" signal (resolveCandidateHopDepth -> 1, resolveCandidateShortlistTopN -> 6),
// so the defaults live in ONE place (the coordinator), never smeared across the config layer.
func TestLoadConfig_CandidateWidening_AbsentIsDefaults(t *testing.T) {
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	// enabled but NO candidate_* keys — the default config.yaml shape.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
		"trade_fleet:\n"+
			"  enabled: true\n"), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.Equal(t, 0, cfg.TradeFleet.CandidateHopDepth,
		"an absent candidate_hop_depth must be the sentinel 0 (the consumer resolves 0 -> default 1, the exact 1-hop set), never a config-layer default that silently widens")
	require.Equal(t, 0, cfg.TradeFleet.CandidateShortlistTopN,
		"an absent candidate_shortlist_top_n must be the sentinel 0 (the consumer resolves 0 -> default 6)")
}

// sp-o4wa cargo_blocklist round-trip pin: the noise-goods blocklist must travel from
// config.yaml's [trade_fleet] section into the loaded config unchanged, so a captain arms
// the FUEL/ALUMINUM/PLASTICS filter by editing config.yaml + restarting — no code redeploy.
// Exercises the REAL viper mapstructure pipeline (trade_fleet.cargo_blocklist ->
// TradeFleetConfig.CargoBlocklist); a typo in the mapstructure tag would ship a silently-
// inert knob, and this catches it.
func TestLoadConfig_CargoBlocklist_RoundTrips(t *testing.T) {
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
		"trade_fleet:\n"+
			"  enabled: true\n"+
			"  cargo_blocklist:\n"+
			"    - FUEL\n"+
			"    - ALUMINUM\n"+
			"    - PLASTICS\n"), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.Equal(t, []string{"FUEL", "ALUMINUM", "PLASTICS"}, cfg.TradeFleet.CargoBlocklist,
		"cargo_blocklist must reach the config struct so the captain arms the noise-goods filter by editing config.yaml + restarting")
}

// An absent cargo_blocklist must be nil/empty — the byte-identical default. The tour
// coordinator's filter is a no-op on an empty set, so the fleet trades the full good
// universe exactly as before until a captain explicitly arms the list.
func TestLoadConfig_CargoBlocklist_AbsentIsEmpty(t *testing.T) {
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	// enabled but NO cargo_blocklist — the default config.yaml shape.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
		"trade_fleet:\n"+
			"  enabled: true\n"), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.Empty(t, cfg.TradeFleet.CargoBlocklist,
		"an absent cargo_blocklist must be empty (no filtering ⇒ byte-identical), never a config-layer default")
}
