package commands

import "context"

// This file is the tour-alignment signal source for SCORE (sp-vdld M3). The design reads the
// C1 stock-draw signal WHERE AVAILABLE and falls back to tour-pass-by throughput where not
// (build against both — C1 deploys on the same boundary). TourAlignmentService implements
// exactly that precedence over two narrow ports; the daemon adapters that satisfy them are
// wired in M7.
//
// SEAM NOTE (from exploration): the C1 stock-draw signal (tour_factory_good_acquisition_cost)
// is a write-only Prometheus gauge with NO in-process read path today, so the daemon's
// StockDrawReader adapter returns available=false and this service falls back to
// tour_leg_telemetry throughput. The precedence is built and tested now so that when a
// persisted stock-draw read path lands, only the adapter changes — this logic already prefers it.

// StockDrawReader reads the C1 stock-draw rate for a factory good (how fast tours withdraw the
// good from planner-visible stock). available=false means no stock-draw signal exists for the
// good/system (the current daemon adapter always returns false — see the seam note), which
// triggers the telemetry fallback.
type StockDrawReader interface {
	StockDrawRate(ctx context.Context, playerID int, good, system string) (rate float64, available bool, err error)
}

// TourThroughputReader reads realized tour pass-by throughput for a good in a system from
// tour_leg_telemetry (the fallback tour-pull signal).
type TourThroughputReader interface {
	TourThroughput(ctx context.Context, playerID int, good, system string) (float64, error)
}

// TourAlignmentService implements TourAlignmentProvider: prefer the C1 stock-draw signal,
// fall back to tour throughput. stock is optional (nil → always fall back); throughput is
// required.
type TourAlignmentService struct {
	stock      StockDrawReader
	throughput TourThroughputReader
}

// NewTourAlignmentService wires the alignment source. A nil stock reader disables the C1
// preference and always uses the throughput fallback.
func NewTourAlignmentService(stock StockDrawReader, throughput TourThroughputReader) *TourAlignmentService {
	return &TourAlignmentService{stock: stock, throughput: throughput}
}

// Alignment returns the tour-pull signal (>= 0): the C1 stock-draw rate where available, else
// the tour throughput. A negative reading is clamped to 0 (neutral).
func (s *TourAlignmentService) Alignment(ctx context.Context, playerID int, good, system string) (float64, error) {
	if s.stock != nil {
		if rate, available, err := s.stock.StockDrawRate(ctx, playerID, good, system); err == nil && available {
			return nonNegative(rate), nil
		}
	}
	tp, err := s.throughput.TourThroughput(ctx, playerID, good, system)
	if err != nil {
		return 0, err
	}
	return nonNegative(tp), nil
}

func nonNegative(v float64) float64 {
	if v < 0 {
		return 0
	}
	return v
}
