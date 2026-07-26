package commands

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// scriptedDormancy is the tour's dormancy stub. It is deliberately
// ADVERSARIAL where scripted so: it can return a wrong dormant=true ALONGSIDE
// an error, so a consumer that trusts the value over the error parks a probe
// it owed a scan. onCheck snapshots the world (API/mediator counters) at every
// read, proving what had — and had not — happened while dormant.
type scriptedDormancy struct {
	calls   int
	script  func(call int) (bool, error)
	onCheck func()

	// hotScript scripts the stage-2 hot-set read of the same seam; nil reads
	// as "no restriction" so every dormancy-only test keeps its full circuit.
	// Like script, it can return a WRONG non-empty list ALONGSIDE an error.
	hotCalls  int
	hotScript func(call int) ([]string, error)
}

func (s *scriptedDormancy) IsDormant(context.Context, int, string) (bool, error) {
	s.calls++
	if s.onCheck != nil {
		s.onCheck()
	}
	return s.script(s.calls)
}

func (s *scriptedDormancy) HotWaypoints(context.Context, int, string) ([]string, error) {
	s.hotCalls++
	if s.hotScript == nil {
		return nil, nil
	}
	return s.hotScript(s.hotCalls)
}

// countingTourMediator counts navigation sends — the multi-market tour's whole
// observable API surface, since every leg is one NavigateRouteCommand.
type countingTourMediator struct {
	sends int
}

func (m *countingTourMediator) Send(context.Context, mediator.Request) (mediator.Response, error) {
	m.sends++
	return &shipNav.NavigateRouteResponse{Status: "completed"}, nil
}

func (m *countingTourMediator) Register(reflect.Type, mediator.RequestHandler) error { return nil }

func (m *countingTourMediator) RegisterMiddleware(mediator.Middleware) {}

// A dormant post parks the stationary probe in place: for the entire hold —
// three full scan intervals here — the tour performs ZERO market reads, then
// resumes and scans the moment the rotation clears the bit.
func TestScoutTour_DormantStationaryPost_ParksWithZeroAPIThenResumes(t *testing.T) {
	api := &countingScoutAPI{}
	marketScanner := ship.NewMarketScanner(api, &fakeMarketStore{}, nil, nil)
	clock := &shared.MockClock{CurrentTime: time.Now()}
	h := NewScoutTourHandler(&fakeTourShipRepo{ship: scoutAt(t, scoutedYard)}, nil, marketScanner, nil, clock)

	var getsAtCheck []int
	reader := &scriptedDormancy{
		script:  func(call int) (bool, error) { return call <= 3, nil },
		onCheck: func() { getsAtCheck = append(getsAtCheck, api.marketGets) },
	}
	h.SetDormancyReader(reader)

	before := clock.CurrentTime
	_, err := h.Handle(tourCtx(), &ScoutTourCommand{
		PlayerID:           shared.MustNewPlayerID(1),
		ShipSymbol:         "PROBE-1",
		Markets:            []string{scoutedYard},
		Iterations:         1,
		ScanInterval:       5 * time.Minute,
		StartJitterMaxSecs: 1,
	})
	require.NoError(t, err)

	require.NotZero(t, reader.calls, "the tour must consult dormancy at circuit start")
	for i, gets := range getsAtCheck {
		require.Zerof(t, gets, "check %d: a parked probe made a market read", i+1)
	}
	require.Equal(t, 1, api.marketGets, "the tour resumes and scans once the bit clears")
	require.GreaterOrEqual(t, clock.CurrentTime.Sub(before), 15*time.Minute,
		"three dormant checks sleep three full scan intervals in place")
}

// A dormant post parks the multi-market circuit before its first leg: zero
// navigation sends for the whole hold, then the full circuit flies when the
// bit clears.
func TestScoutTour_DormantMultiMarketCircuit_HoldsBeforeFirstLeg(t *testing.T) {
	med := &countingTourMediator{}
	api := &countingScoutAPI{}
	marketScanner := ship.NewMarketScanner(api, &fakeMarketStore{}, nil, nil)
	clock := &shared.MockClock{CurrentTime: time.Now()}
	h := NewScoutTourHandler(&fakeTourShipRepo{ship: scoutAt(t, "X1-TEST-M1")}, med, marketScanner, nil, clock)

	var sendsAtCheck []int
	reader := &scriptedDormancy{
		script:  func(call int) (bool, error) { return call <= 2, nil },
		onCheck: func() { sendsAtCheck = append(sendsAtCheck, med.sends) },
	}
	h.SetDormancyReader(reader)

	before := clock.CurrentTime
	_, err := h.Handle(tourCtx(), &ScoutTourCommand{
		PlayerID:           shared.MustNewPlayerID(1),
		ShipSymbol:         "PROBE-1",
		Markets:            []string{"X1-TEST-M1", "X1-TEST-M2", "X1-TEST-M3"},
		Iterations:         1,
		ScanInterval:       5 * time.Minute,
		StartJitterMaxSecs: 1,
	})
	require.NoError(t, err)

	require.NotZero(t, reader.calls, "the circuit must consult dormancy before its first leg")
	for i, sends := range sendsAtCheck {
		require.Zerof(t, sends, "check %d: a parked probe flew a leg", i+1)
	}
	require.Equal(t, 3, med.sends, "the full circuit flies once the bit clears")
	require.GreaterOrEqual(t, clock.CurrentTime.Sub(before), 10*time.Minute,
		"two dormant checks sleep two full scan intervals in place")
}

