package absorption

// SinkDepthScaling is the depth-conditioned crush prior applied to an EXECUTED recovery shadow.
//
// A shadow is a QUANTITY-space claim on a sink's depth: the units a sale took out stay occupied
// until the fitted curve says the sink regrew. Absorption is not uniform across markets, and the
// sink's LISTING BREADTH (how many goods its market trades) is the readable signal of it — a
// market listing one good gives up its bid to a single tranche, while a broad hub takes the same
// tranche with a bid move too small to price against, and embargoing its depth for the shadow's
// whole life strands a lane that is still clearing.
//
// The scale multiplies the shadow's occupied depth. It composes with the recovery curve and the
// recovery floor rather than replacing either, so it sets the PRIOR a shadow starts from, never
// the mechanism that decays or releases it.
//
// Every unconfirmed input resolves to the uniform prior of 1.0, the conservative direction: a
// sink whose breadth we cannot read is charged the full claim. Nothing here can make a shadow
// occupy MORE depth than the uniform prior gives it, so it can only free depth held on a market
// positively confirmed to be broad.
type SinkDepthScaling struct {
	// Enabled applies the prior. Disabled is the kill switch: every sink is charged in full.
	Enabled bool
	// ThinListings is the breadth at or under which a sink keeps the full claim. It holds the
	// thin end of the curve where the protection is load-bearing: raising it widens the class
	// treated as a micro-market.
	ThinListings int
	// MinCrushScale floors the discount, so no market is treated as bottomless. It keeps a sale
	// large enough relative to the sink's tranche blocking at ANY breadth; without it a broad
	// enough market would absorb an unbounded dump for free.
	MinCrushScale float64
}

// The shipped fit. Both shape terms are refit per era and config overrides either.
const (
	// DefaultThinListings keeps the full claim on markets narrow enough that one tranche
	// plausibly clears their book.
	DefaultThinListings = 2
	// DefaultMinCrushScale bounds the discount an arbitrarily broad hub may earn.
	DefaultMinCrushScale = 0.10
)

// uniformCrushScale charges a sale its full units of sink depth.
const uniformCrushScale = 1.0

// DefaultSinkDepthScaling is the prior in force wherever no operator override replaces it, so a
// ledger built without a configured policy runs the shipped fit rather than a zero shape.
func DefaultSinkDepthScaling() SinkDepthScaling {
	return SinkDepthScaling{
		Enabled:       true,
		ThinListings:  DefaultThinListings,
		MinCrushScale: DefaultMinCrushScale,
	}
}

// CrushScale returns the multiplier on an executed shadow's occupied depth for a sink whose cached
// market lists `listings` goods. It returns the uniform prior whenever the policy is disabled, the
// breadth is unreadable (non-positive), the market is at or under the thin threshold, or a shape
// term is outside the range the model is defined on — an ill-formed knob must not silently
// un-protect every sink.
func (s SinkDepthScaling) CrushScale(listings int) float64 {
	if !s.Enabled || listings <= 0 {
		return uniformCrushScale
	}
	if s.ThinListings <= 0 || s.MinCrushScale <= 0 || s.MinCrushScale > uniformCrushScale {
		return uniformCrushScale
	}
	if listings <= s.ThinListings {
		return uniformCrushScale
	}
	scale := float64(s.ThinListings) / float64(listings)
	if scale < s.MinCrushScale {
		return s.MinCrushScale
	}
	return scale
}
