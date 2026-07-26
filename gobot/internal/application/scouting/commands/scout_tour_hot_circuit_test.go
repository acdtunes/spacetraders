package commands

// Stage-2 market selection at the tour: a STANDING circuit restricts itself to
// its post's hot set — the markets that DEAL IN whitelisted goods — read fresh
// each circuit through the same post-read seam as dormancy. Every failure
// direction must fly the FULL circuit: an empty set (stage 1), a read error
// (even one returned ALONGSIDE a wrong non-empty list), a sweep-once/finite
// tour (the sweep IS the first scan), and an intersection too small to keep
// the hot markets fresh.

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

// recordingTourMediator records every leg's destination — the circuit's whole
// observable shape — and FAILS the send after limit legs, which is the only
// deterministic way to end an infinite standing tour under test (a context
// cancel races the mock clock's instant sleeps).
type recordingTourMediator struct {
	destinations []string
	limit        int
}

func (m *recordingTourMediator) Send(_ context.Context, req mediator.Request) (mediator.Response, error) {
	if m.limit > 0 && len(m.destinations) >= m.limit {
		return nil, errors.New("tour ended by test")
	}
	nav, ok := req.(*shipNav.NavigateRouteCommand)
	if !ok {
		return nil, errors.New("unexpected request type")
	}
	m.destinations = append(m.destinations, nav.Destination)
	return &shipNav.NavigateRouteResponse{Status: "completed"}, nil
}

func (m *recordingTourMediator) Register(reflect.Type, mediator.RequestHandler) error { return nil }

func (m *recordingTourMediator) RegisterMiddleware(mediator.Middleware) {}

func neverDormant() func(int) (bool, error) {
	return func(int) (bool, error) { return false, nil }
}

func hotTourFixture(t *testing.T, med *recordingTourMediator, reader *scriptedDormancy) *ScoutTourHandler {
	t.Helper()
	marketScanner := ship.NewMarketScanner(&countingScoutAPI{}, &fakeMarketStore{}, nil, nil)
	h := NewScoutTourHandler(
		&fakeTourShipRepo{ship: scoutAt(t, "X1-TEST-M1")}, med, marketScanner, nil,
		&shared.MockClock{CurrentTime: time.Now()},
	)
	h.SetDormancyReader(reader)
	return h
}

func hotTourCommand(iterations int) *ScoutTourCommand {
	return &ScoutTourCommand{
		PlayerID:           shared.MustNewPlayerID(1),
		ShipSymbol:         "PROBE-1",
		Markets:            []string{"X1-TEST-M1", "X1-TEST-M2", "X1-TEST-M3"},
		Iterations:         iterations,
		ScanInterval:       5 * time.Minute,
		StartJitterMaxSecs: 1,
	}
}

