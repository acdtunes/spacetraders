package common

// CompletionReporter is the honest-completion check of the container lifecycle
// contract: a coordinator RESPONSE implements it to let the container runner
// derive the run's completion status from what actually happened, not from the
// handler's Go error alone. A handler may deliberately return a nil error to
// avoid crashlooping the runner's restart loop, yet the run may still not have
// honestly completed — e.g. it exited holding cargo bought this run (stranded)
// or with its task otherwise incomplete. The runner refuses success=true for
// such a run: it terminalizes the container as FAILED and signals completion
// with success=false carrying the reported reason.
//
// Implement this (nil error + veto via CompletionOutcome) rather than returning
// a non-nil error when a restart cannot safely resume the run's own work — e.g.
// a coordinator whose work is dynamically re-selected each run, where a retry
// would trade AROUND stranded cargo instead of resolving it. A non-nil error is
// the right signal when a restart deterministically RESUMES the same
// operator-fixed lane (skip the buy, deliver the held tranche).
type CompletionReporter interface {
	// CompletionOutcome reports whether the run honestly completed its task.
	// ok=false carries a reason naming what is stranded/incomplete; the runner
	// uses it verbatim as the container's failure signature.
	CompletionOutcome() (ok bool, reason string)
}
