package commands

import (
	"context"
	"errors"
	"testing"
)

type fakeHeavySources struct {
	heavies    int
	heaviesErr error
	lanes      int
	lanesOK    bool
	lanesErr   error
}

func (f *fakeHeavySources) HeavyCount(ctx context.Context, playerID int) (int, error) {
	return f.heavies, f.heaviesErr
}
func (f *fakeHeavySources) UnservedLaneCount(ctx context.Context, playerID int) (int, bool, error) {
	return f.lanes, f.lanesOK, f.lanesErr
}

// One wanted hull per unserved profitable lane beyond the current pool.
func TestComputeHeavyDemand_OneHullPerUnservedLane(t *testing.T) {
	d := computeHeavyDemand(heavyDemandInputs{CurrentHeavies: 6, UnservedLanes: 3})
	if d.Demand != 9 || d.Current != 6 {
		t.Fatalf("demand/current = %d/%d, want 9/6", d.Demand, d.Current)
	}
	if d.Shortfall() != 3 {
		t.Fatalf("shortfall = %d, want 3", d.Shortfall())
	}
}

// No unserved lanes → no shortfall (the pool already covers demand).
func TestComputeHeavyDemand_NoUnservedNoShortfall(t *testing.T) {
	d := computeHeavyDemand(heavyDemandInputs{CurrentHeavies: 8, UnservedLanes: 0})
	if d.Shortfall() != 0 {
		t.Fatalf("shortfall = %d, want 0", d.Shortfall())
	}
}

// A negative unserved count (defensive) never shrinks the pool — the autosizer only grows.
func TestComputeHeavyDemand_NegativeUnservedClamped(t *testing.T) {
	d := computeHeavyDemand(heavyDemandInputs{CurrentHeavies: 8, UnservedLanes: -3})
	if d.Demand != 8 || d.Shortfall() != 0 {
		t.Fatalf("negative unserved must clamp to 0: demand=%d shortfall=%d", d.Demand, d.Shortfall())
	}
}

func TestHeavyProvider_ReadsAndSizes(t *testing.T) {
	src := &fakeHeavySources{heavies: 6, lanes: 2, lanesOK: true}
	p := NewHeavyDemandProvider(src)
	if p.Class() != HullClassHeavy {
		t.Fatalf("class = %q, want heavy", p.Class())
	}
	d, err := p.Demand(context.Background(), 1, DemandParams{})
	if err != nil {
		t.Fatalf("Demand error: %v", err)
	}
	if d.Demand != 8 || d.Current != 6 {
		t.Fatalf("demand/current = %d/%d, want 8/6", d.Demand, d.Current)
	}
	if !d.Readable {
		t.Fatalf("expected Readable true, got %v", d.Readable)
	}
}

func TestHeavyProvider_HeavyCountError_FailsClosed(t *testing.T) {
	src := &fakeHeavySources{heaviesErr: errors.New("ships unreadable"), lanes: 5, lanesOK: true}
	p := NewHeavyDemandProvider(src)
	d, err := p.Demand(context.Background(), 1, DemandParams{})
	if err != nil {
		t.Fatalf("read miss must fail closed, not error the tick; got %v", err)
	}
	if d.Readable {
		t.Fatalf("unreadable heavy count must yield Readable=false")
	}
}

// THE SEAM: if the unserved-lane signal has no read path (readable=false), heavies fail CLOSED —
// no lane signal, no buy — never wrongly bought while the seam is unwired.
func TestHeavyProvider_UnreadableLaneSignal_FailsClosed(t *testing.T) {
	src := &fakeHeavySources{heavies: 6, lanes: 0, lanesOK: false} // solver surface not readable
	p := NewHeavyDemandProvider(src)
	d, _ := p.Demand(context.Background(), 1, DemandParams{})
	if d.Readable {
		t.Fatalf("an unreadable unserved-lane signal must fail closed (Readable=false) — the banked seam")
	}
}

func TestHeavyProvider_LaneCountError_FailsClosed(t *testing.T) {
	src := &fakeHeavySources{heavies: 6, lanesErr: errors.New("solver query failed")}
	p := NewHeavyDemandProvider(src)
	d, _ := p.Demand(context.Background(), 1, DemandParams{})
	if d.Readable {
		t.Fatalf("a lane-count read error must fail closed")
	}
}
