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
	selectorBranchKey                // Factory input-source selector branch, tagged onto the buy's ledger row
	constructionSupplyKey            // Marks a ProduceGood run as construction supply, exempt from resale-margin guards
	scanPolicyKey                    // Tour-scan load policy: recent-scan freshness gate + impact-sample rate
	liveScanRequiredKey              // Marks a market read as pre-commit money-guard verification, exempt from being served from store
	pairedScanKey                    // Marks a market read as the "after" half of an impact-measurement pair, exempt from the freshness veto
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

func NewOperationContext(containerID, operationType string) *OperationContext {
	if containerID == "" || operationType == "" {
		return nil
	}
	return &OperationContext{
		ContainerID:   containerID,
		OperationType: operationType,
	}
}

func (c *OperationContext) IsValid() bool {
	return c != nil && c.ContainerID != "" && c.OperationType != ""
}

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
// stocker, ...) passes through unchanged. There is deliberately NO
// arbitrage_worker→arbitrage or goods_factory_coordinator→factory case: no
// coordinator constructs an OperationContext with those raw types, so neither
// ever appears in live data (detectors.go concurs).
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
		// the tour is never measured against its own trades.
		return "tour"
	default:
		// Return as-is for unknown types
		return c.OperationType
	}
}

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

func SkipMarketRefreshFromContext(ctx context.Context) bool {
	if skip, ok := ctx.Value(skipMarketRefreshKey).(bool); ok {
		return skip
	}
	return false
}

// WithConstructionSupply marks the context as a construction-supply production run.
// The construction drain stamps it before driving ProduceGood so the shared engine sources a
// gate material by FABRICATION (buy inputs → feed factory → harvest output) and then delivers
// the harvested output to the construction site.
//
// Construction output is delivered to the gate, NEVER resold at a market, so the RESALE-margin
// guards (the chain-margin gate and the crushed-sink harvest guard) do not apply
// — those guards already exempt the old inputs-only construction model, and this flag extends the
// same exemption to the new harvest-into-hauler model. It scopes out ONLY the resale-margin
// checks: every INPUT buy still passes through the full money-guard stack (working-capital floor,
// concurrent spend cap, input price ceiling), which is unchanged
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

// WithSelectorBranch stamps the input-source selector's branch (ELIGIBLE | RESCUE |
// era-end | disabled) onto the context so the cargo-transaction recorder tags the resulting
// PURCHASE_CARGO ledger row's metadata with it. ONLY the factory input-buy path
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

// ScanPolicy is the scan-load policy a coordinator threads onto ctx. It governs
// two API-reducing gates on the SHARED market-scan path, and never the shipyard
// scan (which carries its own window):
//
//   - MaxScanAge: an arrival/decision scan whose CACHED market was updated within
//     this window reuses the cache instead of re-calling GetMarket — the redundant
//     re-scan killer (the measured "same hull re-scanning a market 4s apart", and
//     two scouts on overlapping routes both paying for one observation). It must
//     stay FAR below the freshness sizer's tightest per-activity SLA: it exists to
//     drop duplicate observations, never to set scan policy. 0 disables the gate.
//   - ImpactSampleRate: the FRACTION of trades on which the deliberate post-trade
//     impact scan (the paired before/after that records dP/P) still fires so the
//     analyst can refit the model per era (~1 day of pairs at 0.15 is plenty). A
//     non-sampled trade falls back to the MaxScanAge gate — one fresh scan for the
//     decision, no extra measurement scan. 1.0 = every trade instrumented
//     (the prior behavior); 0 = never (max API saving, no refit data).
//
// The zero value is INERT: absent from ctx, ScanPolicyFromContext returns ok=false
// and every scan caller runs its prior path byte-for-byte (deploy-safe: only a
// coordinator that stamps a policy changes behavior).
type ScanPolicy struct {
	MaxScanAge       time.Duration
	ImpactSampleRate float64
}

// WithScanPolicy stamps the scan-load policy onto ctx. The trade coordinators
// stamp the full policy; the scout tour stamps MaxScanAge only, so its arrival
// scans dedup against a market another hull just refreshed without ever
// enabling the trade-side impact instrumentation.
func WithScanPolicy(ctx context.Context, policy ScanPolicy) context.Context {
	return context.WithValue(ctx, scanPolicyKey, policy)
}

// ScanPolicyFromContext returns the stamped scan-load policy and ok=true, or the
// zero policy and ok=false when no trade coordinator stamped one (the default for
// every scout/CLI/other caller — the prior behavior).
func ScanPolicyFromContext(ctx context.Context) (ScanPolicy, bool) {
	policy, ok := ctx.Value(scanPolicyKey).(ScanPolicy)
	return policy, ok
}

// WithLiveScanRequired marks a market read as pre-commit verification for a money
// guard, which exempts it from the fleet's market-scan budget declining it into a
// cache read.
//
// Only FOUR call paths stamp it, and every one of them re-reads a price
// immediately before a buy or a sell commits and fails CLOSED if it cannot:
//
//   - the per-tranche sell floor and buy ceiling, which hold
//     the remainder rather than trade on a bid or ask they could not verify;
//   - the trade circuit's stale-ask abort, which exists because executing on a
//     cache that had moved realised large losses;
//   - the one-shot arb coordinator's min-margin guard, which aborts before the buy
//     when the source market cannot be re-read.
//
// Those guards exist PRECISELY because a cached price is not trustworthy enough to
// trade on. Serving one from store would not save a request — it would silently
// convert a live money guard into a stale one and re-open the losses each was
// written to stop, which RULINGS #4 forbids. So these reads are never declined.
// They are still METERED against the same allowance, so trade-critical
// verification squeezes discretionary scanning rather than being added on top of
// it, and one budget remains the honest total.
//
// Absent (every scouting, charting, manufacturing, contract, tour-arrival and CLI
// read) the market read is budgeted, which is the default that makes the budget
// hold for call sites nobody thought to classify.
func WithLiveScanRequired(ctx context.Context) context.Context {
	return context.WithValue(ctx, liveScanRequiredKey, true)
}

// LiveScanRequiredFromContext reports whether this market read is pre-commit money
// guard verification. Absent it returns false, so an unstamped read is paced.
func LiveScanRequiredFromContext(ctx context.Context) bool {
	required, ok := ctx.Value(liveScanRequiredKey).(bool)
	return ok && required
}

// WithPairedScan marks a market read as the "after" half of the scan-buy-scan
// price-impact pair the model is fitted from, which exempts it from the
// market-scan budget's freshness veto.
//
// It exempts that ONE rule and nothing else. For a paired measurement a fresh
// cache is not evidence the read is redundant — the cached row is the "before"
// observation the "after" is measured against, so freshness is the read's
// precondition. The read still needs an allowance token and still faces the
// value bar, because instrumentation is what a budget under pressure should shed
// first, and it is separately sampled upstream by impact_sample_rate.
//
// Only the SAMPLED post-trade impact scan stamps it. The same call site's other
// branch — re-scanning because the cached market went stale — is an ordinary
// budgeted decision scan and is left unstamped, so it is paced like every other
// discretionary read.
func WithPairedScan(ctx context.Context) context.Context {
	return context.WithValue(ctx, pairedScanKey, true)
}

// PairedScanFromContext reports whether this market read is the "after" half of
// an impact-measurement pair. Absent it returns false, so an unstamped read is
// subject to the freshness veto like every other budgeted read.
func PairedScanFromContext(ctx context.Context) bool {
	paired, ok := ctx.Value(pairedScanKey).(bool)
	return ok && paired
}
