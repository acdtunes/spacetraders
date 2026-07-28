package player

// identity.go owns the durable agent-identity half of players.metadata: the facts that come from
// /my/agent, are fixed for the life of an era, and that engines later READ back out of the row.
//
// sp-0eufi: MetadataKeyHeadquarters had three readers and no reachable writer. The only code that
// ever set it lived in SyncPlayerHandler, which nothing constructed or dispatched — so every
// players row carried {"starting_faction": ...} and nothing else. The parked-sensing HomeSystemPort
// read the missing key, failed, and because the sensing CUTOVER runs before expansion in the tick,
// the whole reconcile aborted: screen, reaper, adoption, drain, placements and expansion all dead,
// every 30 seconds, with an empty sensing_systems table and no probe ever placed.
//
// The merge lives HERE, in the domain, because there is more than one way into the row (era
// rollover seeds it at registration; the daemon re-asserts it at boot) and a second copy of
// "which keys are identity, and when has one actually changed" is exactly how the two drift.

// Metadata keys carrying durable agent identity. Exported because they are a cross-layer contract:
// adapters read them straight off the decoded map, and a typo in a string literal is otherwise
// indistinguishable from a key that was never written — which is the failure this file exists to
// end.
const (
	// MetadataKeyHeadquarters is the agent's home waypoint (e.g. "X1-KP23-A1"). Engines derive the
	// home SYSTEM from it; it is the anchor for sensing, expansion, and every home-relative route.
	MetadataKeyHeadquarters = "headquarters"
	// MetadataKeyStartingFaction is the faction the agent registered under.
	MetadataKeyStartingFaction = "starting_faction"
	// MetadataKeyAccountID is the owning account, for operator display.
	MetadataKeyAccountID = "account_id"
)

// MergeAgentIdentity folds an agent payload's durable identity into a player's metadata map and
// reports whether anything actually CHANGED. It returns the map to write; when metadata is nil a
// fresh one is allocated, so callers may pass a nil map straight from a fresh registration.
//
// Three properties, each of which a caller depends on:
//
//   - MERGE, never replace. Only identity keys are touched; every other key in the map — including
//     starting_faction on the live rows, and anything a future feature adds — is carried through
//     untouched. The whole metadata column is rewritten by the repository on save, so a merge that
//     dropped keys would silently destroy them.
//
//   - IDEMPOTENT. `changed` is false when every identity key already holds the agent's value, which
//     lets the caller skip the write entirely. This matters because the durable fix re-asserts
//     identity on every daemon boot: without it, each boot would rewrite a row that did not need
//     rewriting.
//
//   - FAIL CLOSED on a blank field. An EMPTY value in the agent payload is treated as "unknown",
//     never as "erase what is stored". A partially-populated payload therefore cannot un-seed a
//     headquarters that is already correct — which would resurrect the exact outage this fixes.
//
// A nil agent is inert: a read that failed upstream carries no authority to modify the row.
func MergeAgentIdentity(metadata map[string]interface{}, agent *AgentData) (map[string]interface{}, bool) {
	if metadata == nil {
		metadata = make(map[string]interface{})
	}
	if agent == nil {
		return metadata, false
	}

	changed := false
	for key, value := range map[string]string{
		MetadataKeyHeadquarters:    agent.Headquarters,
		MetadataKeyStartingFaction: agent.StartingFaction,
		MetadataKeyAccountID:       agent.AccountID,
	} {
		if setIdentityKey(metadata, key, value) {
			changed = true
		}
	}
	return metadata, changed
}

// setIdentityKey writes one identity key iff the incoming value is non-empty AND differs from what
// is stored, reporting whether it wrote.
//
// A stored value that is not a string (hand-edited JSON, a number, a null) is treated as ABSENT
// rather than compared: the type assertion yields "", which differs from any non-empty incoming
// value, so the key is repaired rather than left in a shape its readers cannot use.
func setIdentityKey(metadata map[string]interface{}, key, value string) bool {
	if value == "" {
		return false
	}
	if stored, _ := metadata[key].(string); stored == value {
		return false
	}
	metadata[key] = value
	return true
}

// HeadquartersFrom reads the agent's home waypoint out of a decoded metadata map, reporting
// whether it was present and usable. Callers get ONE definition of "is the headquarters readable"
// instead of each repeating the key literal and the string assertion.
func HeadquartersFrom(metadata map[string]interface{}) (string, bool) {
	hq, _ := metadata[MetadataKeyHeadquarters].(string)
	return hq, hq != ""
}

// MissingHeadquartersHint is the operator-facing explanation attached wherever a missing
// headquarters is reported. The key's absence used to surface four layers from its cause — as a
// sensing cutover refusal — with nothing naming what was missing or how it gets populated. Any
// reader that fails on the key states this, so the fix is one line away from the error.
const MissingHeadquartersHint = "players.metadata." + MetadataKeyHeadquarters + " is written from /my/agent " +
	"when the era is registered and re-asserted on every daemon boot; if it is absent this player predates " +
	"that sync (restart the daemon for this player to repopulate it, or re-run the era registration)"
