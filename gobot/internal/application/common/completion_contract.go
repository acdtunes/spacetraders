package common

// CompletionReporter is the honest-completion check of the container lifecycle
// contract (sp-7yej invariant 2, first enforced for trade-route by sp-1hj5).
//
// A coordinator RESPONSE implements it to let the container runner derive the
// run's completion status from what actually happened, not from the handler's
// Go error alone. A handler that ends its run deliberately returns a nil error
// (so the runner's restart loop cannot crashloop it), yet the run may still not
// have honestly completed — e.g. it exited holding cargo bought this run
// (stranded) or with its task otherwise incomplete. The runner refuses
// success=true for such a run: it terminalizes the container as FAILED and
// signals completion with success=false carrying the reported reason, instead
// of the laden success=true that released TORWIND-19 holding 18 lab_instruments
// (the sp-1hj5 incident).
//
// This generalizes two precedents:
//   - sp-vwhi's parked flag: a response field the runner inspects after a clean
//     iteration to keep a deliberate non-completion out of contract.completed.
//   - arb-run's sp-5nqx stranded-cargo rule, which returns a non-nil error
//     instead. That is correct for arb because a restarted arb run RESUMES its
//     operator-fixed lane (skip the buy, deliver the held tranche). A
//     coordinator whose work is dynamically selected (trade-route's ranked
//     lane) cannot resume it after a restart — a retry would trade AROUND the
//     stranded cargo — so its honesty must not trigger the retry loop, which is
//     exactly what the nil-error + veto shape guarantees.
type CompletionReporter interface {
	// CompletionOutcome reports whether the run honestly completed its task.
	// ok=false carries a reason naming what is stranded/incomplete; the runner
	// uses it verbatim as the container's failure signature.
	CompletionOutcome() (ok bool, reason string)
}
