package navigation

import (
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// MarkRetiring records the operator's retirement mark. Idempotent: a re-mark keeps the first.
func (s *Ship) MarkRetiring(clock shared.Clock) {
	if s.retiringAt != nil {
		return
	}
	at := clock.Now()
	s.retiringAt = &at
}

// CancelRetirement returns the hull to normal service.
func (s *Ship) CancelRetirement() { s.retiringAt = nil }

// IsRetiring reports the operator's retirement mark.
func (s *Ship) IsRetiring() bool { return s.retiringAt != nil }

// RetiringAt is when the mark was recorded, nil in normal service.
func (s *Ship) RetiringAt() *time.Time { return s.retiringAt }

// SetRetiringAt loads the persisted mark; prefer MarkRetiring/CancelRetirement.
func (s *Ship) SetRetiringAt(at *time.Time) { s.retiringAt = at }

// RetirementDrained is the scrap-ready test. It reads the live hold, not a finished tour: a
// tour can report success having traded nothing, which must never read as drained.
func (s *Ship) RetirementDrained() bool { return s.IsRetiring() && s.CargoUnits() == 0 }
