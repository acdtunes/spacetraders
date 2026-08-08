package commands

import (
	"context"
	"strings"
	"sync"
)

// Shared test helpers for the fleet package. They were declared inside the fleet autosizer's test
// files; sp-5pclx deleted those with the coordinator, so the ones the SURVIVING tests still use were
// moved here rather than dropped.

// capturingLogger collects log lines and their "action" metadata so a tick's operator-facing
// messages can be asserted.
type capturingLogger struct {
	mu      sync.Mutex
	lines   []string
	actions []string
}

func (l *capturingLogger) Log(_, message string, metadata map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, message)
	if metadata != nil {
		if a, ok := metadata["action"].(string); ok {
			l.actions = append(l.actions, a)
		}
	}
}

func (l *capturingLogger) sawAction(action string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, a := range l.actions {
		if a == action {
			return true
		}
	}
	return false
}

func (l *capturingLogger) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// --- shared buy-path port fakes ---

type fakeTreasury struct {
	credits int64
	ok      bool
	err     error
}

func (f *fakeTreasury) Treasury(ctx context.Context, playerID int) (int64, bool, error) {
	return f.credits, f.ok, f.err
}

type fakeAPIUtil struct {
	pct float64
	ok  bool
	err error
}

func (f *fakeAPIUtil) UtilizationPct(ctx context.Context) (float64, bool, error) {
	return f.pct, f.ok, f.err
}

type fakeYardPrice struct {
	price    int64
	cheapest int64
	yard     string
	ok       bool
	err      error
}

func (f *fakeYardPrice) PriceFor(ctx context.Context, playerID int, class HullClass, shipType string, preferProximal bool) (int64, int64, string, bool, error) {
	return f.price, f.cheapest, f.yard, f.ok, f.err
}

// fakeHeavyCensus is the tag-independent owned-heavy census. err ⇒ unreadable ⇒ the heavy cap
// guard fails closed.
type fakeHeavyCensus struct {
	owned int
	err   error
}

func (f *fakeHeavyCensus) HeaviesOwned(_ context.Context, _ int) (int, error) {
	return f.owned, f.err
}

// fakeTypedYardPrice prices ONE named set of ship types and refuses every other, which is
// the whole point: a fake that answers the same price for any type makes the preferred
// type and the fallback INDISTINGUISHABLE, and a fallback that never fired would still
// pass. Every asked-for type is recorded so a test can pin the ORDER of the attempts.
type fakeTypedYardPrice struct {
	byType map[string]int64
	asked  []string
}

func (f *fakeTypedYardPrice) PriceFor(_ context.Context, _ int, _ HullClass, shipType string, _ bool) (int64, int64, string, bool, error) {
	f.asked = append(f.asked, shipType)
	price, priced := f.byType[shipType]
	if !priced {
		return 0, 0, "", false, nil // unpriceable at every reachable yard
	}
	return price, price, shipType + "-YARD", true, nil
}

// fakeHeavyYard is the SHARED heavy-target read behind the reservation: the yard the purchase
// path would target and its ask, NOT the cheapest ask on the map (sp-fwk8z).
//
// found doubles as both halves of the real answer — a priced target implies an open capability —
// because every existing test that sets it means "a heavy is buyable". The capability-open-but-
// unpriced state, which found cannot express, is exercised explicitly where it matters.
type fakeHeavyYard struct {
	price int64
	found bool
	// capabilityOpenUnpriced is the production state a priced-only fake cannot reach: a known
	// heavy yard whose ask nobody has read. It opens the capability with no usable price.
	capabilityOpenUnpriced bool
	err                    error
}

// The target comes back ALONGSIDE any error, mirroring fakeHeavyCensus (which returns owned
// alongside its own). A fake that zeroed its answer on failure would make the coordinator's
// `err == nil` gate untestable: with a zero target the reserve is 0 by ARITHMETIC rather than by
// the guard, so swallowing the error would leave the suite green while a blind price read held
// treasury against a number nobody could see.
func (f *fakeHeavyYard) HeavyTarget(_ context.Context, _ int) (HeavyTargetYard, error) {
	if !f.found {
		return HeavyTargetYard{CapabilityOpen: f.capabilityOpenUnpriced}, f.err
	}
	return HeavyTargetYard{CapabilityOpen: true, Priced: true, WaypointSymbol: "X1-QR78-AE4F", PurchasePrice: f.price}, f.err
}

// intPtr is the *int helper for HeavyCap, whose pointer-ness is what lets an explicit 0
// (operator hold) be told from unset.
func intPtr(v int) *int { return &v }