// A standing post that goes dormant MID-LIFE parks between scan cycles: the
// tour has already scanned once, the rotation sheds it, and for the whole hold
// the market-read count is FROZEN — then the remaining cycles complete when
// the bit clears. This is the production shape (rotation flips posts on a
// running tour), and it is carried entirely by the hold inside the scan-cycle
// loop: deleting that call site fails this test.
func TestScoutTour_DormantMidLife_FreezesScanCycleThenResumes(t *testing.T) {
	api := &countingScoutAPI{}
	marketScanner := ship.NewMarketScanner(api, &fakeMarketStore{}, nil, nil)
	clock := &shared.MockClock{CurrentTime: time.Now()}
	h := NewScoutTourHandler(&fakeTourShipRepo{ship: scoutAt(t, scoutedYard)}, nil, marketScanner, nil, clock)

	var getsWhileDormant []int
	reader := &scriptedDormancy{
		script: func(call int) (bool, error) {
			dormant := call >= 2 && call <= 4
			if dormant {
				getsWhileDormant = append(getsWhileDormant, api.marketGets)
			}
			return dormant, nil
		},
	}
	h.SetDormancyReader(reader)

	before := clock.CurrentTime
	_, err := h.Handle(tourCtx(), &ScoutTourCommand{
		PlayerID:           shared.MustNewPlayerID(1),
		ShipSymbol:         "PROBE-1",
		Markets:            []string{scoutedYard},
		Iterations:         3,
		ScanInterval:       5 * time.Minute,
		StartJitterMaxSecs: 1,
	})
	require.NoError(t, err)

	require.Len(t, getsWhileDormant, 3,
		"the scan-cycle loop must consult dormancy every cycle — going dormant mid-life parks via that call site")
	for i, gets := range getsWhileDormant {
		require.Equalf(t, 1, gets, "hold check %d: the read count must be frozen at the pre-hold scan", i+1)
	}
	require.Equal(t, 3, api.marketGets, "the remaining cycles complete once the bit clears")
	require.GreaterOrEqual(t, clock.CurrentTime.Sub(before), 25*time.Minute,
		"three parked intervals plus two live scan waits")
}

// A dormancy READ ERROR must fail toward scanning: the stub returns a wrong
// dormant=true alongside the error, and the tour must scan anyway, immediately
// — the error wins over the value, because a blind sensing signal must never
// silently park the fleet's sensor.
func TestScoutTour_DormancyReadError_ScansAnyway(t *testing.T) {
	api := &countingScoutAPI{}
	marketScanner := ship.NewMarketScanner(api, &fakeMarketStore{}, nil, nil)
	clock := &shared.MockClock{CurrentTime: time.Now()}
	h := NewScoutTourHandler(&fakeTourShipRepo{ship: scoutAt(t, scoutedYard)}, nil, marketScanner, nil, clock)

	reader := &scriptedDormancy{
		script: func(call int) (bool, error) {
			if call <= 5 {
				return true, errors.New("db unavailable")
			}
			return false, nil
		},
	}
	h.SetDormancyReader(reader)

	_, err := h.Handle(tourCtx(), &ScoutTourCommand{
		PlayerID:           shared.MustNewPlayerID(1),
		ShipSymbol:         "PROBE-1",
		Markets:            []string{scoutedYard},
		Iterations:         1,
		ScanInterval:       5 * time.Minute,
		StartJitterMaxSecs: 1,
	})
	require.NoError(t, err)

	require.Equal(t, 1, api.marketGets, "a read error must not suppress the scan")
	require.Equal(t, 1, reader.calls,
		"the error must short-circuit the hold on the first check — never park on a value the error invalidated")
}

// An unwired tour (no dormancy reader) is byte-identical: it scans without
// ever consulting anything.
func TestScoutTour_NoDormancyReader_ScansUnchanged(t *testing.T) {
	api := &countingScoutAPI{}
	marketScanner := ship.NewMarketScanner(api, &fakeMarketStore{}, nil, nil)
	clock := &shared.MockClock{CurrentTime: time.Now()}
	h := NewScoutTourHandler(&fakeTourShipRepo{ship: scoutAt(t, scoutedYard)}, nil, marketScanner, nil, clock)

	_, err := h.Handle(tourCtx(), &ScoutTourCommand{
		PlayerID:           shared.MustNewPlayerID(1),
		ShipSymbol:         "PROBE-1",
		Markets:            []string{scoutedYard},
		Iterations:         1,
		StartJitterMaxSecs: 1,
	})
	require.NoError(t, err)
	require.Equal(t, 1, api.marketGets)
}
