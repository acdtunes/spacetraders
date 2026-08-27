package commands

import "context"

// APISaturationReader is the port the tour coordinator reads live API-budget pressure
// through, in permille of the ceiling, so a plan is ranked by both resources it spends. Its
// shape lives in trading.SaturationPermille. A plain int, because an unknown pressure is not
// a failure: 0 is the fail-open value every layer lands on — no reader, no tracker, a thin
// window, real headroom — and no spend rides on it, so there is no guard here to fail closed.
type APISaturationReader interface {
	SaturationPermille(ctx context.Context) int
}
