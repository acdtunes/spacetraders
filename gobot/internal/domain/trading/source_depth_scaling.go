package trading

import (
	"context"
	"time"
)

// SourceBreadthReader resolves a source market's LISTING BREADTH — how many goods that market
// trades. It reports (0, false) for anything it cannot positively confirm: an uncached market, a
// read error, a market this fleet has never scanned. Every caller reads that as unknown, and
// unknown is the uniform prior.
type SourceBreadthReader interface {
	ListingBreadth(ctx context.Context, waypoint string) (int, bool)
}

// SourceDepthScaling is the depth-conditioned prior on the compression debt a BUY-side pacing
// consult compares against its bound.
//
// A consult reading a source-drain key is pacing itself off its own recorded buying, and that debt
// accrues per unit at a single impact coefficient, so the market's own depth never enters it.
// Buying a tranche out of a broad hub moves its ask by a fraction of what the same tranche does to
// a market listing one good, so pricing both the same holds a hub yielded for hours over a move
// too small to measure while the buyer diverts onto whatever thin droplet is untouched.
//
// The scale applies to the debt a consult READS. Accrual, decay and the bound are untouched, so it
// sets the PRIOR a source is paced against and never the mechanism that records or sheds it —
// which is why a replayed history prices through it with no rewrite.
//
// Every unconfirmed input resolves to the uniform prior of 1.0, the cautious direction: a source
// whose breadth we cannot read is paced at its full debt. Nothing here can make a source read MORE
// compressed than the uniform prior makes it.
type SourceDepthScaling struct {
	// Enabled applies the prior. Disabled is the kill switch: every source is paced in full.
	Enabled bool
	// ThinListings is the breadth at or under which a source is paced at its full debt. It holds
	// the thin end of the curve where the protection is load-bearing: raising it widens the class
	// treated as a micro-market.
	ThinListings int
	// MinDebtScale floors the relief, so no source is paced as bottomless. It keeps a sustained
	// drain blocking at ANY breadth; without it a broad enough market would take an unbounded
	// repeat buy for free.
	MinDebtScale float64
}

// The shipped fit. Both shape terms are refit per era and config overrides either.
const (
	// DefaultSourceThinListings paces at full debt the markets narrow enough that one tranche
	// plausibly clears their book.
	DefaultSourceThinListings = 2
	// DefaultMinSourceDebtScale bounds the relief an arbitrarily broad hub may earn on the buy
	// side. It sits ABOVE the sell side's floor: a repeat buy moves a hub's ask by a small but
	// real amount, where the matching dump barely moves its bid, so the buy side keeps more
	// caution at the deep end than the sell side does.
	DefaultMinSourceDebtScale = 0.2
)

// uniformDebtScale paces a drain at the full debt it accrued.
const uniformDebtScale = 1.0

// DefaultSourceDepthScaling is the prior in force wherever no operator override replaces it, so a
// ledger built without a configured policy runs the shipped fit rather than a zero shape.
func DefaultSourceDepthScaling() SourceDepthScaling {
	return SourceDepthScaling{
		Enabled:      true,
		ThinListings: DefaultSourceThinListings,
		MinDebtScale: DefaultMinSourceDebtScale,
	}
}

// DebtScale returns the multiplier on a source's standing compression debt for a market whose
// cached listings number `listings`. It returns the uniform prior whenever the policy is disabled,
// the breadth is unreadable (non-positive), the market is at or under the thin threshold, or a
// shape term is outside the range the model is defined on — an ill-formed knob must not silently
// un-pace every source.
func (s SourceDepthScaling) DebtScale(listings int) float64 {
	if !s.Enabled || listings <= 0 {
		return uniformDebtScale
	}
	if s.ThinListings <= 0 || s.MinDebtScale <= 0 || s.MinDebtScale > uniformDebtScale {
		return uniformDebtScale
	}
	if listings <= s.ThinListings {
		return uniformDebtScale
	}
	scale := float64(s.ThinListings) / float64(listings)
	if scale < s.MinDebtScale {
		return s.MinDebtScale
	}
	return scale
}

// SetSourceDepthScaling replaces the pacing prior and wires the listing-breadth lookup it reads.
// Breadth is an OBSERVABLE of the market, read here and never written. A nil reader leaves every
// source on the uniform prior, so a ledger nobody wired paces exactly as one whose markets are all
// uncached.
func (l *LaneCooldownLedger) SetSourceDepthScaling(policy SourceDepthScaling, breadth SourceBreadthReader) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sourceDepth = policy
	l.sourceBreadth = breadth
}

// SetSourceBreadthReader wires the breadth lookup while leaving the prior as it stands, for a
// caller that has no policy to state.
func (l *LaneCooldownLedger) SetSourceBreadthReader(breadth SourceBreadthReader) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sourceBreadth = breadth
}

// PacedDebt is the live debt a BUY-side pacing consult compares against TrancheDebt, scaled by how
// deep the source market is. It is Debt with the depth prior applied and nothing else.
//
// The breadth read is skipped — no query issued — whenever there is nothing the prior could
// change: a disabled prior, no wired reader, a source carrying no debt, or a key naming two
// markets.
func (l *LaneCooldownLedger) PacedDebt(ctx context.Context, key LaneKey, now time.Time) float64 {
	debt := l.Debt(key, now)
	if debt <= 0 {
		return debt
	}
	return debt * l.sourceDepthScale(ctx, key)
}

// sourceDepthScale resolves the depth prior for one key, reading the breadth lookup outside the
// ledger mutex so a market read can never serialize against accrual.
//
// SOURCE-DRAIN KEYS ONLY. A full lane names two markets and there is no single breadth to read;
// those keys are also the lane ranker's, and re-pricing them would move the income side.
func (l *LaneCooldownLedger) sourceDepthScale(ctx context.Context, key LaneKey) float64 {
	l.mu.Lock()
	policy, breadth := l.sourceDepth, l.sourceBreadth
	l.mu.Unlock()

	if !policy.Enabled || breadth == nil || key.Dest != "" {
		return uniformDebtScale
	}
	listings, ok := breadth.ListingBreadth(ctx, key.Source)
	if !ok {
		return uniformDebtScale
	}
	return policy.DebtScale(listings)
}
