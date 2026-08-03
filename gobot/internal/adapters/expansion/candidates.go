package expansion

import "context"

// ExpansionCandidate is a gate-reachable system considered for probe coverage.
type ExpansionCandidate struct {
	SystemSymbol string
	Hops         int
	KnownMarkets int
	Charted      bool

	Scanned bool

	BranchRoot string
}

// candidateLister enumerates gate-reachable systems for shipyard backfill.
type candidateLister interface {
	ExpansionCandidates(ctx context.Context, playerID int, maxHops int) ([]ExpansionCandidate, error)
}
