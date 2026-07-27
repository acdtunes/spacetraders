package shared

import "testing"

// TestFlightModeTravelTimeRanksModesBySpeed pins the speed ordering every ETA,
// tour cost and mode ranking is built on: BURN beats CRUISE beats DRIFT, over the
// same distance at the same engine speed. DRIFT trading fuel for time is the whole
// point of the mode, so a model that makes it look cheap inverts every decision
// that weighs modes against each other.
//
// The distance/speed pair is the production leg that exposed the inversion: a
// DRIFT crossing measured ~885s where CRUISE models ~128s.
func TestFlightModeTravelTimeRanksModesBySpeed(t *testing.T) {
	const distance = 123.81
	const engineSpeed = 30

	burn := FlightModeBurn.TravelTime(distance, engineSpeed)
	cruise := FlightModeCruise.TravelTime(distance, engineSpeed)
	drift := FlightModeDrift.TravelTime(distance, engineSpeed)

	if burn >= cruise {
		t.Fatalf("BURN must be faster than CRUISE: burn=%ds cruise=%ds", burn, cruise)
	}
	if cruise >= drift {
		t.Fatalf("CRUISE must be faster than DRIFT: cruise=%ds drift=%ds", cruise, drift)
	}
	if drift < 5*cruise {
		t.Fatalf("DRIFT must model as several times slower than CRUISE (production measured ~7x): cruise=%ds drift=%ds", cruise, drift)
	}

	// IsFasterThan is what every mode ranking asks, so it must agree with the clock.
	if !FlightModeCruise.IsFasterThan(FlightModeDrift) {
		t.Fatal("CRUISE.IsFasterThan(DRIFT) must be true")
	}
	if FlightModeDrift.IsFasterThan(FlightModeCruise) {
		t.Fatal("DRIFT.IsFasterThan(CRUISE) must be false")
	}
	if !FlightModeBurn.IsFasterThan(FlightModeCruise) {
		t.Fatal("BURN.IsFasterThan(CRUISE) must be true")
	}
}
