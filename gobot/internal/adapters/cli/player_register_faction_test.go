package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// sp-dqbzm — the companion defect. `universe transition` was not the only caller
// shipping an empty faction into Register: `player register --new` declares
// --faction "(optional)" with an empty default and passes it straight through, so it
// hits the identical 422 (code 3001, invalid_enum_value, received ""). Both mint
// paths now resolve and validate through the same gate.

// B5 (companion AC#1) — `player register --new` mints with a real faction enum.
func TestPlayerRegisterNewMintsWithAValidatedFactionNeverTheEmptyString(t *testing.T) {
	cases := []struct {
		name      string
		requested string
		sent      string
	}{
		{"defaults to the faction every prior era used", "", "COSMIC"},
		{"honours an explicit faction", "VOID", "VOID"},
		{"normalises case", "void", "VOID"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := database.NewTestConnection()
			require.NoError(t, err)
			store := persistence.NewEraRepository(db)

			client := &fakeRegistrationAPI{
				status: statusOn("2026-07-06"),
				result: &api.RegisterResult{Token: "agent-jwt-token", AgentSymbol: "ORION", Faction: tc.sent},
			}
			var out bytes.Buffer

			err = runPlayerRegisterNew(context.Background(), client, store, "account-token", "ORION", tc.requested, &out)
			require.NoError(t, err)

			require.Equal(t, tc.sent, client.registerFaction)
			require.NotEmpty(t, client.registerFaction, "an empty faction is exactly what the API 422s on")

			var eras []persistence.EraModel
			require.NoError(t, db.Find(&eras).Error)
			require.Len(t, eras, 1)
			require.NotNil(t, eras[0].Faction, "a minted era knows its faction and must record it")
			require.Equal(t, tc.sent, *eras[0].Faction)
		})
	}
}

// B6 (companion AC#3) — an invalid faction is refused before the irreversible mint.
func TestPlayerRegisterNewRefusesAnInvalidFactionBeforeMinting(t *testing.T) {
	client := &fakeRegistrationAPI{
		status: statusOn("2026-07-06"),
		result: &api.RegisterResult{Token: "agent-jwt-token", AgentSymbol: "ORION", Faction: "COSMIC"},
	}
	store := &fakeRegistrationStore{}
	var out bytes.Buffer

	err := runPlayerRegisterNew(context.Background(), client, store, "account-token", "ORION", "BOGUS", &out)

	require.Error(t, err)
	require.Contains(t, err.Error(), "--faction")
	require.Contains(t, err.Error(), "COSMIC")
	require.False(t, client.registerCalled, "must not consume an account slot on a bad faction")
	require.False(t, store.createCalled)
}

// B7 — the from-token path RECORDS a faction, it never sends one: the agent was minted
// elsewhere. An unsupplied faction must therefore stay unknown (NULL) rather than
// acquire the mint default, but a supplied one is still validated so the history
// report cannot inherit a typo.
func TestPlayerRegisterFromTokenDoesNotInventAFactionAndValidatesTheOneItIsGiven(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	store := persistence.NewEraRepository(db)
	client := &fakeRegistrationAPI{status: statusOn("2026-07-06")}
	var out bytes.Buffer

	err = runPlayerRegisterFromToken(context.Background(), client, store, "ORION", "agent-jwt-token", "", &out)
	require.NoError(t, err)

	var eras []persistence.EraModel
	require.NoError(t, db.Find(&eras).Error)
	require.Len(t, eras, 1)
	require.Nil(t, eras[0].Faction, "an imported token's faction is unknown — do not stamp the mint default on it")

	rejectStore := &fakeRegistrationStore{}
	err = runPlayerRegisterFromToken(context.Background(), client, rejectStore, "ORION", "agent-jwt-token", "BOGUS", &out)
	require.Error(t, err)
	require.Contains(t, err.Error(), "--faction")
	require.False(t, rejectStore.createCalled, "a typo must not be persisted as history")
}
