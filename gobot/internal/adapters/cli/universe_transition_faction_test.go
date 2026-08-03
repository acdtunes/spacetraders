package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// sp-dqbzm — TWO defects on one path.
//
// FIRST HALF: `universe transition` (the no --token path, which mints from
// ST_ACCOUNT_TOKEN) hardcoded faction "" into Register. The API rejects that with
// 422 code 3001 (zodIssues path [faction], invalid_enum_value, received ""), so the
// one-command era rollover could not self-serve — torwind-2026-08-02 had to be minted
// by hand with curl.
//
// SECOND HALF (latent, and NOT fixed by passing the flag through): the new era row's
// faction is read from GetAgent's startingFaction alone, and an empty read was skipped
// SILENTLY. That single externally-controlled read HAS come back empty on a real
// rollover: era 3 (torwind-2026-07-12) landed eras.faction NULL and players.metadata
// {} from that line and nobody noticed for three weeks. The mint path holds two better
// answers for the agent it just created — the register response's own echo and the
// validated --faction the API accepted — and threw both away.

// B1 (AC#1) — the mint sends a real faction enum, never the empty string that 422s.
// Input variations of one behaviour, parametrized: default, explicit, and the
// case/whitespace normalisation an operator will inevitably type.
func TestTransition_MintsWithAValidatedFactionNeverTheEmptyString(t *testing.T) {
	cases := []struct {
		name      string
		requested string
		sent      string
	}{
		{"defaults to the faction every prior era used", "", "COSMIC"},
		{"honours an explicit faction", "VOID", "VOID"},
		{"normalises case", "void", "VOID"},
		{"normalises surrounding whitespace", "  galactic  ", "GALACTIC"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, apiFake, store, _, _, _ := happyDeps()
			apiFake.registerResult = &api.RegisterResult{Token: "minted-jwt", AgentSymbol: "TORWIND", Faction: tc.sent}
			var out bytes.Buffer

			err := runUniverseTransition(context.Background(), deps, transitionOpts{
				agent: "TORWIND", accountToken: "acct-tok", faction: tc.requested, confirm: true,
			}, &out)
			require.NoError(t, err)

			require.Equal(t, 1, apiFake.registerCalls)
			require.Equal(t, tc.sent, apiFake.registerFaction)
			require.NotEmpty(t, apiFake.registerFaction, "an empty faction is exactly what the API 422s on")
			require.Equal(t, 1, store.transitionCalls)
		})
	}
}

// B2 (AC#3) — a faction the API would reject is refused LOCALLY: no Register (which
// consumes an irreversible account slot), no token validation, no era flip, no repoint,
// no drain. This preserves the validated-before-write guarantee the guard already had.
func TestTransition_InvalidFactionRefusedBeforeAnyApiCallOrWrite(t *testing.T) {
	for _, requested := range []string{"BOGUS", "COSMICC", "cosmic void", "COSMIC,VOID", "-"} {
		t.Run(requested, func(t *testing.T) {
			deps, apiFake, store, def, capCfg, fleet := happyDeps()
			apiFake.registerResult = &api.RegisterResult{Token: "minted-jwt", AgentSymbol: "TORWIND", Faction: "COSMIC"}
			var out bytes.Buffer

			err := runUniverseTransition(context.Background(), deps, transitionOpts{
				agent: "TORWIND", accountToken: "acct-tok", faction: requested, confirm: true,
			}, &out)

			require.Error(t, err)
			require.Contains(t, err.Error(), "--faction")
			require.Contains(t, err.Error(), "COSMIC", "the error must name the factions the API accepts")
			require.Contains(t, err.Error(), "no changes made")

			require.Zero(t, apiFake.registerCalls, "must not consume an account slot on a bad faction")
			require.False(t, apiFake.agentCalled, "no API calls at all on the fail-closed path")
			require.Zero(t, store.transitionCalls)
			require.False(t, def.called)
			require.False(t, capCfg.called)
			require.Empty(t, fleet.stopOrder)
		})
	}
}

