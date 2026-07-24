package expansion

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ---- doubles at the three repo ports ---------------------------------------

type phaseFakeShips struct {
	ships []*navigation.Ship
	err   error
}

func (f *phaseFakeShips) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return f.ships, f.err
}

type phaseFakeWaypoints struct {
	bySystem map[string][]*shared.Waypoint
	err      error
	queried  []string
}

func (f *phaseFakeWaypoints) ListBySystem(_ context.Context, systemSymbol string) ([]*shared.Waypoint, error) {
	f.queried = append(f.queried, systemSymbol)
	if f.err != nil {
		return nil, f.err
	}
	return f.bySystem[systemSymbol], nil
}

type phaseFakeConstruction struct {
	sites map[string]*manufacturing.ConstructionSite
	err   error
}

func (f *phaseFakeConstruction) FindByWaypoint(_ context.Context, waypointSymbol string, _ int) (*manufacturing.ConstructionSite, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.sites[waypointSymbol], nil
}

// ---- fixtures --------------------------------------------------------------

func phaseShip(t *testing.T, symbol, waypoint, role string) *navigation.Ship {
	t.Helper()
	loc, err := shared.NewWaypoint(waypoint, 0, 0)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(0, 0, nil)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), loc, fuel, 100, 0, cargo, 30, "FRAME_PROBE", role, nil, navigation.NavStatusInOrbit)
	require.NoError(t, err)
	return ship
}

func gateWaypoint(symbol, system string) *shared.Waypoint {
	return &shared.Waypoint{Symbol: symbol, SystemSymbol: system, Type: "JUMP_GATE"}
}

func planetWaypoint(symbol, system string) *shared.Waypoint {
	return &shared.Waypoint{Symbol: symbol, SystemSymbol: system, Type: "PLANET"}
}

func gateSite(waypoint string, complete bool) *manufacturing.ConstructionSite {
	return manufacturing.NewConstructionSite(waypoint, "JUMP_GATE", nil, complete)
}

func phasePid() shared.PlayerID { return shared.MustNewPlayerID(1) }

// ---- tests -----------------------------------------------------------------

// EXPANSION ⇔ the HOME-system jump gate is built. The home system is the COMMAND hull's system
// (bootstrap's own resolution), and the built gate reads InExpansion=true.
func TestInExpansion_HomeGateBuilt_True(t *testing.T) {
	ships := &phaseFakeShips{ships: []*navigation.Ship{
		phaseShip(t, "PROBE-2", "X1-ELSEWHERE-A1", "SATELLITE"), // listed first — must NOT define home
		phaseShip(t, "CMD-1", "X1-HQ-A1", "COMMAND"),
	}}
	wps := &phaseFakeWaypoints{bySystem: map[string][]*shared.Waypoint{
		"X1-HQ": {planetWaypoint("X1-HQ-P1", "X1-HQ"), gateWaypoint("X1-HQ-GATE", "X1-HQ")},
	}}
	con := &phaseFakeConstruction{sites: map[string]*manufacturing.ConstructionSite{
		"X1-HQ-GATE": gateSite("X1-HQ-GATE", true),
	}}
	r := NewBootstrapExpansionPhaseReader(ships, wps, con)

	in, err := r.InExpansion(context.Background(), phasePid())
	require.NoError(t, err)
	require.True(t, in, "a built home gate is the EXPANSION signal")
	require.Equal(t, []string{"X1-HQ"}, wps.queried, "home is the COMMAND hull's system, not the first-listed hull's")
}

// An under-construction home gate is pre-EXPANSION (DATA/INCOME/GATE at the bootstrap derivation):
// readable, false, no error.
func TestInExpansion_GateUnderConstruction_False(t *testing.T) {
	ships := &phaseFakeShips{ships: []*navigation.Ship{phaseShip(t, "CMD-1", "X1-HQ-A1", "COMMAND")}}
	wps := &phaseFakeWaypoints{bySystem: map[string][]*shared.Waypoint{
		"X1-HQ": {gateWaypoint("X1-HQ-GATE", "X1-HQ")},
	}}
	con := &phaseFakeConstruction{sites: map[string]*manufacturing.ConstructionSite{
		"X1-HQ-GATE": gateSite("X1-HQ-GATE", false),
	}}
	r := NewBootstrapExpansionPhaseReader(ships, wps, con)

	in, err := r.InExpansion(context.Background(), phasePid())
	require.NoError(t, err)
	require.False(t, in, "an unbuilt gate means the world is still pre-EXPANSION")
}

