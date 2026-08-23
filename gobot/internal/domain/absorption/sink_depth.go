package absorption

// SinkDepthScaling is the depth-conditioned crush prior applied to an EXECUTED
// recovery shadow. A shadow is a QUANTITY-space claim on a sink's depth: the units a
// sale took out stay occupied until the fitted curve says the sink regrew. Charging
// every sink the same claim per unit sold is what over-penalizes deep markets —
// absorption is not uniform across markets, and the sink's LISTING BREADTH (how many
// goods its market trades) is the readable signal of it. A market listing one good is a
// micro-market whose bid a single tranche takes off the board; a broad hub absorbs the
// same tranche with a bid move too small to price against, and holding its depth
// embargoed for the shadow's whole life strands a lane that is still clearing.
//
// The scale multiplies the shadow's occupied depth. It composes with the recovery curve
// and the recovery floor rather than replacing either, so this changes the PRIOR a
// shadow starts from, never the mechanism that decays or releases it.
//
// Every unconfirmed input resolves to the uniform prior of 1.0, which is the
// conservative direction — a sink whose breadth we cannot read is charged the full
// claim. Nothing here can make a shadow occupy MORE depth than the uniform model gave
// it, so this can only ever free depth the model held on a market it could positively
// confirm is broad.
type SinkDepthScaling struct {
	// Enabled arms the prior. Disabled reproduces the uniform model exactly.
	Enabled bool
	// ThinListings is the breadth at or under which a sink keeps the full claim. It
	// holds the thin end of the curve where the protection is load-bearing: raising it
	// widens the class treated as a micro-market.
	ThinListings int
	// MinCrushScale floors the discount, so no market is ever treated as bottomless. It
	// is what keeps a sale large enough relative to the sink's tranche blocking at ANY
	// breadth; without it a broad enough market would absorb an unbounded dump for free.
	MinCrushScale float64
}

// Shipped fit. Both are refit knobs (config overrides them), and these are the
// documented fallback a zero/absent value resolves to.
const (
	// DefaultThinListings keeps the full claim on markets narrow enough that one
	// tranche plausibly clears their book.
	DefaultThinListings = 2
	// DefaultMinCrushScale bounds the discount an arbitrarily broad hub may earn.
	DefaultMinCrushScale = 0.10
)

// uniformCrushScale is the pre-refit prior: a sale claims its full units of sink depth.
const uniformCrushScale = 1.0

// CrushScale returns the multiplier on an executed shadow's occupied depth for a sink
// whose cached market lists `listings` goods. It returns the uniform prior whenever the
// policy is disabled, the breadth is unreadable (non-positive), the market is at or
// under the thin threshold, or a shape term is outside the range the model is defined
// on — an ill-formed knob must not silently un-protect every sink.
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