// B3 (AC#4) — the SECOND HALF. On the mint path the new era row records a non-NULL
// faction unconditionally, walking a precedence chain from most to least authoritative:
// the live agent read, then the register response's echo, then the validated request.
// The third row is era 3 reproduced exactly — a silent API on both reads — and it is
// the case that used to write NULL.
func TestTransition_MintPathAlwaysRecordsAFactionOnTheNewEraRow(t *testing.T) {
	cases := []struct {
		name        string
		liveAgent   string
		mintedEcho  string
		requested   string
		eraRecords  string
		explanation string
	}{
		{"the live agent read wins when the API supplies it", "GALACTIC", "VOID", "VOID", "GALACTIC",
			"what the agent IS outranks what we asked for"},
		{"the register echo backfills a silent agent read", "", "VOID", "VOID", "VOID",
			"the mint response already told us the faction"},
		{"the validated request backfills when both reads are silent", "", "", "VOID", "VOID",
			"era-3 regression: this combination used to write NULL"},
		{"the default backfills when nothing at all is known", "", "", "", "COSMIC",
			"era-3 regression with no explicit --faction"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, apiFake, store, _, _, _ := happyDeps()
			apiFake.agent = &player.AgentData{Symbol: "TORWIND", Credits: 1000, StartingFaction: tc.liveAgent}
			apiFake.registerResult = &api.RegisterResult{Token: "minted-jwt", AgentSymbol: "TORWIND", Faction: tc.mintedEcho}
			var out bytes.Buffer

			err := runUniverseTransition(context.Background(), deps, transitionOpts{
				agent: "TORWIND", accountToken: "acct-tok", faction: tc.requested, confirm: true,
			}, &out)
			require.NoError(t, err)

			require.Equal(t, 1, store.transitionCalls)
			require.NotNil(t, store.capturedEra.Faction, "eras.faction must not land NULL on a mint (%s)", tc.explanation)
			require.Equal(t, tc.eraRecords, *store.capturedEra.Faction, tc.explanation)

			// The player row's identity metadata is fed from the SAME value, and era 3
			// lost it too ({}). Fixing one sink and not the other leaves half the hole.
			require.Contains(t, store.capturedPlayer.Metadata, "starting_faction")
			require.Contains(t, store.capturedPlayer.Metadata, tc.eraRecords)
		})
	}
}

// B4 — the --token path imports an agent minted elsewhere, so a --faction default is
// a guess, not knowledge. It must never be stamped onto the era row. But the silence
// is what hid era 3 for three weeks, so an unknowable faction is announced loudly
// instead of skipped quietly.
func TestTransition_TokenPathNeverInventsAFactionAndSaysSoWhenItCannotKnow(t *testing.T) {
	deps, apiFake, store, _, _, _ := happyDeps()
	apiFake.agent = &player.AgentData{Symbol: "TORWIND", Credits: 1000, StartingFaction: ""}
	apiFake.registerResult = &api.RegisterResult{Token: "unused", AgentSymbol: "TORWIND", Faction: "COSMIC"}
	var out bytes.Buffer

	err := runUniverseTransition(context.Background(), deps, transitionOpts{
		agent: "TORWIND", token: "explicit-jwt", accountToken: "acct-tok", faction: "VOID", confirm: true,
	}, &out)
	require.NoError(t, err)

	require.Zero(t, apiFake.registerCalls, "an explicit --token must not mint")
	require.Equal(t, 1, store.transitionCalls)
	require.Nil(t, store.capturedEra.Faction,
		"the agent was minted elsewhere — recording the --faction default would fabricate history")
	require.Contains(t, strings.ToLower(out.String()), "startingfaction",
		"an unknowable faction must be announced, not silently skipped (this is what hid era 3)")
}