// No jump-gate waypoint known in the home system (uncharted cold start, or a gateless system):
// readable pre-EXPANSION — false, no error. Probe-buying stays off, matching fail-closed intent.
func TestInExpansion_NoGateWaypoint_False(t *testing.T) {
	ships := &phaseFakeShips{ships: []*navigation.Ship{phaseShip(t, "CMD-1", "X1-HQ-A1", "COMMAND")}}
	wps := &phaseFakeWaypoints{bySystem: map[string][]*shared.Waypoint{
		"X1-HQ": {planetWaypoint("X1-HQ-P1", "X1-HQ")},
	}}
	r := NewBootstrapExpansionPhaseReader(ships, wps, &phaseFakeConstruction{})

	in, err := r.InExpansion(context.Background(), phasePid())
	require.NoError(t, err)
	require.False(t, in)
}

// Unreadable inputs surface as ERRORS (the coordinator fails closed on them): fleet read down,
// waypoints down, construction-site read down, or no locatable hull at all.
func TestInExpansion_UnreadableInputs_Error(t *testing.T) {
	cmd := phaseShip(t, "CMD-1", "X1-HQ-A1", "COMMAND")
	gate := map[string][]*shared.Waypoint{"X1-HQ": {gateWaypoint("X1-HQ-GATE", "X1-HQ")}}

	cases := []struct {
		name string
		r    *BootstrapExpansionPhaseReader
	}{
		{"fleet read fails", NewBootstrapExpansionPhaseReader(
			&phaseFakeShips{err: errors.New("db down")}, &phaseFakeWaypoints{bySystem: gate}, &phaseFakeConstruction{})},
		{"no locatable hull (home unresolvable)", NewBootstrapExpansionPhaseReader(
			&phaseFakeShips{}, &phaseFakeWaypoints{bySystem: gate}, &phaseFakeConstruction{})},
		{"waypoint list fails", NewBootstrapExpansionPhaseReader(
			&phaseFakeShips{ships: []*navigation.Ship{cmd}}, &phaseFakeWaypoints{err: errors.New("db down")}, &phaseFakeConstruction{})},
		{"construction-site read fails", NewBootstrapExpansionPhaseReader(
			&phaseFakeShips{ships: []*navigation.Ship{cmd}}, &phaseFakeWaypoints{bySystem: gate}, &phaseFakeConstruction{err: errors.New("api down")})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, err := tc.r.InExpansion(context.Background(), phasePid())
			require.Error(t, err, "an unreadable phase input must surface as an error (fail-closed upstream)")
			require.False(t, in, "never true on an unreadable world")
		})
	}
}

// No COMMAND hull resolved: fall back to any located hull's system (bootstrap's own fallback), so a
// frigate-less fleet still reads its world.
func TestInExpansion_FallsBackToAnyHullsSystem(t *testing.T) {
	ships := &phaseFakeShips{ships: []*navigation.Ship{phaseShip(t, "PROBE-2", "X1-HQ-A1", "SATELLITE")}}
	wps := &phaseFakeWaypoints{bySystem: map[string][]*shared.Waypoint{
		"X1-HQ": {gateWaypoint("X1-HQ-GATE", "X1-HQ")},
	}}
	con := &phaseFakeConstruction{sites: map[string]*manufacturing.ConstructionSite{
		"X1-HQ-GATE": gateSite("X1-HQ-GATE", true),
	}}
	r := NewBootstrapExpansionPhaseReader(ships, wps, con)

	in, err := r.InExpansion(context.Background(), phasePid())
	require.NoError(t, err)
	require.True(t, in)
	require.Equal(t, []string{"X1-HQ"}, wps.queried)
}
