package commands

import (
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
)

// The cycle line is the ONLY standing sensor under the wake model, so what it
// omits is invisible. It read "(6 attempts)" for 37 consecutive ticks on the
// live fleet while every one of those attempts was refused — these tests pin
// that the reason now reaches it, and that reading it stays bounded.

func TestRefusalSuffix_NamesTheStepYardHullAndReason(t *testing.T) {
	got := refusalSuffix([]parkedsensing.BuyRefusal{{
		Step:   parkedsensing.BuyStepBuy,
		Yard:   "X1-KP23-C38",
		Buyer:  "TORWIND-11",
		Reason: "sensing probe buyer TORWIND-11 claim failed",
		Count:  3,
	}})

	for _, want := range []string{"buy", "X1-KP23-C38", "TORWIND-11", "claim failed", "×3"} {
		if !strings.Contains(got, want) {
			t.Fatalf("refusal summary %q omits %q — an operator cannot act on what it does not say", got, want)
		}
	}
}

// A quote refusal engaged no hull, so it must NOT invent one. Reporting a buyer
// here would tell an operator to go and inspect a hull that was never involved.
func TestRefusalSuffix_QuoteRefusalNamesNoHull(t *testing.T) {
	got := refusalSuffix([]parkedsensing.BuyRefusal{{
		Step:   parkedsensing.BuyStepQuote,
		Yard:   "X1-KP23-A2",
		Reason: "shipyard at X1-KP23-A2 has no priced SHIP_PROBE listing",
		Count:  1,
	}})

	if !strings.Contains(got, "quote") || !strings.Contains(got, "X1-KP23-A2") {
		t.Fatalf("quote refusal summary %q does not name the step and the yard", got)
	}
	if strings.Contains(got, " via ") {
		t.Fatalf("quote refusal summary %q names a purchasing hull, but none was engaged", got)
	}
	// Count 1 is the common case and adding "×1" to every line is noise.
	if strings.Contains(got, "×1") {
		t.Fatalf("summary %q spells out a count of one", got)
	}
}

// Emitted every ~30s forever, so the line must not grow without limit.
func TestRefusalSuffix_IsBounded(t *testing.T) {
	many := make([]parkedsensing.BuyRefusal, 0, 6)
	for i := 0; i < 6; i++ {
		many = append(many, parkedsensing.BuyRefusal{
			Step: parkedsensing.BuyStepBuy, Yard: "X1-AA-Y", Buyer: "B", Reason: "refused", Count: 1,
		})
	}
	got := refusalSuffix(many)

	if strings.Count(got, "; ") != maxLoggedRefusals-1 {
		t.Fatalf("summary %q does not cap at %d refusals", got, maxLoggedRefusals)
	}
	if !strings.Contains(got, "+3 more") {
		t.Fatalf("summary %q drops refusals without saying how many", got)
	}
	// The payload is the queryable record and must keep every row, or the one
	// refusal an operator is hunting could be the one truncated away.
	if len(refusalPayload(many)) != len(many) {
		t.Fatalf("payload dropped rows: got %d, want %d", len(refusalPayload(many)), len(many))
	}
}

func TestRefusalSuffix_SilentWhenNothingRefused(t *testing.T) {
	if got := refusalSuffix(nil); got != "" {
		t.Fatalf("a clean tick appended %q to the cycle line, want nothing", got)
	}
}
