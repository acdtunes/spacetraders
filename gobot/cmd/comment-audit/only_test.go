package main

import (
	"strings"
	"testing"
)

// The gate reports on the packages it was given, so a value the shell has already taken apart
// makes it report on a subset while still saying OK. That is the one failure a checking tool
// must not have: it is indistinguishable from a pass, and every caller relays it in good faith.

// TestResolveOnly_LeftoverArgumentsAreRefused is the bug. A list written the natural way and
// interpolated unquoted arrives as separate arguments: the flag takes the first, and the rest
// land as positional arguments nothing reads. Scope silently narrows to one package, and every
// flag after the list is dropped along with it, so even -quiet stops applying.
//
// The leftovers ARE the evidence. Refusing on them is what makes the mistake impossible to make
// quietly, whichever caller made it.
func TestResolveOnly_LeftoverArgumentsAreRefused(t *testing.T) {
	// The dropped package is deliberately one whose NAME contains no part of the guidance the
	// assertion below looks for. An earlier version used a package ending in "commands", and
	// because the message quotes the offending argument back, "commands" satisfied a search for
	// "comma" — the assertion passed on the fixture's own name rather than on anything the
	// message said, and stayed green when the guidance was removed entirely.
	_, err := resolveOnly("internal/adapters/parkedsensing", []string{"internal/domain/hullbuy", "-quiet"})
	if err == nil {
		t.Fatal("a list the shell split into separate arguments must be refused: the tool would otherwise check the first package, ignore the rest, and report OK")
	}
	// The message has to hand back a form that works, because the caller who hits this wrote the
	// one that reads naturally and has no reason to suspect a separator. Asserting the worked
	// example rather than a word keeps this from passing on incidental text.
	if !strings.Contains(err.Error(), `-only "pkg1,pkg2"`) {
		t.Fatalf("the refusal must show the invocation that works, got: %v", err)
	}
	// And it must quote the argument that was dropped, or the reader cannot tell which part of
	// their command was ignored.
	if !strings.Contains(err.Error(), "internal/domain/hullbuy") {
		t.Fatalf("the refusal must name the argument it refused, got: %v", err)
	}
}

// A value that survives the shell intact is honoured whichever separator was used, so the form
// that reads naturally is correct rather than merely diagnosed.
func TestResolveOnly_AcceptsEitherSeparator(t *testing.T) {
	want := []string{"internal/adapters/parkedsensing", "internal/application/fleet/commands"}
	for _, value := range []string{
		"internal/adapters/parkedsensing,internal/application/fleet/commands",
		"internal/adapters/parkedsensing internal/application/fleet/commands",
		"internal/adapters/parkedsensing, internal/application/fleet/commands",
		"  internal/adapters/parkedsensing   internal/application/fleet/commands  ",
	} {
		got, err := resolveOnly(value, nil)
		if err != nil {
			t.Fatalf("resolveOnly(%q): %v", value, err)
		}
		if len(got) != len(want) {
			t.Fatalf("resolveOnly(%q) returned %d package(s) %v, want %d — a separator the tool does not split on silently collapses the list into one entry that matches nothing", value, len(got), got, len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("resolveOnly(%q)[%d] = %q, want %q", value, i, got[i], want[i])
			}
		}
	}
}

// An empty value means "check everything", which must stay distinct from "check nothing" — an
// empty slice would match no package and pass every time.
func TestResolveOnly_EmptyValueChecksEverything(t *testing.T) {
	got, err := resolveOnly("", nil)
	if err != nil {
		t.Fatalf("an absent -only is not an error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("an absent -only must select every package (nil filter), got %v", got)
	}
}

// Separators and nothing else must not read as a filter. A list of empty strings would be a
// prefix filter that matches every package, which is the full run wearing a scope's clothes.
func TestResolveOnly_SeparatorsOnlyIsNotAFilter(t *testing.T) {
	got, err := resolveOnly(" , ,, ", nil)
	if err != nil {
		t.Fatalf("resolveOnly: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a value of separators alone selects nothing meaningful and must not become a filter, got %v", got)
	}
}
