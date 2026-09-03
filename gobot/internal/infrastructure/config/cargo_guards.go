package config

// CargoGuardsConfig is the [cargo_guards] section. An ABSENT key defers to the cargo
// handler's armed default (10% headroom, 120s); an EXPLICIT reuse_headroom_pct at or
// below 0, or reuse_max_age_secs at or below 0, DISARMS the reuse and restores a live
// read before every tranche (RULINGS #5). Pointers because absent and explicitly-zero
// mean opposite things here and an int cannot tell them apart.
type CargoGuardsConfig struct {
	ReuseHeadroomPct *int `mapstructure:"reuse_headroom_pct"`
	ReuseMaxAgeSecs  *int `mapstructure:"reuse_max_age_secs"`
}