// hotTourCtx bounds every hot-circuit test in WALL time: a regression that
// empties the circuit would otherwise spin the standing loop forever on the
// instant mock clock and hang the suite instead of failing it.
func hotTourCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(tourCtx(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// A standing (infinite) tour whose post carries hot set {M1,M3} of {M1,M2,M3}
// circuits ONLY the hot markets, circuit after circuit — M2 is stage-1
// territory and costs nothing while the post is in stage 2.
func TestScoutTour_HotCircuit_StandingTourVisitsOnlyHotWaypoints(t *testing.T) {
	med := &recordingTourMediator{limit: 4}
	reader := &scriptedDormancy{
		script:    neverDormant(),
		hotScript: func(int) ([]string, error) { return []string{"X1-TEST-M1", "X1-TEST-M3"}, nil },
	}
	h := hotTourFixture(t, med, reader)

	_, err := h.Handle(hotTourCtx(t), hotTourCommand(-1))

	require.ErrorContains(t, err, "tour ended by test")
	require.Equal(t, []string{"X1-TEST-M1", "X1-TEST-M3", "X1-TEST-M1", "X1-TEST-M3"}, med.destinations,
		"two full circuits over the hot set only — the cold market is never flown")
	require.GreaterOrEqual(t, reader.hotCalls, 2, "the hot set is re-read every circuit, so a stage flip lands without a respawn")
}

// An EMPTY hot set is stage 1 / cold start: the standing tour flies its FULL
// circuit. This is the fail-toward-sensing default — restriction is earned by
// a non-empty stamp, never assumed.
func TestScoutTour_HotCircuit_EmptySetFliesFullCircuit(t *testing.T) {
	med := &recordingTourMediator{limit: 3}
	reader := &scriptedDormancy{
		script:    neverDormant(),
		hotScript: func(int) ([]string, error) { return []string{}, nil },
	}
	h := hotTourFixture(t, med, reader)

	_, err := h.Handle(hotTourCtx(t), hotTourCommand(-1))

	require.ErrorContains(t, err, "tour ended by test")
	require.Equal(t, []string{"X1-TEST-M1", "X1-TEST-M2", "X1-TEST-M3"}, med.destinations,
		"empty hot set ⇒ the full stage-1 circuit, all three markets")
}

// A hot-set READ ERROR must fly the full circuit — even when a wrong non-empty
// list rides alongside the error. The error wins over the value: a blind
// signal may widen scanning, never narrow it.
func TestScoutTour_HotCircuit_ReadErrorFliesFullCircuit(t *testing.T) {
	med := &recordingTourMediator{limit: 3}
	reader := &scriptedDormancy{
		script: neverDormant(),
		// The wrong list is deliberately one that WOULD restrict (two legs) if
		// consumed — a singleton would be masked by the too-small-intersection
		// guard and could never distinguish "error won" from "blackout guard won".
		hotScript: func(int) ([]string, error) {
			return []string{"X1-TEST-M1", "X1-TEST-M3"}, errors.New("post read down")
		},
	}
	h := hotTourFixture(t, med, reader)

	_, err := h.Handle(hotTourCtx(t), hotTourCommand(-1))

	require.ErrorContains(t, err, "tour ended by test")
	require.Equal(t, []string{"X1-TEST-M1", "X1-TEST-M2", "X1-TEST-M3"}, med.destinations,
		"the error must invalidate the list beside it — full circuit, all three markets")
}

// A sweep-once tour (the finite, one-pass shape the reconciler spawns for
// sweep posts) IGNORES the hot set entirely: that single pass IS the system's
// first scan and must see every market. The stub adversarially serves a
// non-empty hot set; the tour must not even consult it.
func TestScoutTour_HotCircuit_SweepOnceTourIgnoresTheField(t *testing.T) {
	med := &recordingTourMediator{}
	reader := &scriptedDormancy{
		script: neverDormant(),
		// Two legs, so a dropped exemption shows up in the DESTINATIONS too,
		// not only in the consult count.
		hotScript: func(int) ([]string, error) { return []string{"X1-TEST-M1", "X1-TEST-M3"}, nil },
	}
	h := hotTourFixture(t, med, reader)

	_, err := h.Handle(hotTourCtx(t), hotTourCommand(1))

	require.NoError(t, err)
	require.Equal(t, []string{"X1-TEST-M1", "X1-TEST-M2", "X1-TEST-M3"}, med.destinations,
		"a sweep's one pass is the first scan — it must fly every market")
	require.Zero(t, reader.hotCalls, "a finite tour never consults the restriction")
}

// A hot set that would leave the circuit under two legs is a blackout, not a
// restriction: a disjoint set has nothing to visit, and a single repeated
// waypoint degenerates to a no-op navigate that never rescans — so both fly
// the FULL circuit.
func TestScoutTour_HotCircuit_TooSmallIntersectionFliesFullCircuit(t *testing.T) {
	cases := map[string][]string{
		"disjoint set":           {"X1-TEST-M9"},
		"singleton intersection": {"X1-TEST-M2"},
	}
	for name, hot := range cases {
		t.Run(name, func(t *testing.T) {
			med := &recordingTourMediator{limit: 3}
			reader := &scriptedDormancy{
				script:    neverDormant(),
				hotScript: func(int) ([]string, error) { return hot, nil },
			}
			h := hotTourFixture(t, med, reader)

			_, err := h.Handle(hotTourCtx(t), hotTourCommand(-1))

			require.ErrorContains(t, err, "tour ended by test")
			require.Equal(t, []string{"X1-TEST-M1", "X1-TEST-M2", "X1-TEST-M3"}, med.destinations)
		})
	}
}
