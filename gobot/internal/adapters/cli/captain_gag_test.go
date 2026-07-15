package cli

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	watchkeeper "github.com/andrescamacho/spacetraders-go/internal/captain"
)

type fakeGagPolicyStore struct {
	loaded  watchkeeper.GagPolicy
	loadErr error
	saved   *watchkeeper.GagPolicy
	saveErr error
}

func (f *fakeGagPolicyStore) Load() (watchkeeper.GagPolicy, error) {
	return f.loaded, f.loadErr
}

func (f *fakeGagPolicyStore) Save(policy watchkeeper.GagPolicy) error {
	f.saved = &policy
	return f.saveErr
}

func TestRunGagSetOnPersistsGaggedWithReasonAndTimestamp(t *testing.T) {
	store := &fakeGagPolicyStore{}
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)

	err := runGagSet(store, now, true, "admiral halt")
	require.NoError(t, err)
	require.NotNil(t, store.saved)
	require.True(t, store.saved.Gagged, "gag on must persist Gagged=true")
	require.Equal(t, "admiral halt", store.saved.GagReason)
	require.Equal(t, now, store.saved.GagDeclaredAt)
}

func TestRunGagSetOffPersistsUngagged(t *testing.T) {
	store := &fakeGagPolicyStore{loaded: watchkeeper.GagPolicy{Gagged: true, GagReason: "old"}}
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)

	err := runGagSet(store, now, false, "")
	require.NoError(t, err)
	require.NotNil(t, store.saved)
	require.False(t, store.saved.Gagged, "gag off must persist Gagged=false")
}

func TestRunGagStatusRendersOffWhenUndeclared(t *testing.T) {
	store := &fakeGagPolicyStore{}
	var buf bytes.Buffer
	require.NoError(t, runGagShow(store, &buf))
	out := buf.String()
	require.Contains(t, out, "gagged:")
	require.Contains(t, out, "off")
}

func TestRunGagStatusRendersOnWithReasonAndTimestamp(t *testing.T) {
	declaredAt := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	store := &fakeGagPolicyStore{loaded: watchkeeper.GagPolicy{
		Gagged: true, GagReason: "deploy freeze", GagDeclaredAt: declaredAt,
	}}
	var buf bytes.Buffer
	require.NoError(t, runGagShow(store, &buf))
	out := buf.String()
	require.Contains(t, out, "on")
	require.Contains(t, out, "deploy freeze")
	require.Contains(t, out, "2026-07-15T12:00:00Z")
}
