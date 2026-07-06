package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

type fakeRegistrationAPI struct {
	status         *api.ServerStatus
	statusErr      error
	result         *api.RegisterResult
	registerErr    error
	registerCalled bool
}

func (f *fakeRegistrationAPI) GetServerStatus(ctx context.Context) (*api.ServerStatus, error) {
	return f.status, f.statusErr
}

func (f *fakeRegistrationAPI) Register(ctx context.Context, accountToken, agentSymbol, faction string) (*api.RegisterResult, error) {
	f.registerCalled = true
	return f.result, f.registerErr
}

type fakeRegistrationStore struct {
	openEra      *persistence.EraModel
	findErr      error
	createCalled bool
}

func (f *fakeRegistrationStore) FindOpenEra(ctx context.Context) (*persistence.EraModel, error) {
	return f.openEra, f.findErr
}

func (f *fakeRegistrationStore) CreatePlayerWithEra(ctx context.Context, player *persistence.PlayerModel, era *persistence.EraModel) error {
	f.createCalled = true
	return nil
}

func TestPlayerRegisterNewRefusesWhenOpenEraExists(t *testing.T) {
	client := &fakeRegistrationAPI{status: statusOn("2026-07-06")}
	store := &fakeRegistrationStore{openEra: &persistence.EraModel{Name: "torwind"}}
	var out bytes.Buffer

	err := runPlayerRegisterNew(context.Background(), client, store, "account-token", "ORION", "COSMIC", &out)

	require.Error(t, err)
	require.Contains(t, err.Error(), "torwind")
	require.False(t, client.registerCalled)
	require.False(t, store.createCalled)
}

func TestPlayerRegisterNewRefusesWithoutAccountToken(t *testing.T) {
	client := &fakeRegistrationAPI{status: statusOn("2026-07-06")}
	store := &fakeRegistrationStore{}
	var out bytes.Buffer

	err := runPlayerRegisterNew(context.Background(), client, store, "", "ORION", "COSMIC", &out)

	require.Error(t, err)
	require.False(t, client.registerCalled)
	require.False(t, store.createCalled)
}

func TestPlayerRegisterNewPersistsNothingWhenApiRegisterFails(t *testing.T) {
	client := &fakeRegistrationAPI{
		status:      statusOn("2026-07-06"),
		registerErr: errors.New("agent symbol already taken"),
	}
	store := &fakeRegistrationStore{}
	var out bytes.Buffer

	err := runPlayerRegisterNew(context.Background(), client, store, "account-token", "ORION", "COSMIC", &out)

	require.Error(t, err)
	require.True(t, client.registerCalled)
	require.False(t, store.createCalled)
}

func TestPlayerRegisterNewCreatesPlayerAndEraWithResetDateAndLowercaseName(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	store := persistence.NewEraRepository(db)

	client := &fakeRegistrationAPI{
		status: statusOn("2026-07-06"),
		result: &api.RegisterResult{Token: "agent-jwt-token", AgentSymbol: "ORION", Faction: "COSMIC"},
	}
	var out bytes.Buffer

	err = runPlayerRegisterNew(context.Background(), client, store, "account-token", "ORION", "COSMIC", &out)
	require.NoError(t, err)

	var players []persistence.PlayerModel
	require.NoError(t, db.Find(&players).Error)
	require.Len(t, players, 1)
	require.Equal(t, "ORION", players[0].AgentSymbol)
	require.Equal(t, "agent-jwt-token", players[0].Token)

	var eras []persistence.EraModel
	require.NoError(t, db.Find(&eras).Error)
	require.Len(t, eras, 1)
	require.Equal(t, "orion", eras[0].Name)
	require.Equal(t, "ORION", eras[0].AgentSymbol)
	require.Equal(t, players[0].ID, eras[0].PlayerID)
	require.NotNil(t, eras[0].UniverseResetDate)
	require.Equal(t, "2026-07-06", eras[0].UniverseResetDate.Format("2006-01-02"))
	require.NotNil(t, eras[0].RegisteredAt)
	require.NotNil(t, eras[0].Faction)
	require.Equal(t, "COSMIC", *eras[0].Faction)

	require.NotContains(t, out.String(), "agent-jwt-token")
}
