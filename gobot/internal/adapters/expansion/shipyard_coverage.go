package expansion

import (
	"context"

	expansionCmd "github.com/andrescamacho/spacetraders-go/internal/application/expansion/commands"
)

// candidateLister is the narrow slice of the ExpansionScanner the coverage reader reuses: the
// gate-reachable frontier candidates, each carrying a Scanned flag (whether its full waypoint
// set — hence its shipyards — has been swept).
type candidateLister interface {
	ExpansionCandidates(ctx context.Context, playerID int, maxHops int) ([]expansionCmd.ExpansionCandidate, error)
}

// GateShipyardCoverageReader reports whether the gate-reachable shipyards have been swept
// thoroughly enough that a missing heavy yard is CONCLUSIVE (trigger b). It reuses
// the ExpansionScanner: coverage is EXHAUSTED iff every gate-reachable system within the scan
// horizon has been SWEPT (a swept system reveals all its shipyards, so if none carries a heavy
// yard, none exists on-gate within reach). While ANY reachable system is still unscanned — or
// the reachable set is empty (cold start) — coverage is SPARSE and the (b) trigger must not
// fire. It implements the coordinator's commands.ShipyardCoverageReader driven port. Every read
// fails SAFE to unreadable (readable=false ⇒ the caller treats coverage as sparse).
type GateShipyardCoverageReader struct {
	scanner candidateLister
	maxHops int
}

// NewGateShipyardCoverageReader wires the reader over the frontier scanner. maxHops bounds the
// coverage horizon (the same reach the expansion queue scans).
func NewGateShipyardCoverageReader(scanner candidateLister, maxHops int) *GateShipyardCoverageReader {
	return &GateShipyardCoverageReader{scanner: scanner, maxHops: maxHops}
}

// GateShipyardsScanExhausted returns exhausted=true only when every gate-reachable system has
// been swept; sparse (false) otherwise; and readable=false when the frontier scan fails.
func (r *GateShipyardCoverageReader) GateShipyardsScanExhausted(ctx context.Context, playerID int) (bool, bool, error) {
	candidates, err := r.scanner.ExpansionCandidates(ctx, playerID, r.maxHops)
	if err != nil {
		return false, false, nil // fail-safe: unreadable → caller treats coverage as sparse
	}
	if len(candidates) == 0 {
		return false, true, nil // nothing reachable yet (cold start) → sparse, not exhausted
	}
	for _, candidate := range candidates {
		if !candidate.Scanned {
			return false, true, nil // an unswept reachable system → coverage still sparse
		}
	}
	return true, true, nil // every reachable system swept → gate shipyard coverage exhausted
}
