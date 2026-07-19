package commands

import (
	"context"
	"fmt"
)

// The EXPLORER demand provider. It sizes the off-gate warp-exploration
// pool to slice-B's off-gate demand signal, DOUBLE-GATED so a bare deploy buys nothing:
//
//	(a) ARMED — explorer_hulls_enabled (default OFF). Disarmed ⇒ ZERO demand unconditionally. (The
//	    coordinator's classDisabled ALSO skips the whole class when disarmed, so in production this
//	    provider is not even called then; the check here is defense-in-depth so the provider is
//	    self-contained and directly proves "disarmed ⇒ no buy".)
//	(b) DEMAND — slice-B off-gate demand must be firing. No off-gate demand ⇒ ZERO demand (no
//	    speculative buy). BOTH gates must hold to raise ANY demand.
//
// The demand is HARD-CAPPED at MaxExplorerHulls (the class fleet ceiling, default 1): it never wants
// more than the cap whatever the signal's count says, and it reports the CURRENT explorer pool as
// Current so the shortfall — and the guard-stack fleet ceiling — refuse a second buy. It leaves the
// realized-rate fields UNSET: the explorer buys REACH not income, so EvaluateGuards exempts it from
// the era-payback/realized-rate guards (guardExplorerExempt). Every input fails CLOSED — an
// unreadable off-gate signal or an unreadable pool count yields Readable=false (no buy).

// OffGateDemandSource is the read side of slice-B's off-gate explorer demand signal (the frontier
// coordinator's OffGateDemand, adapted). ok=false ⇒ the signal is unreadable and the explorer pass
// fails CLOSED (no speculative buy on a signal we could not read).
type OffGateDemandSource interface {
	ExplorerDemand(ctx context.Context, playerID int) (demanded bool, wantCount int, ok bool)
}

// ExplorerFleetSource counts the player's existing explorer-dedicated hulls — the hard-cap basis and
// the shortfall's Current. An error fails the class CLOSED: an unknowable pool must never buy (a
// mis-count could breach the hard cap of 1).
type ExplorerFleetSource interface {
	ExplorerCount(ctx context.Context, playerID int) (int, error)
}

// ExplorerDemandProvider is the registered singleton demand provider for the explorer class. Its only
// collaborators are the off-gate demand source (read) and the explorer-pool counter (read); it holds
// no cross-tick state (every decision is derived fresh, RULINGS #2).
type ExplorerDemandProvider struct {
	offGate OffGateDemandSource
	fleet   ExplorerFleetSource
}

// NewExplorerDemandProvider wires the provider over its two read sources.
func NewExplorerDemandProvider(offGate OffGateDemandSource, fleet ExplorerFleetSource) *ExplorerDemandProvider {
	return &ExplorerDemandProvider{offGate: offGate, fleet: fleet}
}

// Class identifies this provider as the explorer sizer.
func (p *ExplorerDemandProvider) Class() HullClass { return HullClassExplorer }

// Demand reads the double-gated off-gate explorer demand for the player this tick. See the file
// comment for the two gates and the hard cap.
func (p *ExplorerDemandProvider) Demand(ctx context.Context, playerID int, params DemandParams) (ClassDemand, error) {
	// GATE (a) ARMING — disarmed ⇒ ZERO demand unconditionally (deploy-inert; defense-in-depth
	// beside classDisabled).
	if !params.ExplorerHullsEnabled {
		return ClassDemand{
			Class: HullClassExplorer, Readable: true, Demand: 0, Current: 0,
			Reason: "explorer class DISARMED (explorer_hulls_enabled=false) — zero demand, no buy",
		}, nil
	}

	// Fail CLOSED on an unreadable off-gate signal.
	demanded, wantCount, ok := p.offGate.ExplorerDemand(ctx, playerID)
	if !ok {
		return ClassDemand{
			Class: HullClassExplorer, Readable: false,
			Reason: "off-gate demand signal unreadable — fail closed (no buy)",
		}, nil
	}

	// GATE (b) DEMAND — no off-gate demand ⇒ ZERO demand (no speculative buy). This short-circuit is
	// load-bearing: the want floors to 1 below, so removing it would raise a speculative want of 1
	// (the no-off-gate-demand test is the mutation guard).
	if !demanded {
		return ClassDemand{
			Class: HullClassExplorer, Readable: true, Demand: 0, Current: 0,
			Reason: "no off-gate demand this tick — zero explorer demand (no speculative buy)",
		}, nil
	}

	// Current pool: the hard-cap basis + the shortfall's Current. Fail CLOSED on an unreadable count.
	current, err := p.fleet.ExplorerCount(ctx, playerID)
	if err != nil {
		return ClassDemand{
			Class: HullClassExplorer, Readable: false,
			Reason: fmt.Sprintf("explorer pool count unreadable: %v — fail closed", err),
		}, nil
	}

	// HARD CAP: never want more than the cap (default 1), whatever the signal's count says. A firing
	// off-gate signal always wants at least one explorer, so the want floors at 1.
	hardCap := params.MaxExplorerHulls
	if hardCap < 1 {
		hardCap = 1
	}
	want := wantCount
	if want < 1 {
		want = 1
	}
	if want > hardCap {
		want = hardCap
	}

	return ClassDemand{
		Class:    HullClassExplorer,
		Demand:   want,
		Current:  current,
		Readable: true,
		// MarginalRate/RateReadable LEFT UNSET — the explorer is payback-exempt (see EvaluateGuards).
		Reason: fmt.Sprintf("off-gate demand firing (signal wants %d, hard cap %d); explorer pool %d — exploration-justified, payback-exempt", wantCount, hardCap, current),
	}, nil
}
