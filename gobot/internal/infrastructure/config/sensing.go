package config

// SensingConfig holds the probe-sensing coordinator's config.yaml-authoritative
// knobs. The daemon injects these into the probe_sensing_coordinator launch
// config on every build — creation AND restart recovery, via resolveSensingConfig
// — so a captain retunes them by editing config.yaml and restarting, no code
// redeploy (the sp-ts82 live-config pattern, RULINGS #5). The int knobs live in
// the `tune --operation sensing` registry instead; this section carries only what
// the int-only tune mechanism cannot: the string-valued whitelist.
//
// A zero value means "unset" and defers to the coordinator's own documented
// default, so the daemon injects only the keys the captain actually set.
type SensingConfig struct {
	// GoodsWhitelist is the comma-separated set of goods whose market depth
	// defines the sensing footprint — a system earns standing probes only for
	// depth in these goods. Empty/absent => the coordinator's era-goods default
	// (defaultSensingWhitelist). This is the single source of truth for the
	// whitelist: a stale copy persisted in the container config can never
	// shadow it.
	GoodsWhitelist string `mapstructure:"goods_whitelist"`
}