// B4b — the same --token path with a live faction still records it: the non-fabrication
// rule must not become an excuse to drop a faction the API did report.
func TestTransition_TokenPathRecordsTheFactionTheApiReports(t *testing.T) {
	deps, _, store, _, _, _ := happyDeps() // happyDeps' agent reports COSMIC
	var out bytes.Buffer

	err := runUniverseTransition(context.Background(), deps, transitionOpts{
		agent: "TORWIND", token: "explicit-jwt", confirm: true,
	}, &out)
	require.NoError(t, err)

	require.NotNil(t, store.capturedEra.Faction)
	require.Equal(t, "COSMIC", *store.capturedEra.Faction)
	require.NotContains(t, strings.ToLower(out.String()), "startingfaction", "no warning when the faction is known")
}

// B3 at the real persistence boundary. AC#4 is a statement about a DB column, and the
// fake era store can only prove what the CLI HANDS to TransitionEra. This runs the same
// mint-with-a-silent-API scenario through the actual EraRepository against a real
// (migrated, in-memory) database and reads eras.faction back out of the column that was
// NULL for era 3.
func TestTransition_MintPathPersistsANonNullEraFaction(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	priorReset := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	registeredAt := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	priorPlayer := &persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "old-jwt", CreatedAt: priorReset}
	priorEra := &persistence.EraModel{
		Name: "torwind-2026-07-05", AgentSymbol: "TORWIND",
		RegisteredAt: &registeredAt, UniverseResetDate: &priorReset,
	}
	store := persistence.NewEraRepository(db)
	require.NoError(t, store.CreatePlayerWithEra(context.Background(), priorPlayer, priorEra))

	// The era-3 condition: the API reports no startingFaction on EITHER read.
	apiFake := &fakeTransitionAPI{
		agent:          &player.AgentData{Symbol: "TORWIND", Credits: 1000, StartingFaction: ""},
		status:         statusOn("2026-07-12"),
		registerResult: &api.RegisterResult{Token: "minted-jwt", AgentSymbol: "TORWIND", Faction: ""},
	}
	fleet := &fakeFleet{notFound: map[string]bool{}}
	deps := transitionDeps{
		api: apiFake, era: store, cliDefault: &fakeDefaultSetter{}, captainCfg: &fakeCaptainCfg{},
		lister: fleet, stopper: fleet, reconciler: fleet,
	}
	var out bytes.Buffer

	err = runUniverseTransition(context.Background(), deps, transitionOpts{
		agent: "TORWIND", accountToken: "acct-tok", faction: "VOID", confirm: true,
	}, &out)
	require.NoError(t, err)

	var stored persistence.EraModel
	require.NoError(t, db.Where("name = ?", "torwind-2026-07-12").First(&stored).Error)
	require.NotNil(t, stored.Faction, "eras.faction landed NULL — this is the era-3 regression")
	require.Equal(t, "VOID", *stored.Faction)
}

// B2b — the preview path validates the faction too, so `--dry-run` cannot promise a
// rollover that the subsequent `--confirm` would refuse, and it states which faction
// the irreversible mint will use.
func TestTransition_PreviewValidatesAndStatesTheMintFaction(t *testing.T) {
	deps, apiFake, store, _, _, _ := happyDeps()
	var out bytes.Buffer

	err := runUniverseTransition(context.Background(), deps, transitionOpts{
		agent: "TORWIND", accountToken: "acct-tok", faction: "BOGUS",
	}, &out)
	require.Error(t, err)
	require.Zero(t, apiFake.registerCalls)
	require.Zero(t, store.transitionCalls)

	out.Reset()
	err = runUniverseTransition(context.Background(), deps, transitionOpts{
		agent: "TORWIND", accountToken: "acct-tok", faction: "void",
	}, &out)
	require.NoError(t, err)
	require.Zero(t, apiFake.registerCalls, "a preview must still not mint")
	require.Contains(t, out.String(), "VOID", "the preview must state the faction the mint will use")
}
