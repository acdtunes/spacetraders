package cli

import (
	"fmt"
	"sort"
	"strings"
)

// defaultStartingFaction is the faction every era has actually registered under
// (eras 1-6, torwind-2026-06-28 through torwind-2026-08-02). It is what the mint
// paths send when the operator names none — the API has no default of its own and
// rejects an absent one.
const defaultStartingFaction = "COSMIC"

// registrableFactions is the /register faction enum, transcribed from the API's own
// rejection of the empty string (422, code 3001, zodIssues path [faction],
// invalid_enum_value, options [...]).
//
// Holding the list client-side is what lets both mint paths refuse a typo BEFORE the
// Register call, which is irreversible and consumes an account slot. The cost is that
// a faction the API adds later reads as invalid here until this list is updated — the
// error says exactly that, so the operator is never left guessing why a real faction
// was refused.
var registrableFactions = map[string]bool{
	"COSMIC": true, "VOID": true, "GALACTIC": true, "QUANTUM": true, "DOMINION": true,
	"ASTRO": true, "CORSAIRS": true, "OBSIDIAN": true, "AEGIS": true, "UNITED": true,
	"SOLITARY": true, "COBALT": true, "OMEGA": true, "ECHO": true, "LORDS": true,
	"CULT": true, "ANCIENTS": true, "SHADOW": true, "ETHEREAL": true,
}

// normalizeFaction upper-cases and trims a caller-supplied faction, then refuses
// anything outside the /register enum.
//
// The empty string is invalid HERE by design: it is precisely the value the API 422s
// on, and it is what `universe transition` hardcoded and `player register --new`
// defaulted to (sp-dqbzm). Callers that legitimately have no faction to offer decide
// that for themselves before calling — resolveMintFaction substitutes the default,
// the from-token path skips the call entirely.
func normalizeFaction(requested string) (string, error) {
	normalized := strings.ToUpper(strings.TrimSpace(requested))
	if registrableFactions[normalized] {
		return normalized, nil
	}
	return "", fmt.Errorf("--faction %q is not a faction the API accepts; valid factions are %s",
		requested, strings.Join(knownFactions(), " "))
}

// resolveMintFaction resolves the faction a Register call will be made with. An
// unnamed faction means "the one every era has used"; anything named must be real.
// The result is never empty, so no mint can reach the API with the value it rejects.
func resolveMintFaction(requested string) (string, error) {
	if strings.TrimSpace(requested) == "" {
		return defaultStartingFaction, nil
	}
	return normalizeFaction(requested)
}

// knownFactions lists the enum in a stable order for error messages.
func knownFactions() []string {
	names := make([]string, 0, len(registrableFactions))
	for name := range registrableFactions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
