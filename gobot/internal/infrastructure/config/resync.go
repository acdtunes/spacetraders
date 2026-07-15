package config

import "time"

// DefaultResyncInterval / DefaultResyncJitter are the sane defaults the daemon's
// periodic full-fleet ship resync (sp-p1ci) falls back to when the operator has
// not set [ship_resync] in config.yaml. Base ~1h keeps the DB from drifting vs
// live API truth between the event-driven updates; the +/-10min jitter decoheres
// the resync off a fixed phase so a fleet of daemons cannot stack their ListShips
// bursts on the same wall-clock minute.
const (
	DefaultResyncInterval = time.Hour
	DefaultResyncJitter   = 10 * time.Minute
)

// ResyncConfig holds the periodic full-fleet ship-resync knobs (sp-p1ci) under
// the [ship_resync] section. Following the sp-ts82 live-config idiom, a zero
// value means "unset" and defers to the documented default, so the operator
// retunes the cadence by editing config.yaml and restarting — no code redeploy.
type ResyncConfig struct {
	// IntervalSeconds is the base wait between full-fleet resyncs. 0/absent =>
	// DefaultResyncInterval (1h).
	IntervalSeconds int `mapstructure:"interval_seconds"`

	// JitterSeconds is the +/- random spread applied to each interval. Because 0
	// is the "unset" sentinel that selects DefaultResyncJitter (10min), a
	// NEGATIVE value is how the operator explicitly disables jitter for a fixed
	// cadence.
	JitterSeconds int `mapstructure:"jitter_seconds"`
}

// ResolvedInterval maps IntervalSeconds to a duration, applying the default for
// an unset/non-positive knob.
func (c ResyncConfig) ResolvedInterval() time.Duration {
	if c.IntervalSeconds <= 0 {
		return DefaultResyncInterval
	}
	return time.Duration(c.IntervalSeconds) * time.Second
}

// ResolvedJitter maps JitterSeconds to a duration: negative disables jitter (0);
// zero/absent applies the default; positive is taken literally.
func (c ResyncConfig) ResolvedJitter() time.Duration {
	if c.JitterSeconds < 0 {
		return 0
	}
	if c.JitterSeconds == 0 {
		return DefaultResyncJitter
	}
	return time.Duration(c.JitterSeconds) * time.Second
}
