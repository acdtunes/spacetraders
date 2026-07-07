package watchkeeper

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

const eraDateLayout = "2006-01-02"

type serverStatusSource interface {
	GetServerStatus(ctx context.Context) (*api.ServerStatus, error)
}

type openEraSource interface {
	FindOpenEra(ctx context.Context) (*persistence.EraModel, error)
}

// SetUniverseWatch wires the universe-reset detector's collaborators (the API
// status seam and the open-era source) without touching the constructor.
func (s *Supervisor) SetUniverseWatch(status serverStatusSource, eras openEraSource) {
	s.status = status
	s.eras = eras
}

// checkUniverseReset compares the live server resetDate against the open era on
// its cadence. On mismatch it halts the fleet (touch DISABLED) and mails the
// Admiral once. It fails quiet on every non-mismatch path and never clears the
// switch.
func (s *Supervisor) checkUniverseReset(ctx context.Context, now time.Time) {
	if !s.universeCheckDue(now) {
		return
	}
	status, err := s.status.GetServerStatus(ctx)
	if err != nil {
		fmt.Printf("watchkeeper: universe check: server status error: %v\n", err)
		return
	}
	s.lastUniverseCheck = now
	era, err := s.eras.FindOpenEra(ctx)
	if err != nil {
		fmt.Printf("watchkeeper: universe check: open era lookup error: %v\n", err)
		return
	}
	if era == nil {
		return
	}
	eraDate := ""
	if era.UniverseResetDate != nil {
		eraDate = era.UniverseResetDate.Format(eraDateLayout)
	}
	if eraDate == status.ResetDate {
		return
	}
	s.haltForUniverseReset(ctx, now, status.ResetDate, era.Name, eraDate)
}

func (s *Supervisor) universeCheckDue(now time.Time) bool {
	if s.lastUniverseCheck.IsZero() {
		return true
	}
	return now.Sub(s.lastUniverseCheck) >= time.Duration(s.cfg.UniverseCheckHours)*time.Hour
}

func (s *Supervisor) haltForUniverseReset(ctx context.Context, now time.Time, serverDate, eraName, eraDate string) {
	reason := fmt.Sprintf("universe reset detected %s: server resetDate %s != open era %s resetDate %s",
		now.Format(eraDateLayout), serverDate, eraName, eraDate)
	created, err := s.ws.TouchDisabled(reason)
	if err != nil {
		fmt.Printf("watchkeeper: universe check: touch DISABLED failed: %v\n", err)
		return
	}
	if !created {
		return
	}
	if s.gw == nil {
		return
	}
	if err := s.gw.SendMail(ctx, s.cfg.AdmiralAlias, "universe reset detected", reason); err != nil {
		fmt.Printf("watchkeeper: universe check: Admiral mail failed: %v\n", err)
	}
}
