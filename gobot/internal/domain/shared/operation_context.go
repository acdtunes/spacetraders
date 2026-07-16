package shared

import (
	"context"
	"time"
)

// contextKey is a type for context keys to avoid collisions
type contextKey int

const (
	operationContextKey   contextKey = iota
	skipMarketRefreshKey             // Skip market refresh after cargo transactions (optimization)
	selectorBranchKey                // Factory input-source selector branch, tagged onto the buy's ledger row (sp-br0m)
	constructionSupplyKey            // Marks a ProduceGood run as construction supply, exempt from resale-margin guards (sp-qmp8)
	scanPolicyKey                    // Tour-scan load policy: recent-scan freshness gate + impact-sample rate (sp-v34b)
)

// OperationContext provides traceability from high-level operations (containers)
// down to individual financial transactions.
//
// This enables profit/loss tracking at the operation level by linking all
// child transactions (refuel, cargo purchases, cargo sales, etc.) back to
// their parent operation container.
//
// Example usage:
//
//	context := NewOperationContext("contract-work-COOPER-23-abc123", "contract_workflow")
//	refuelCmd := &RefuelShipCommand{
//	    ShipSymbol: "COOPER-23",
//	    Context: context,
//	}
//
// When the refuel handler records a ledger transaction, it will include
// the container ID as related_entity_id, enabling queries like:
//
//	SELECT SUM(amount) FROM transactions
//	WHERE related_entity_type = 'container'
//	  AND related_entity_id = 'contract-work-COOPER-23-abc123'
type OperationContext struct {
	// ContainerID is the unique identifier of the container running this operation
	// Examples: "contract-work-COOPER-23-abc123", "goods_factory-ELECTRONICS-def456"
	ContainerID string

	// OperationType is the type of operation being performed
	// Examples: "contract_workflow", "goods_factory_coordinator", "mining_worker"
	OperationType string
}

// NewOperationContext creates a new operation context with validation
func NewOperationContext(containerID, operationType string) *OperationContext {
	if containerID == "" || operationType == "" {
		return nil
	}
	return &OperationContext{
		ContainerID:   containerID,
		OperationType: operationType,
	}
}

// IsValid returns true if the context has required fields
func (c *OperationContext) IsValid() bool {
	return c != nil && c.ContainerID != "" && c.OperationType != ""
}

// String returns a human-readable representation of the context
func (c *OperationContext) String() string {
	if c == nil {
		return "<no context>"
	}
	return c.OperationType + ":" + c.ContainerID
}

// NormalizedOperationType converts command_type to normalized operation_type for ledger
// Maps from container command types to user-facing operation types:
//   - contract_workflow → contract
//   - balance_ship_position → fleet rebalancing
//   - manufacturing_worker → manufacturing
//   - tour_run → tour
//
// Every other raw type (factory_workflow, trade_route, construction_supply,
// stocker, ...) passes through unchanged. The former arbitrage_worker→arbitrage
// and goods_factory_coordinator→factory cases were removed as dead (sp-xdr6): no
// coordinator constructs an OperationContext with those raw types, so they never
// appeared in live data (category audit 2026-07-14 F4; detectors.go concurs).
func (c *OperationContext) NormalizedOperationType() string {
	if c == nil || c.OperationType == "" {
		return ""
	}

	switch c.OperationType {
	case "contract_workflow":
		return "contract"
	case "balance_ship_position":
		return "fleet rebalancing"
	case "manufacturing_worker":
		return "manufacturing"
	case "tour_run":
		// The tour_run container's buy/sell legs; the graduation baseline
		// (tour_report.go) excludes these rows via operation_type <> 'tour' so
		// the tour is never measured against its own trades (sp-lgnh).
		return "tour"
	default:
		// Return as-is for unknown types
		return c.OperationType
	}
}

// WithOperationContext adds an operation context to the context
func WithOperationContext(ctx context.Context, opCtx *OperationContext) context.Context {
	return context.WithValue(ctx, operationContextKey, opCtx)
}

// OperationContextFromContext extracts the operation context from context, or returns nil if not found
func OperationContextFromContext(ctx context.Context) *OperationContext {
	if opCtx, ok := ctx.Value(operationContextKey).(*OperationContext); ok {
		return opCtx
	}
	return nil
}

// WithSkipMarketRefresh returns a context that signals to skip market refresh after cargo transactions.
// This optimization reduces API calls for operations that manage their own market scanning.
func WithSkipMarketRefresh(ctx context.Context) context.Context {
	return context.WithValue(ctx, skipMarketRefreshKey, true)
}

// SkipMarketRefreshFromContext returns true if the context has skip market refresh flag set.
func SkipMarketRefreshFromContext(ctx context.Context) bool {
	if skip, ok := ctx.Value(skipMarketRefreshKey).(bool); ok {
		return skip
	}
	return false
}

