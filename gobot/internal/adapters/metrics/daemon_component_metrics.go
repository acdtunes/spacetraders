package metrics

// DaemonComponentRecorder is implemented by collectors that track supervised
// daemon background components. A separate single-method interface
// (instead of widening MetricsRecorder) so existing MetricsRecorder
// implementations and test fakes keep compiling.
type DaemonComponentRecorder interface {
	RecordDaemonComponentRestart(component string)
}

// RecordDaemonComponentRestart records a supervised daemon component restart
// globally. Wired into supervise.WithOnRestart at daemon boot.
func RecordDaemonComponentRestart(component string) {
	if globalCollector == nil {
		return
	}
	if rec, ok := globalCollector.(DaemonComponentRecorder); ok {
		rec.RecordDaemonComponentRestart(component)
	}
}
