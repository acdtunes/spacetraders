package placement

import (
	"math/rand"
	"strings"
	"testing"
)

// feasible builds a scored, feasible evaluation for a given β so the test's Score matches Decide's.
func feasible(system string, ex float64, hops int, deadheadHours, beta float64) Evaluation {
	return Evaluation{
		System: system, Waypoint: system + "-WP", EX: ex, Hops: hops,
		DeadheadHours: deadheadHours, Score: Score(ex, beta, deadheadHours), Feasible: true,
	}
}

// RED#3 (sp-z7ng): score(x) = E_x − β·D_x is a linear deadhead charge. A candidate 0.25h of deadhead
// away at β=800/hr is charged 200; a D=0 candidate (the current system) is charged nothing, so its
// score is exactly E_x (the D_s=0 identity the stay option rides).
func TestScore_LinearDeadheadCharge(t *testing.T) {
	if got := Score(1000, 800, 0.25); got != 800 {
		t.Fatalf("Score(1000,800,0.25) = %v, want 1000 − 800·0.25 = 800", got)
	}
	if got := Score(1000, 800, 0); got != 1000 {
		t.Fatalf("Score with D=0 = %v, want E_x=1000 (the D_s=0 identity)", got)
	}
}

// RED#4 (sp-z7ng): STAY wins net of the move. A current-system E_s=900 (D=0) beats a richer foreign
// E_x=1000 that is 0.5h of deadhead away at β=800 (charged score 1000−400=600), so Decide holds the
// hull on its ground — Winner is the current system and Stay is set. A jump that earns less per hour
// once the crossing is paid is not worth the antimatter.
func TestDecide_StayWinsNetOfMove(t *testing.T) {
	const beta = 800.0
	evals := []Evaluation{
		feasible("X1-HOME", 900, 0, 0, beta),   // stay: score 900
		feasible("X1-FAR", 1000, 1, 0.5, beta), // jump: score 1000 − 800·0.5 = 600
	}
	d := Decide(evals, beta, 0.3)
	if !d.Stay {
		t.Fatalf("the current system must WIN (Stay=true) when its E_s beats every charged jump, got %+v", d)
	}
	if d.Winner == nil || d.Winner.System != "X1-HOME" {
		t.Fatalf("winner must be the current system X1-HOME, got %+v", d.Winner)
	}
	if d.Hold {
		t.Fatalf("a stay that clears the floor is not a Hold")
	}
}

// RED#5 (sp-z7ng): the park floor HOLDS a hull when no ground — including staying — clears φ·β. With
// β=1000 and φ=0.3 the floor is 300; every candidate score sits below it, so Decide parks (Hold=true,
// no Winner) and the HoldReason names the floor so an operator reading the log sees WHY nothing fired.
func TestDecide_ParkFloorHoldsBelowPhi(t *testing.T) {
	const beta = 1000.0
	evals := []Evaluation{
		feasible("X1-HOME", 250, 0, 0, beta), // score 250 < 300
		feasible("X1-A", 400, 1, 0.4, beta),  // score 400 − 1000·0.4 = 0
	}
	d := Decide(evals, beta, 0.3)
	if !d.Hold {
		t.Fatalf("all scores below φ·β=300 must Hold, got %+v", d)
	}
	if d.Winner != nil {
		t.Fatalf("a Hold has no winner, got %+v", d.Winner)
	}
	if !strings.Contains(d.HoldReason, "park floor") {
		t.Fatalf("the HoldReason must name the park floor, got %q", d.HoldReason)
	}
}

// RED#6 (sp-z7ng): the epic's unit/brute-force parity lane. Over 500 randomized evaluation sets,
// Decide's winner and its stay/hold flags must match an independent straight-line reimplementation of
// argmax-over-feasible + park floor — the algebra of the DIFF phase has no hidden state.
func TestDecide_BruteForceArgmaxParity(t *testing.T) {
	rng := rand.New(rand.NewSource(0x2c0ffee))
	for trial := 0; trial < 500; trial++ {
		beta := 1 + rng.Float64()*1000
		phi := rng.Float64() // [0,1)
		n := 1 + rng.Intn(6)
		evals := make([]Evaluation, 0, n)
		for i := 0; i < n; i++ {
			hops := rng.Intn(5)        // 0..4 (0 = the stay option)
			d := float64(hops) * 0.1   // deadhead hours
			ex := rng.Float64() * 2000 // 0..2000 $/hr
			e := Evaluation{System: systemName(i), Hops: hops, DeadheadHours: d, EX: ex, Feasible: rng.Intn(2) == 0}
			if e.Feasible {
				e.Score = Score(ex, beta, d)
			}
			evals = append(evals, e)
		}

		wantWinner, wantStay, wantHold := refDecide(evals, beta, phi)
		got := Decide(evals, beta, phi)

		gotWinner := ""
		if got.Winner != nil {
			gotWinner = got.Winner.System
		}
		if gotWinner != wantWinner || got.Stay != wantStay || got.Hold != wantHold {
			t.Fatalf("trial %d parity mismatch (β=%.1f φ=%.2f):\n got winner=%q stay=%v hold=%v\nwant winner=%q stay=%v hold=%v\nevals=%+v",
				trial, beta, phi, gotWinner, got.Stay, got.Hold, wantWinner, wantStay, wantHold, evals)
		}
	}
}

// refDecide is the independent reference: max-score feasible, park floor, stay iff the winner is a
// D=0 (current-system) eval. Deliberately NOT sharing code with Decide.
func refDecide(evals []Evaluation, beta, phi float64) (winner string, stay, hold bool) {
	floor := phi * beta
	best := -1
	for i := range evals {
		if !evals[i].Feasible {
			continue
		}
		if best == -1 || evals[i].Score > evals[best].Score {
			best = i
		}
	}
	if best == -1 {
		return "", false, true // no feasible → hold
	}
	if evals[best].Score < floor {
		return "", false, true // below park floor → hold
	}
	return evals[best].System, evals[best].Hops == 0, false
}

func systemName(i int) string { return "X1-" + string(rune('A'+i)) }

// RED#7 (sp-z7ng): infeasible candidates NEVER win (even with an enormous score), and an unreadable
// β (≤0) is reported as BetaReadable=false with no verdict — Decide refuses to invent a β, so the
// caller can fall back to the legacy engine instead of jumping off a fabricated rate.
func TestDecide_InfeasibleAndUnreadableInputs(t *testing.T) {
	const beta = 500.0
	evals := []Evaluation{
		{System: "X1-INF", EX: 99999, Hops: 1, DeadheadHours: 0.1, Score: 99999, Feasible: false},
		feasible("X1-OK", 600, 1, 0.1, beta), // score 600 − 500·0.1 = 550
	}
	d := Decide(evals, beta, 0.3)
	if d.Winner == nil || d.Winner.System != "X1-OK" {
		t.Fatalf("an infeasible candidate must never win despite its score — winner should be X1-OK, got %+v", d.Winner)
	}

	unreadable := Decide(evals, 0, 0.3)
	if unreadable.BetaReadable {
		t.Fatalf("β=0 must be reported unreadable (Decide never invents a β)")
	}
	if unreadable.Winner != nil || unreadable.Stay || unreadable.Hold {
		t.Fatalf("an unreadable β yields NO verdict (caller falls back to legacy), got %+v", unreadable)
	}
}
