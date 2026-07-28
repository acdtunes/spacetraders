package player

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// sp-0eufi: players.metadata.headquarters had three readers and no reachable writer, so the
// sensing cutover refused every 30s and ALL frontier expansion was dead. These pin the merge
// contract the durable fix rests on: seed the key, preserve every other key, and stay quiet when
// nothing changed.

// Requirement 2: merge, never replace. starting_faction is the only key the live rows carried,
// and losing it would trade one silent breakage for another.
func TestMergeAgentIdentity_SeedsHeadquartersWithoutClobberingExistingKeys(t *testing.T) {
	metadata := map[string]interface{}{
		"starting_faction": "COSMIC",
		"operator_note":    "do not lose me",
	}
	agent := &AgentData{
		AccountID:       "acct-1",
		Symbol:          "TORWIND",
		Headquarters:    "X1-KP23-A1",
		StartingFaction: "COSMIC",
	}

	merged, changed := MergeAgentIdentity(metadata, agent)

	require.True(t, changed, "seeding a missing headquarters is a change")
	require.Equal(t, "X1-KP23-A1", merged["headquarters"])
	require.Equal(t, "COSMIC", merged["starting_faction"], "the pre-existing faction must survive")
	require.Equal(t, "do not lose me", merged["operator_note"], "unrelated keys must survive")
	require.Equal(t, "acct-1", merged["account_id"])
}

// Requirement 3: idempotent. A re-sync that finds everything already correct must report NO
// change, so the caller can skip the write entirely rather than thrash the row every boot.
func TestMergeAgentIdentity_IsIdempotent_ReportsNoChangeWhenAlreadyCorrect(t *testing.T) {
	agent := &AgentData{AccountID: "acct-1", Headquarters: "X1-KP23-A1", StartingFaction: "COSMIC"}
	metadata := map[string]interface{}{
		"account_id":       "acct-1",
		"headquarters":     "X1-KP23-A1",
		"starting_faction": "COSMIC",
	}

	merged, changed := MergeAgentIdentity(metadata, agent)

	require.False(t, changed, "re-syncing identical identity must not report a change")
	require.Equal(t, "X1-KP23-A1", merged["headquarters"])
}

// A nil metadata map is the fresh-registration shape; the merge must allocate rather than panic.
func TestMergeAgentIdentity_AllocatesWhenMetadataIsNil(t *testing.T) {
	agent := &AgentData{Headquarters: "X1-KP23-A1", StartingFaction: "COSMIC"}

	merged, changed := MergeAgentIdentity(nil, agent)

	require.True(t, changed)
	require.NotNil(t, merged)
	require.Equal(t, "X1-KP23-A1", merged["headquarters"])
}

// Fail closed. An agent payload with an EMPTY headquarters is not authority to erase a good
// stored value — that would hand the sensing cutover the same missing-key failure the fix
// exists to end, and would do it to a row that was already correct.
func TestMergeAgentIdentity_EmptyAgentValuesNeverOverwriteStoredOnes(t *testing.T) {
	metadata := map[string]interface{}{
		"headquarters":     "X1-KP23-A1",
		"starting_faction": "COSMIC",
		"account_id":       "acct-1",
	}
	blank := &AgentData{} // every field empty

	merged, changed := MergeAgentIdentity(metadata, blank)

	require.False(t, changed, "an empty agent payload changes nothing")
	require.Equal(t, "X1-KP23-A1", merged["headquarters"], "a good headquarters is never erased")
	require.Equal(t, "COSMIC", merged["starting_faction"])
	require.Equal(t, "acct-1", merged["account_id"])
}

// A nil agent is a read that failed upstream. It must be inert, not destructive.
func TestMergeAgentIdentity_NilAgentIsInert(t *testing.T) {
	metadata := map[string]interface{}{"headquarters": "X1-KP23-A1"}

	merged, changed := MergeAgentIdentity(metadata, nil)

	require.False(t, changed)
	require.Equal(t, "X1-KP23-A1", merged["headquarters"])
}

// A genuine relocation (a new era's agent under the same row) must be picked up, or the fix
// would seed once and then serve a stale home system forever.
func TestMergeAgentIdentity_UpdatesAChangedHeadquarters(t *testing.T) {
	metadata := map[string]interface{}{"headquarters": "X1-OLD-A1", "starting_faction": "COSMIC"}
	agent := &AgentData{Headquarters: "X1-NEW-B2", StartingFaction: "COSMIC"}

	merged, changed := MergeAgentIdentity(metadata, agent)

	require.True(t, changed, "a moved headquarters is a change")
	require.Equal(t, "X1-NEW-B2", merged["headquarters"])
	require.Equal(t, "COSMIC", merged["starting_faction"])
}

// The stored value may be a non-string (hand-edited JSON, a number, null). Treat it as absent
// and overwrite rather than panicking on the type assertion.
func TestMergeAgentIdentity_ReplacesNonStringStoredValue(t *testing.T) {
	metadata := map[string]interface{}{"headquarters": 42}
	agent := &AgentData{Headquarters: "X1-KP23-A1"}

	merged, changed := MergeAgentIdentity(metadata, agent)

	require.True(t, changed)
	require.Equal(t, "X1-KP23-A1", merged["headquarters"])
}
