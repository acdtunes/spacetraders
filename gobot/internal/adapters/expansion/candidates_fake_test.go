package expansion

import "context"

type fakeCandidateLister struct {
	candidates []ExpansionCandidate
	err        error
	gotMaxHops int
}

func (f *fakeCandidateLister) ExpansionCandidates(_ context.Context, _ int, maxHops int) ([]ExpansionCandidate, error) {
	f.gotMaxHops = maxHops
	if f.err != nil {
		return nil, f.err
	}
	within := make([]ExpansionCandidate, 0, len(f.candidates))
	for _, c := range f.candidates {
		if c.Hops <= maxHops {
			within = append(within, c)
		}
	}
	return within, nil
}
