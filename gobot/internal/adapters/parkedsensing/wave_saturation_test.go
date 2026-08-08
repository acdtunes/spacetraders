package parkedsensing

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// THE WITHHOLDER'S HALF OF THE SWITCH-BACK, and the reason it matters as much as the spender's: the
// drain is what probe buying is PAUSED BY. A coordinator that stopped buying heavies while this port
// still read HEAVY would leave the fleet saving toward a hull nobody intends to buy and coverage
// growth stalled behind it — the split-brain the shared predicate exists to prevent.

// A SATURATED SURFACE RELEASES PROBE BUYING, with lanes still unserved. The count clause would hold
// this world HEAVY; the depth verdict overrides it, and probe buying resumes.
func TestWavePort_SaturatedSurfaceIsProbeWithLanesStillUnserved(t *testing.T) {
	sw, res, lanes, peak := reachableWorld()
	lanes.saturated = true

	wave, reason, _, err := waveOf(t, sw, res, lanes, peak)

	require.NoError(t, err)
	require.Equal(t, common.WaveProbe, wave)
	require.Equal(t, common.WaveProbeReasonTradeSaturated, reason)
	require.Equal(t, 7, lanes.count, "the fixture must keep lanes UNSERVED, or saturation is not what forced this")
}

// THE ANTI-VACUITY CONTROL. reachableWorld() is unsaturated and must stay HEAVY — asserted here
// beside its complement so the case above cannot pass on a port that answers PROBE unconditionally.
func TestWavePort_AnUnsaturatedSurfaceIsHeavyUnchanged(t *testing.T) {
	sw, res, lanes, peak := reachableWorld()

	wave, reason, _, err := waveOf(t, sw, res, lanes, peak)

	require.NoError(t, err)
	require.Equal(t, common.WaveHeavy, wave)
	require.Equal(t, common.WaveProbeReasonNone, reason)
}

// ONE READ SERVES BOTH DIMENSIONS. The saturation window lives inside the shared reader and is
// advanced by the read itself, so a port that asked for the count and the depth separately would
// advance that window twice per tick — and hand the two consumers two different histories of one
// surface. The call count is the only place that property is observable.
func TestWavePort_TheCountAndTheDepthComeOffOneRead(t *testing.T) {
	sw, res, lanes, peak := reachableWorld()
	lanes.saturated = true

	_, _, _, err := waveOf(t, sw, res, lanes, peak)

	require.NoError(t, err)
	require.Equal(t, 1, lanes.calls, "the lane surface must be read exactly once per tick")
}
