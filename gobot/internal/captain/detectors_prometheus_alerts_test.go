package watchkeeper

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// prometheusAlertsFixture builds a minimal Prometheus /api/v1/alerts response
// body, mirroring the subset detectPrometheusAlerts actually reads (see
// prometheusAlertsAPIResponse in detectors.go).
func prometheusAlertsFixture(alerts ...map[string]any) []byte {
	body := map[string]any{
		"status": "success",
		"data":   map[string]any{"alerts": alerts},
	}
	b, err := json.Marshal(body)
	if err != nil {
		panic(err) // test fixture construction only; a marshal failure here is a test bug
	}
	return b
}

func firingAlert(name, summary, severity string) map[string]any {
	return map[string]any{
		"labels":      map[string]string{"alertname": name, "severity": severity},
		"annotations": map[string]string{"summary": summary},
		"state":       "firing",
	}
}

// TestDetectPrometheusAlertsRecordsFiringAlert: a firing alert becomes exactly
// one prometheus.alert_firing event, keyed by alertname, carrying the
// annotation/label detail the wake mail renders (describePrometheusAlert).
func TestDetectPrometheusAlertsRecordsFiringAlert(t *testing.T) {
	_, playerID, store := setupDB(t)
	now := time.Now()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/alerts", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(prometheusAlertsFixture(firingAlert("EarnerDark", "no heavy sells in 20m", "critical")))
	}))
	defer srv.Close()

	cfg := DetectorConfig{PlayerID: playerID, PrometheusAlertsURL: srv.URL}
	require.NoError(t, detectPrometheusAlerts(context.Background(), store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, captain.EventPrometheusAlertFiring, events[0].Type)
	require.Equal(t, "alert:EarnerDark", events[0].Ship)
	require.Contains(t, events[0].Payload, `"alertname":"EarnerDark"`)
	require.Contains(t, events[0].Payload, `"summary":"no heavy sells in 20m"`)
	require.Contains(t, events[0].Payload, `"severity":"critical"`)
}

// TestDetectPrometheusAlertsIgnoresNonFiringStates: pending/inactive alerts
// are not-yet (or no-longer) real conditions — only "firing" pages.
func TestDetectPrometheusAlertsIgnoresNonFiringStates(t *testing.T) {
	_, playerID, store := setupDB(t)
	now := time.Now()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(prometheusAlertsFixture(
			map[string]any{
				"labels":      map[string]string{"alertname": "ApproachCeiling"},
				"annotations": map[string]string{},
				"state":       "pending",
			},
			map[string]any{
				"labels":      map[string]string{"alertname": "BurstSaturation"},
				"annotations": map[string]string{},
				"state":       "inactive",
			},
		))
	}))
	defer srv.Close()

	cfg := DetectorConfig{PlayerID: playerID, PrometheusAlertsURL: srv.URL}
	require.NoError(t, detectPrometheusAlerts(context.Background(), store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Empty(t, events, "pending/inactive alerts must not page the captain")
}

// TestDetectPrometheusAlertsDisabledWhenURLEmpty: an empty PrometheusAlertsURL
// disables the detector entirely (matches the ExpectedStreams "empty means
// off" idiom) — no HTTP call is even attempted.
func TestDetectPrometheusAlertsDisabledWhenURLEmpty(t *testing.T) {
	_, playerID, store := setupDB(t)
	now := time.Now()

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write(prometheusAlertsFixture(firingAlert("EarnerDark", "x", "critical")))
	}))
	defer srv.Close()

	cfg := DetectorConfig{PlayerID: playerID, PrometheusAlertsURL: ""}
	require.NoError(t, detectPrometheusAlerts(context.Background(), store, cfg, now))

	require.Equal(t, 0, hits, "an empty PrometheusAlertsURL must disable the detector with no HTTP call")
	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Empty(t, events)
}

// TestDetectPrometheusAlertsCooldownSuppressesRepeat: a sustained firing
// alert reads true on every poll, so without a cooldown it would re-page the
// captain every tick until it clears. Mirrors the sibling factory income-stall
// cooldown test's idiom of backdating created_at directly rather than
// reasoning about synthetic-vs-wall-clock time.
func TestDetectPrometheusAlertsCooldownSuppressesRepeat(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(prometheusAlertsFixture(firingAlert("EarnerDark", "x", "critical")))
	}))
	defer srv.Close()

	cfg := DetectorConfig{PlayerID: playerID, PrometheusAlertsURL: srv.URL}

	require.NoError(t, detectPrometheusAlerts(context.Background(), store, cfg, now))
	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)

	// Still firing on the very next poll: must not re-page within the cooldown.
	require.NoError(t, detectPrometheusAlerts(context.Background(), store, cfg, now.Add(time.Minute)))
	events, err = store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1, "a sustained firing alert re-emitted within cooldown: would page every tick")

	// Backdate the recorded event past the cooldown window and confirm the
	// still-firing alert re-fires once the window elapses.
	require.NoError(t, db.Exec("UPDATE captain_events SET created_at = ?",
		now.Add(-defaultPrometheusAlertsCooldown-time.Minute)).Error)
	require.NoError(t, detectPrometheusAlerts(context.Background(), store, cfg, now.Add(time.Minute)))
	events, err = store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 2, "after the cooldown window elapses a persistent alert must re-fire")
}

// TestDetectPrometheusAlertsPropagatesFetchError: an unreachable Prometheus
// must surface as a Go error (so Tick's "detectors error (continuing" log
// line fires and the failure is observable), not a silent no-op success.
func TestDetectPrometheusAlertsPropagatesFetchError(t *testing.T) {
	_, playerID, store := setupDB(t)
	now := time.Now()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	srv.Close() // closed before any request: connection refused on every attempt

	cfg := DetectorConfig{PlayerID: playerID, PrometheusAlertsURL: srv.URL}
	err := detectPrometheusAlerts(context.Background(), store, cfg, now)
	require.Error(t, err, "an unreachable Prometheus must surface as a Go error, not silent success")
}
