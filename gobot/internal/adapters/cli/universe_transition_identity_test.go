package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
)

// sp-0eufi requirement 1: a fresh registration on the NEXT era must carry headquarters with no
// manual step. The era rollover already validates the token by calling /my/agent, so the value is
// sitting in `agentData` at the moment the new players row is built — it was simply dropped, and
// only starting_faction was persisted. This pins that it is written at birth.
//
// The boot-time sync would eventually repair the row anyway, but only after the daemon starts:
// seeding here means the row is never wrong in the first place, so the first sensing tick of a new
// era does not have to fail before something fixes it.
func TestAgentIdentityMetadata_CarriesHeadquartersFromTheValidatedAgent(t *testing.T) {
	agent := &player.AgentData{
		AccountID:       "acct-9",
		Symbol:          "TORWIND",
		Headquarters:    "X1-KP23-A1",
		StartingFaction: "COSMIC",
	}

	raw := agentIdentityMetadata(agent)

	var got map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(raw), &got), "the era row must persist valid JSON")
	require.Equal(t, "X1-KP23-A1", got["headquarters"],
		"the era rollover must seed headquarters, not drop it")
	require.Equal(t, "COSMIC", got["starting_faction"], "the pre-existing faction key is still written")
	require.Equal(t, "acct-9", got["account_id"])
}

// A blank agent yields an empty metadata column rather than a JSON object full of empty strings —
// matching what the prior factionMetadata("") did, so nothing downstream sees a new shape.
func TestAgentIdentityMetadata_BlankAgentWritesNoMetadata(t *testing.T) {
	require.Equal(t, "", agentIdentityMetadata(&player.AgentData{}),
		"an agent payload with nothing in it must not write an object of empty strings")
	require.Equal(t, "", agentIdentityMetadata(nil), "a nil agent writes nothing")
}

// Partial data is still worth persisting: a faction with no headquarters must not be discarded
// just because the other key is missing.
func TestAgentIdentityMetadata_PersistsWhatIsKnownWhenHeadquartersIsAbsent(t *testing.T) {
	raw := agentIdentityMetadata(&player.AgentData{StartingFaction: "COSMIC"})

	var got map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(raw), &got))
	require.Equal(t, "COSMIC", got["starting_faction"])
	require.NotContains(t, got, "headquarters", "an unknown headquarters is absent, never empty-string")
}

// WIRING, not just the helper. The mutation probe for sp-0eufi found that reverting the era
// rollover's CALL SITE to the old faction-only metadata left this entire package green: every
// assertion above exercises agentIdentityMetadata directly, so none of them notices if the
// rollover stops calling it. That is precisely the shape of test this project has shipped before —
// one that pins a function nothing is required to use.
//
// This closes it end-to-end: run the real rollover and assert on the player row actually handed to
// the era store, which is what lands in the database and what every sensing tick then reads.
func TestTransition_NewEraPlayerRowCarriesHeadquarters(t *testing.T) {
	deps, apiFake, store, _, _, _ := happyDeps()
	apiFake.agent = &player.AgentData{
		Symbol: "TORWIND", Headquarters: "X1-KP23-A1", Credits: 1000,
		StartingFaction: "COSMIC", AccountID: "acct-9",
	}

	err := runUniverseTransition(context.Background(),
		deps, transitionOpts{agent: "TORWIND", token: "valid-jwt", confirm: true}, &bytes.Buffer{})
	require.NoError(t, err)
	require.NotNil(t, store.capturedPlayer, "the rollover must have created a new player row")

	var metadata map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(store.capturedPlayer.Metadata), &metadata),
		"the persisted metadata column must be valid JSON")

	require.Equal(t, "X1-KP23-A1", metadata["headquarters"],
		"the new era's player row must carry headquarters, or the next era boots into the same "+
			"sensing-cutover outage this fix exists to end")
	require.Equal(t, "COSMIC", metadata["starting_faction"],
		"the faction key the rollover already persisted must not be lost")
}