// WithConstructionSupply marks the context as a construction-supply production run (sp-qmp8).
// The construction drain stamps it before driving ProduceGood so the shared engine sources a
// gate material by FABRICATION (buy inputs → feed factory → harvest output) and then delivers
// the harvested output to the construction site.
//
// Construction output is delivered to the gate, NEVER resold at a market, so the RESALE-margin
// guards (the sp-iv65 chain-margin gate and the bp6f #3 crushed-sink harvest guard) do not apply
// — those guards already exempt the old inputs-only construction model, and this flag extends the
// same exemption to the new harvest-into-hauler model. It scopes out ONLY the resale-margin
// checks: every INPUT buy still passes through the full money-guard stack (working-capital floor
// sp-9aoc, concurrent spend cap sp-w3he, input price ceiling sp-iv65), which is unchanged
// (RULINGS #4 — input buys still go through the guard stack).
func WithConstructionSupply(ctx context.Context) context.Context {
	return context.WithValue(ctx, constructionSupplyKey, true)
}

// ConstructionSupplyFromContext reports whether the context was marked as a construction-supply
// run by WithConstructionSupply. Absent (the default for every factory/tour/arb caller) it
// returns false, leaving the resale-margin guards fully in force for resale production.
func ConstructionSupplyFromContext(ctx context.Context) bool {
	supply, ok := ctx.Value(constructionSupplyKey).(bool)
	return ok && supply
}

// WithSelectorBranch stamps the a5j7 input-source selector's branch (ELIGIBLE | RESCUE |
// era-end | disabled) onto the context so the cargo-transaction recorder tags the resulting
// PURCHASE_CARGO ledger row's metadata with it (sp-br0m). ONLY the factory input-buy path
// (production_executor.buyGood) stamps this; every other cargo caller (trade, tour, arb,
// contract delivery, refuel, CLI, the fabricated-output harvest) leaves it unset, so their
// recorded rows are byte-identical to before. The tag makes A1 (supply-first compliance)
// gradable straight from the ledger — an ELIGIBLE buy is a healthy supply-first pick, a RESCUE
// buy is the legal single-source-degraded exception — and arms the rescue-rate mis-siting
// tripwire (a chain buying >20% RESCUE is mis-sited), which are otherwise indistinguishable
// once a buy is recorded.
func WithSelectorBranch(ctx context.Context, branch string) context.Context {
	return context.WithValue(ctx, selectorBranchKey, branch)
}

// SelectorBranchFromContext returns the selector branch stamped by WithSelectorBranch and
// ok=true, or ("", false) when the caller is not a tagged factory input buy (an empty stamp
// is treated as absent, so a blank tag never lands in the ledger metadata).
func SelectorBranchFromContext(ctx context.Context) (string, bool) {
	if branch, ok := ctx.Value(selectorBranchKey).(string); ok && branch != "" {
		return branch, true
	}
	return "", false
}

// ScanPolicy is the sp-v34b tour-scan load policy a TRADE coordinator (tour /
// trade-route) threads onto ctx to throttle the deliberate price-impact
// instrumentation the sp-tl68 model is fitted from. It governs two API-reducing
// gates on the SHARED scan path WITHOUT touching the freshness-scout recovery path
// (which never stamps a policy, so its scans stay ungated — the recovery/decay
// dataset is unaffected) or the shipyard scan:
//
//   - MaxScanAge: an arrival/decision scan whose CACHED market was updated within
//     this window reuses the cache instead of re-calling GetMarket — the redundant
//     re-scan killer (the measured "same hull re-scanning a market 4s apart"). 0
//     disables the gate (always scan — pre-sp-v34b behavior).
//   - ImpactSampleRate: the FRACTION of trades on which the deliberate post-trade
//     impact scan (the paired before/after that records dP/P) still fires so the
//     analyst can refit the model per era (~1 day of pairs at 0.15 is plenty). A
//     non-sampled trade falls back to the MaxScanAge gate — one fresh scan for the
//     decision, no extra measurement scan. 1.0 = every trade instrumented
//     (pre-sp-v34b behavior); 0 = never (max API saving, no refit data).
//
// The zero value is INERT: absent from ctx, ScanPolicyFromContext returns ok=false
// and every scan caller runs its pre-sp-v34b path byte-for-byte (deploy-safe: only a
// coordinator that stamps a policy changes behavior).
type ScanPolicy struct {
	MaxScanAge       time.Duration
	ImpactSampleRate float64
}

// WithScanPolicy stamps the sp-v34b scan-load policy onto ctx. Only the trade
// coordinators stamp it; the scout tour deliberately does NOT, so its recovery scans
// remain ungated.
func WithScanPolicy(ctx context.Context, policy ScanPolicy) context.Context {
	return context.WithValue(ctx, scanPolicyKey, policy)
}

// ScanPolicyFromContext returns the stamped scan-load policy and ok=true, or the
// zero policy and ok=false when no trade coordinator stamped one (the default for
// every scout/CLI/other caller — pre-sp-v34b behavior).
func ScanPolicyFromContext(ctx context.Context) (ScanPolicy, bool) {
	policy, ok := ctx.Value(scanPolicyKey).(ScanPolicy)
	return policy, ok
}
