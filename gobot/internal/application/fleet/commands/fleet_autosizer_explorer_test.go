package commands

import (
	"context"
	"errors"
	"testing"
)

// fakeOffGateSource is the double at the slice-B off-gate demand port (the frontier coordinator's
// OffGateDemand, adapted). Injected per test so the demand-gate can be exercised in isolation.
type fakeOffGateSource struct {
	demanded  bool
	wantCount int
	ok        bool
}

func (f *fakeOffGateSource) ExplorerDemand(_ context.Context, _ int) (bool, int, bool) {
	return f.demanded, f.wantCount, f.ok
}

// fakeExplorerFleet is the double at the explorer-pool count port (the hard-cap basis + shortfall
// Current).
type fakeExplorerFleet struct {
	count int
	err   error
}

func (f *fakeExplorerFleet) ExplorerCount(_ context.Context, _ int) (int, error) {
	return f.count, f.err
}

func armedParams() DemandParams { return DemandParams{ExplorerHullsEnabled: true, MaxExplorerHulls: 1} }

// ARMED + off-gate demand fired + no explorer owned ⇒ wants exactly ONE (Shortfall 1), Readable, and
// the realized-rate fields are LEFT UNSET (the explorer is payback-exempt).
func TestExplorer_ArmedAndDemandFired_WantsExactlyOne(t *testing.T) {
	p := NewExplorerDemandProvider(&fakeOffGateSource{demanded: true, wantCount: 1, ok: true}, &fakeExplorerFleet{count: 0})
	d, err := p.Demand(context.Background(), 1, armedParams())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !d.Readable {
		t.Fatalf("armed+demand-fired must be Readable; reason=%s", d.Reason)
	}
	if d.Demand != 1 || d.Current != 0 || d.Shortfall() != 1 {
		t.Fatalf("want Demand=1 Current=0 Shortfall=1, got Demand=%d Current=%d Shortfall=%d", d.Demand, d.Current, d.Shortfall())
	}
	if d.RateReadable || d.MarginalRate != 0 {
		t.Fatalf("explorer must leave realized-rate UNSET (payback-exempt), got RateReadable=%v MarginalRate=%v", d.RateReadable, d.MarginalRate)
	}
	if d.Class != HullClassExplorer {
		t.Fatalf("class must be explorer, got %q", d.Class)
	}
}

// DISARMED ⇒ ZERO demand even though off-gate demand is firing (the deploy-inert arming proof).
func TestExplorer_Disarmed_ZeroDemandEvenWithDemandFired(t *testing.T) {
	p := NewExplorerDemandProvider(&fakeOffGateSource{demanded: true, wantCount: 1, ok: true}, &fakeExplorerFleet{count: 0})
	params := DemandParams{ExplorerHullsEnabled: false, MaxExplorerHulls: 1} // DISARMED
	d, err := p.Demand(context.Background(), 1, params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Shortfall() != 0 || d.Demand != 0 {
		t.Fatalf("DISARMED must yield zero demand even with demand fired, got Demand=%d Shortfall=%d", d.Demand, d.Shortfall())
	}
}

// ARMED but NO off-gate demand ⇒ ZERO demand (no speculative buy). This is the demand-gate; removing
// the `if !demanded` short-circuit makes the want floor to 1 and this test fails (mutation guard).
func TestExplorer_NoOffGateDemand_ZeroDemandEvenArmed(t *testing.T) {
	p := NewExplorerDemandProvider(&fakeOffGateSource{demanded: false, wantCount: 0, ok: true}, &fakeExplorerFleet{count: 0})
	d, err := p.Demand(context.Background(), 1, armedParams())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Shortfall() != 0 || d.Demand != 0 {
		t.Fatalf("no off-gate demand must yield zero demand (no speculative buy), got Demand=%d Shortfall=%d", d.Demand, d.Shortfall())
	}
}

// HARD CAP via the pool: one explorer already owned ⇒ Demand capped at 1 and Current=1, so Shortfall
// is 0 (no second buy). The guard-stack fleet ceiling is the belt; this is the suspenders.
func TestExplorer_HardCap_NoSecondBuyWhenPoolAtCap(t *testing.T) {
	p := NewExplorerDemandProvider(&fakeOffGateSource{demanded: true, wantCount: 1, ok: true}, &fakeExplorerFleet{count: 1})
	d, err := p.Demand(context.Background(), 1, armedParams())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Current != 1 || d.Shortfall() != 0 {
		t.Fatalf("pool at cap must yield Shortfall 0 (no second explorer), got Demand=%d Current=%d Shortfall=%d", d.Demand, d.Current, d.Shortfall())
	}
}

// A signal that wants MANY explorers is clamped to the hard cap — the provider never over-wants.
func TestExplorer_SignalCountClampedToHardCap(t *testing.T) {
	p := NewExplorerDemandProvider(&fakeOffGateSource{demanded: true, wantCount: 99, ok: true}, &fakeExplorerFleet{count: 0})
	d, _ := p.Demand(context.Background(), 1, DemandParams{ExplorerHullsEnabled: true, MaxExplorerHulls: 1})
	if d.Demand != 1 {
		t.Fatalf("signal want 99 must clamp to hard cap 1, got Demand=%d", d.Demand)
	}
}

// FAIL CLOSED on an unreadable off-gate signal — a signal we could not read must never buy.
func TestExplorer_FailsClosed_OnUnreadableOffGate(t *testing.T) {
	p := NewExplorerDemandProvider(&fakeOffGateSource{demanded: true, wantCount: 1, ok: false}, &fakeExplorerFleet{count: 0})
	d, err := p.Demand(context.Background(), 1, armedParams())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Readable {
		t.Fatalf("unreadable off-gate signal must fail CLOSED (Readable=false), got Readable=true")
	}
}

// FAIL CLOSED on an unreadable explorer pool count — a mis-count could breach the hard cap, so an
// unknowable pool never buys.
func TestExplorer_FailsClosed_OnUnreadablePoolCount(t *testing.T) {
	p := NewExplorerDemandProvider(&fakeOffGateSource{demanded: true, wantCount: 1, ok: true}, &fakeExplorerFleet{err: errors.New("db down")})
	d, err := p.Demand(context.Background(), 1, armedParams())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Readable {
		t.Fatalf("unreadable pool count must fail CLOSED (Readable=false), got Readable=true")
	}
}
