package common

// RunTerminalReporter is the run-termination half of the container lifecycle
// contract: a coordinator RESPONSE implements it to tell the container runner
// that the command's WHOLE run is finished — not merely that one iteration
// returned. The runner-loop model otherwise re-enters an infinite
// (maxIterations=-1) container's handler the moment Handle() returns, with no
// pause in between; for a handler that owns its whole run internally and exits
// only at a terminal state (the bootstrap coordinator's gate-built EXPANSION
// exit), that re-entry turns "done, exiting" into an unpaced spin. A response
// reporting terminal makes the runner stop iterating and complete the container
// cleanly (COMPLETED), whatever the container's iteration budget says.
//
// Complementary to CompletionReporter, which judges HOW a finished run ended
// (honest or vetoed); this reports WHETHER the run is finished at all. A
// response may implement both.
type RunTerminalReporter interface {
	// RunTerminal reports whether the run reached its terminal state this
	// iteration. true ⇒ the runner must not re-enter the handler; it finishes
	// the container through its normal clean-exit path.
	RunTerminal() bool
}
