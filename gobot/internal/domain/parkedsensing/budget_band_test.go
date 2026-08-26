package parkedsensing

// budget_band_test.go pins the emergency throttle's behaviour in the band between
// the two wait marks — the region where nothing is alarming and nothing is quiet.
//
// The throttle is an INTEGRATOR: its value is the product of every step it has
// taken, never a function of the current wait alone. A band that left it unchanged
// therefore does not mean "hold steady", it means "hold whatever history chose" —
// two fleets under identical live pressure can sit at fully released and at the
// floor, and neither has any path back. These tests pin the escape, and pin that
// the escape does not cost anything under genuine saturation.

import (
	"testing"
	"time"
)

const (
	bandWaitLow  = 200 * time.Millisecond
	bandWaitHigh = 800 * time.Millisecond
)

// bandWait is a smoothed wait strictly inside the band: elevated, tolerable,
// neither mark crossed.
const bandWait = 500 * time.Millisecond

// TestApplyBrake_EscapesTheWaitBand is the headline property. A throttle driven to
// its floor by a burst, then left under a wait that settles INSIDE the band, must
// climb back out on its own — the operator must not have to move a threshold to
// unstick it.
func TestApplyBrake_EscapesTheWaitBand(t *testing.T) {
	brake := brakeFloor

	// One tick inside the band moves it. This is the assertion the dead band fails:
	// with a frozen band the value below is still exactly the floor.
	stepped := ApplyBrake(brake, bandWait, bandWaitLow, bandWaitHigh)
	if stepped <= brakeFloor {
		t.Fatalf("brake after one in-band tick = %v, want above the %v floor — a wait that is neither "+
			"alarming nor quiet must not pin the throttle where a past burst left it", stepped, brakeFloor)
	}

	// And it keeps climbing to fully released rather than converging somewhere
	// arbitrary. 100 ticks is ~50 minutes at the default cadence, which is the
	// timescale a stuck throttle actually costs.
	for range 100 {
		brake = ApplyBrake(brake, bandWait, bandWaitLow, bandWaitHigh)
	}
	assertNear(t, "brake after sustained in-band pressure", brake, brakeReleased)
}

// The in-band drift is a RELEASE, never a bite: an unbraked fleet sitting in the
// band stays unbraked rather than being throttled by the mere absence of quiet.
func TestApplyBrake_InBandDriftNeverBrakes(t *testing.T) {
	brake := brakeReleased
	for range 20 {
		brake = ApplyBrake(brake, bandWait, bandWaitLow, bandWaitHigh)
	}
	assertNear(t, "released brake under in-band pressure", brake, brakeReleased)
}

// TestApplyBrake_SaturationOutrunsTheBandDrift is the protection guard. The band
// drift must never let a fleet climb out from under a genuinely degraded API: one
// tick above the high mark has to undo many in-band ticks, so an API that keeps
// crossing the mark still drives the throttle to its floor.
func TestApplyBrake_SaturationOutrunsTheBandDrift(t *testing.T) {
	// Twelve in-band ticks per one saturated tick. The throttle must still end at
	// the floor: braking is multiplicative-halving and the drift is a few percent,
	// so the ratio has to be firmly in the brake's favour.
	brake := brakeReleased
	for range 40 {
		for range 12 {
			brake = ApplyBrake(brake, bandWait, bandWaitLow, bandWaitHigh)
		}
		brake = ApplyBrake(brake, bandWaitHigh+time.Millisecond, bandWaitLow, bandWaitHigh)
	}
	assertNear(t, "brake under repeated saturation", brake, brakeFloor)
}

// Recovery below the low mark stays STRICTLY faster than the in-band drift. The
// two marks must not collapse into one behaviour: crossing the low mark is what
// says the API is quiet, and a quiet API has to buy back the scan rate sooner than
// a merely tolerable one.
func TestApplyBrake_QuietRecoversFasterThanTheBandDrift(t *testing.T) {
	const prev = 0.5
	quiet := ApplyBrake(prev, bandWaitLow-time.Millisecond, bandWaitLow, bandWaitHigh)
	inBand := ApplyBrake(prev, bandWait, bandWaitLow, bandWaitHigh)

	if !(quiet > inBand) {
		t.Fatalf("quiet recovery %v must outpace the in-band drift %v — otherwise the low mark "+
			"stops meaning anything and the throttle reopens at one speed everywhere", quiet, inBand)
	}
	if !(inBand > prev) {
		t.Fatalf("in-band drift %v did not move the throttle off %v", inBand, prev)
	}
}
