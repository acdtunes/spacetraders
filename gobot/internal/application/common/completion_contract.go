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

// ReleaseReasonHonestCompletionVeto is the release reason the container runner stamps on
// a hull whose run was refused by the veto above — the durable trace of "this hull came
// back from a run that could not honestly finish", most often still holding cargo the run
// bought and nothing where it stands would buy.
//
// It exists because the veto used to release the hull as a plain "failed", the same mark
// a routing outage or a planner error leaves. That made the strand invisible the moment
// the container died: the hull went back in the pool indistinguishable from one that
// merely had a bad lane, so the fleet coordinator relaunched an identical run onto the
// identical dead ground, which re-inherited the same unsold obligation and was refused
// again — a loop that burns a container per turn and never resolves the cargo.
//
// It lives here, next to the contract it records, because the two sides are in different
// layers: the adapter (container runner) writes it, the application (fleet coordinators)
// reads it back off the ship row to decide what the hull needs next. One constant so the
// writer and the readers can never drift. This is the whole cross-tick channel — nothing
// is held in memory between ticks (RULINGS #2).
const ReleaseReasonHonestCompletionVeto = "honest_completion_veto"
