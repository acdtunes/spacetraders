package commands

import (
	"context"
	"errors"
	"testing"
)

type fakeStockDrawReader struct {
	rate      float64
	available bool
	err       error
}

func (f *fakeStockDrawReader) StockDrawRate(ctx context.Context, playerID int, good, system string) (float64, bool, error) {
	return f.rate, f.available, f.err
}

type fakeThroughputReader struct {
	value float64
	err   error
	calls int
}

func (f *fakeThroughputReader) TourThroughput(ctx context.Context, playerID int, good, system string) (float64, error) {
	f.calls++
	return f.value, f.err
}

// C1 stock-draw is PREFERRED when available; the throughput fallback is not consulted.
func TestAlignment_PrefersStockDrawWhenAvailable(t *testing.T) {
	stock := &fakeStockDrawReader{rate: 5.0, available: true}
	tp := &fakeThroughputReader{value: 99.0}
	got, err := NewTourAlignmentService(stock, tp).Alignment(context.Background(), 1, "ELEC", "X1-AA")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 5.0 {
		t.Errorf("signal = %v, want stock-draw rate 5.0", got)
	}
	if tp.calls != 0 {
		t.Errorf("throughput fallback must not be consulted when stock-draw is available (calls=%d)", tp.calls)
	}
}

// When the stock-draw signal is unavailable (the daemon adapter's current reality), fall back
// to tour throughput.
func TestAlignment_FallsBackToThroughputWhenStockUnavailable(t *testing.T) {
	stock := &fakeStockDrawReader{available: false}
	tp := &fakeThroughputReader{value: 12.0}
	got, err := NewTourAlignmentService(stock, tp).Alignment(context.Background(), 1, "ELEC", "X1-AA")
	if err != nil || got != 12.0 {
		t.Errorf("signal = %v (err %v), want throughput 12.0", got, err)
	}
}

// A stock-draw read error also falls back to throughput (never fails the score on the C1 read).
func TestAlignment_FallsBackOnStockError(t *testing.T) {
	stock := &fakeStockDrawReader{err: errors.New("stock read blip")}
	tp := &fakeThroughputReader{value: 7.0}
	got, err := NewTourAlignmentService(stock, tp).Alignment(context.Background(), 1, "ELEC", "X1-AA")
	if err != nil || got != 7.0 {
		t.Errorf("signal = %v (err %v), want throughput 7.0 fallback", got, err)
	}
}

// A nil stock reader (no C1 preference wired) always uses throughput.
func TestAlignment_NilStockUsesThroughput(t *testing.T) {
	tp := &fakeThroughputReader{value: 4.0}
	got, err := NewTourAlignmentService(nil, tp).Alignment(context.Background(), 1, "ELEC", "X1-AA")
	if err != nil || got != 4.0 {
		t.Errorf("signal = %v (err %v), want throughput 4.0", got, err)
	}
}

// A throughput error propagates (the caller — SCORE — treats it as neutral 0).
func TestAlignment_ThroughputErrorPropagates(t *testing.T) {
	tp := &fakeThroughputReader{err: errors.New("telemetry down")}
	got, err := NewTourAlignmentService(nil, tp).Alignment(context.Background(), 1, "ELEC", "X1-AA")
	if err == nil {
		t.Fatal("expected throughput error to propagate")
	}
	if got != 0 {
		t.Errorf("signal on error = %v, want 0", got)
	}
}

// Negative readings are clamped to 0 (neutral).
func TestAlignment_ClampsNegative(t *testing.T) {
	tp := &fakeThroughputReader{value: -3.0}
	got, _ := NewTourAlignmentService(nil, tp).Alignment(context.Background(), 1, "ELEC", "X1-AA")
	if got != 0 {
		t.Errorf("negative signal = %v, want clamped 0", got)
	}
}
