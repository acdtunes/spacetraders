package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// sp-pr42: `player register --agent X --token Y` (importing an already-created
// agent's token into a fresh local DB) wrote only the player row and NO era row
// — only the --new path called CreatePlayerWithEra. The daemon then ran off
// primaryPlayerID's players[0] fallback, but `universe status` reported "Open
// era: NO ERA" and era/reset detection ran blind. The from-token path must open
// the era row too, mirroring --new: name = <symbol>-<resetDate> from live server
// status, persisted atomically with the player via CreatePlayerWithEra.

func TestPlayerRegisterFromTokenCreatesOpenEraRow(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	store := persistence.NewEraRepository(db)

	client := &fakeRegistrationAPI{status: statusOn("2026-07-06")}
	var out bytes.Buffer

	err = runPlayerRegisterFromToken(context.Background(), client, store, "ORION", "agent-jwt-token", "COSMIC", &out)
	require.NoError(t, err)

	var players []persistence.PlayerModel
	require.NoError(t, db.Find(&players).Error)
	require.Len(t, players, 1)
	require.Equal(t, "ORION", players[0].AgentSymbol)
	require.Equal(t, "agent-jwt-token", players[0].Token)

	var eras []persistence.EraModel
	require.NoError(t, db.Find(&eras).Error)
	require.Len(t, eras, 1, "from-token register must open an era row (sp-pr42)")
	// Era names are keyed by symbol + server reset date so the same agent symbol
	// can be re-registered in a later universe without colliding with the unique
	// eras.name constraint (same rule as the --new path).
	require.Equal(t, "orion-2026-07-06", eras[0].Name)
	require.Equal(t, "ORION", eras[0].AgentSymbol)
	require.Equal(t, players[0].ID, eras[0].PlayerID)
	require.Nil(t, eras[0].ClosedAt, "the era must be OPEN")
	require.NotNil(t, eras[0].UniverseResetDate)
	require.Equal(t, "2026-07-06", eras[0].UniverseResetDate.Format("2006-01-02"))
	require.NotNil(t, eras[0].RegisteredAt)
	require.NotNil(t, eras[0].Faction)
	require.Equal(t, "COSMIC", *eras[0].Faction)

	// The from-token path receives a caller-supplied token; it must not echo it.
	require.NotContains(t, out.String(), "agent-jwt-token")
}

func TestPlayerRegisterFromTokenRefusesWhenOpenEraExists(t *testing.T) {
	client := &fakeRegistrationAPI{status: statusOn("2026-07-06")}
	store := &fakeRegistrationStore{openEra: &persistence.EraModel{Name: "torwind"}}
	var out bytes.Buffer

	err := runPlayerRegisterFromToken(context.Background(), client, store, "ORION", "agent-jwt-token", "COSMIC", &out)

	require.Error(t, err)
	require.Contains(t, err.Error(), "torwind")
	require.False(t, store.createCalled, "must not persist a second player/era while an era is open")
}

func TestPlayerRegisterFromTokenRequiresAgentAndToken(t *testing.T) {
	client := &fakeRegistrationAPI{status: statusOn("2026-07-06")}
	var out bytes.Buffer

	err := runPlayerRegisterFromToken(context.Background(), client, &fakeRegistrationStore{}, "", "agent-jwt-token", "", &out)
	require.Error(t, err)

	err = runPlayerRegisterFromToken(context.Background(), client, &fakeRegistrationStore{}, "ORION", "", "", &out)
	require.Error(t, err)
}

func TestPlayerRegisterFromTokenFailsWhenServerResetDateUnparseable(t *testing.T) {
	client := &fakeRegistrationAPI{status: statusOn("not-a-date")}
	store := &fakeRegistrationStore{}
	var out bytes.Buffer

	err := runPlayerRegisterFromToken(context.Background(), client, store, "ORION", "agent-jwt-token", "COSMIC", &out)

	require.Error(t, err)
	require.False(t, store.createCalled, "must not persist when the era name cannot be derived")
}

func TestPlayerRegisterFromTokenSurfacesStatusError(t *testing.T) {
	client := &fakeRegistrationAPI{statusErr: errors.New("universe unreachable")}
	store := &fakeRegistrationStore{}
	var out bytes.Buffer

	err := runPlayerRegisterFromToken(context.Background(), client, store, "ORION", "agent-jwt-token", "COSMIC", &out)

	require.Error(t, err)
	require.False(t, store.createCalled)
}
