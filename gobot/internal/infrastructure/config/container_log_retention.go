package config

import "time"

// The container_logs retention defaults (sp-p1jo4). The table had no bound at all and grew to
// 63% of the database, duplicating what daemon.log already holds on disk.
//
// WHY TWO WINDOWS. Measured on the live table, INFO+DEBUG are 98.5% of rows and
// ERROR+WARN+WARNING 1.5%, so keeping the incident spine for a fortnight costs about a hundredth
// of what keeping the chatter that long costs. One window would have to price the whole table at
// the chatter's rate; two let each class be held for as long as it is worth.
const (
	// DefaultContainerLogRetention is how long a non-problem (INFO/DEBUG/unrecognised) line is
	// kept. FORTY-EIGHT HOURS, set by what actually reads this table: every production reader
	// wants the newest rows and nothing else — the liveness watchdog reads one line per running
	// container against a 12-MINUTE stall threshold, `container logs` fetches the newest N, the
	// visualizer reconstructs the CURRENT tour, and the daemon's own GetContainerLogs RPC is a
	// stub returning nothing. So the window is set by the HUMAN read: an overnight incident stays
	// fully readable for two mornings. Ad-hoc forensic SQL has occasionally reached further back,
	// which is why the window is tunable for the duration of an investigation rather than paying
	// for that reach every hour of every day.
	DefaultContainerLogRetention = 48 * time.Hour

	// DefaultContainerLogProblemRetention is how long an ERROR/WARN/WARNING line is kept.
	// FOURTEEN DAYS, deliberately equal to persistence.ContainerRetentionWindow — the forensics
	// budget already ruled on for the containers table. Matching it ages a container row and its
	// error trail out together, so neither outlives the other.
	DefaultContainerLogProblemRetention = 14 * 24 * time.Hour

	// DefaultContainerLogSweepBatch is the maximum rows a single DELETE may take. The sweep runs
	// against the live trading database, so the size of each STATEMENT is what matters, not the
	// size of the sweep.
	DefaultContainerLogSweepBatch = 10_000

	// DefaultContainerLogSweepMaxBatches bounds ONE sweep's total work, so a pathological backlog
	// becomes several bounded daily sweeps instead of one hours-long I/O event on the hot path.
	DefaultContainerLogSweepMaxBatches = 2_000
)

// ContainerLogRetentionConfig holds the container_logs retention knobs under the
// [container_log_retention] section. Following the live-config idiom (ResyncConfig), a
// non-positive value means "unset" and defers to the documented default.
//
// RULINGS #22: the sweep is ARMED with no config present. Every knob here is a parameter, not an
// arming flag; the one kill switch is Disabled, and it defaults to false.
type ContainerLogRetentionConfig struct {
	// Disabled turns the sweep off entirely — a kill switch (#5) defaulting to false, because an
	// unbounded container_logs is what this section exists to prevent.
	Disabled bool `mapstructure:"disabled"`

	// WindowHours and ProblemWindowHours are the two retention windows. Setting the problem
	// window SHORTER is legal but pointless: the sweep stops at whichever cutoff is newer, so
	// the problem levels would simply be swept on the chatter's schedule.
	WindowHours        int `mapstructure:"window_hours"`
	ProblemWindowHours int `mapstructure:"problem_window_hours"`

	// BatchSize is the maximum rows a single DELETE may take; MaxBatches bounds one sweep.
	BatchSize  int `mapstructure:"batch_size"`
	MaxBatches int `mapstructure:"max_batches"`
}

// Enabled reports whether the sweep should run. Absent config means yes.
func (c ContainerLogRetentionConfig) Enabled() bool { return !c.Disabled }

// ResolvedWindow applies the default for an unset/non-positive knob. A non-positive value falls
// back rather than disabling the sweep: disabling is what Disabled is for, and a typo'd zero must
// never silently restore unbounded growth.
func (c ContainerLogRetentionConfig) ResolvedWindow() time.Duration {
	if c.WindowHours <= 0 {
		return DefaultContainerLogRetention
	}
	return time.Duration(c.WindowHours) * time.Hour
}

// ResolvedProblemWindow applies the default for an unset/non-positive knob.
func (c ContainerLogRetentionConfig) ResolvedProblemWindow() time.Duration {
	if c.ProblemWindowHours <= 0 {
		return DefaultContainerLogProblemRetention
	}
	return time.Duration(c.ProblemWindowHours) * time.Hour
}

// ResolvedBatchSize applies the default for an unset/non-positive knob.
func (c ContainerLogRetentionConfig) ResolvedBatchSize() int {
	if c.BatchSize <= 0 {
		return DefaultContainerLogSweepBatch
	}
	return c.BatchSize
}

// ResolvedMaxBatches applies the default for an unset/non-positive knob.
func (c ContainerLogRetentionConfig) ResolvedMaxBatches() int {
	if c.MaxBatches <= 0 {
		return DefaultContainerLogSweepMaxBatches
	}
	return c.MaxBatches
}
