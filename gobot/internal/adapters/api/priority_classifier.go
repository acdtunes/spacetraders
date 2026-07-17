package api

import "context"

// Priority is the scheduling tier of an API call when priority scheduling is
// enabled (see priorityScheduler). A higher tier acquires a CONTENDED rate-limit
// token ahead of a lower tier. The tier only affects the ORDER in which waiting
// acquirers are served under saturation; it never changes the rate, the burst, or
// the refill of the underlying token bucket, so total throughput is identical to
// the un-prioritised path.
type Priority int

const (
	// PriorityLow is for deferrable status/discovery reads (Get Agent, Get
	// Construction, Get Ship, Get Jump Gate, Get Shipyard, Get Waypoint, List*).
	// Under saturation these may wait behind trade-critical calls. Bounded aging
	// (priorityScheduler.agingWindow) guarantees they are never starved.
	PriorityLow Priority = iota

	// PriorityNormal is the default tier: necessary overhead that is NOT a
	// deferrable poll (Navigate, Dock, Orbit, Refuel, Get Market, Get Cooldown,
	// ...) AND every unknown/unclassified endpoint. Unknown endpoints are never
	// treated as LOW (so they can never be accidentally starved) and never as HIGH
	// (so they can never wrongly jump the queue).
	PriorityNormal

	// PriorityHigh is for trade-critical calls (Buy Cargo, Sell Cargo) and for any
	// call a caller explicitly marks trade-blocking via WithPriority — e.g. the
	// dock/navigate/orbit steps enabling an imminent trade, or a Get Agent read a
	// spend-gate is blocked on.
	PriorityHigh
)

func (p Priority) String() string {
	switch p {
	case PriorityLow:
		return "LOW"
	case PriorityHigh:
		return "HIGH"
	default:
		return "NORMAL"
	}
}

type priorityContextKey struct{}

// WithPriority tags ctx with an explicit scheduling priority that OVERRIDES
// endpoint-based classification. Callers on a known trade-critical path use it to
// promote a call to HIGH even when its endpoint would otherwise classify LOW or
// NORMAL (the "explicitly marked trade-blocking" case). The tag is completely
// inert unless priority scheduling is enabled (default OFF).
func WithPriority(ctx context.Context, p Priority) context.Context {
	return context.WithValue(ctx, priorityContextKey{}, p)
}

func priorityFromContext(ctx context.Context) (Priority, bool) {
	p, ok := ctx.Value(priorityContextKey{}).(Priority)
	return p, ok
}

// highPriorityEndpoints are the trade-critical calls that move credits: buying
// and selling cargo. Classified by the human-readable name produced by
// apiEndpointClassifier.
var highPriorityEndpoints = map[string]struct{}{
	"Buy Cargo":  {},
	"Sell Cargo": {},
}

// lowPriorityEndpoints are the deferrable status/discovery reads named in the
// API-optimization analysis. They are the ONLY tier that can be deprioritised, so
// the set is kept conservative — anything not listed here (and not HIGH) stays
// NORMAL.
var lowPriorityEndpoints = map[string]struct{}{
	"Get Agent":        {},
	"Get Construction": {},
	"Get Ship":         {},
	"Get Jump Gate":    {},
	"Get Shipyard":     {},
	"Get Waypoint":     {},
	"List Ships":       {},
	"List Systems":     {},
	"List Waypoints":   {},
	"List Contracts":   {},
	"List Factions":    {},
}

// priorityForRequest resolves the scheduling priority of an API call.
// Precedence: explicit ctx override (WithPriority) > endpoint classification >
// NORMAL default.
func priorityForRequest(ctx context.Context, endpoint string) Priority {
	if p, ok := priorityFromContext(ctx); ok {
		return p
	}
	return priorityForEndpoint(endpoint)
}

// priorityForEndpoint classifies by the human-readable endpoint name.
// Trade-critical -> HIGH; deferrable status poll -> LOW; everything else,
// including any unknown/unclassified endpoint -> NORMAL.
func priorityForEndpoint(endpoint string) Priority {
	if _, ok := highPriorityEndpoints[endpoint]; ok {
		return PriorityHigh
	}
	if _, ok := lowPriorityEndpoints[endpoint]; ok {
		return PriorityLow
	}
	return PriorityNormal
}
