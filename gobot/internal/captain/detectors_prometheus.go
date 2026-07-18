// detectors_prometheus.go — sp-y0f6 Prometheus alert-firing poller: reads
// Prometheus's own /api/v1/alerts and records prometheus.alert_firing events.
// Split out of detectors.go for navigability; behavior unchanged.
package watchkeeper

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// prometheusAlertsAPIResponse is the subset of Prometheus's own
// /api/v1/alerts response body (sp-y0f6) this detector reads. Prometheus
// evaluates its rule_files and exposes the resulting alert states on this
// endpoint WITHOUT requiring an Alertmanager deployment — there is none in
// this stack today, so polling Prometheus directly is the lightest correct
// join to the watchkeeper's existing tick loop.
type prometheusAlertsAPIResponse struct {
	Data struct {
		Alerts []struct {
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
			State       string            `json:"state"`
		} `json:"alerts"`
	} `json:"data"`
}

// detectPrometheusAlerts polls Prometheus's own alert-evaluation state
// (sp-y0f6) and records one interrupt-class prometheus.alert_firing event per
// firing alertname: EarnerDark, BurstSaturation, ApproachCeiling,
// StarvationWave (gobot/configs/prometheus/rules/fleet-health.yml). This is
// the alert layer for the 2026-07-11 incident (sp-4hl5): the fleet earned
// zero for 2h50m and nothing paged, a human caught the flatline on a chart
// ~60min after onset. Empty PrometheusAlertsURL disables the detector
// entirely — no HTTP call, matching the ExpectedStreams/RegimeTripwires
// "empty means off" idiom used throughout this file.
//
// Deliberately no DB parameter: unlike its siblings this detector's source of
// truth is Prometheus's HTTP API, not the local database, so it mirrors
// detectCreditsCrossing's signature rather than the db-taking majority.
func detectPrometheusAlerts(ctx context.Context, store captain.EventStore, cfg DetectorConfig, now time.Time) error {
	if cfg.PrometheusAlertsURL == "" {
		return nil // disabled
	}
	url := strings.TrimRight(cfg.PrometheusAlertsURL, "/") + "/api/v1/alerts"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("prometheus alerts request: %w", err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("prometheus alerts fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("prometheus alerts fetch: unexpected status %d", resp.StatusCode)
	}
	var parsed prometheusAlertsAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return fmt.Errorf("prometheus alerts decode: %w", err)
	}

	for _, a := range parsed.Data.Alerts {
		if a.State != "firing" {
			continue // pending/inactive — not yet (or no longer) a real condition.
		}
		name := a.Labels["alertname"]
		if name == "" {
			continue
		}
		// Dedup per alertname, not per label set: the same alert re-evaluating
		// firing=true on every poll must not re-wake the captain every tick
		// while the underlying condition persists (mirrors the sibling
		// detectors' HasSince cooldown idiom).
		key := "alert:" + name
		recent, err := store.HasSince(ctx, cfg.PlayerID, captain.EventPrometheusAlertFiring, key, now.Add(-defaultPrometheusAlertsCooldown))
		if err != nil {
			return err
		}
		if recent {
			continue
		}
		payload, err := json.Marshal(map[string]string{
			"alertname": name,
			"summary":   a.Annotations["summary"],
			"severity":  a.Labels["severity"],
		})
		if err != nil {
			return err
		}
		if err := store.Record(ctx, &captain.Event{
			Type: captain.EventPrometheusAlertFiring, Ship: key, PlayerID: cfg.PlayerID,
			Payload: string(payload),
		}); err != nil {
			return err
		}
	}
	return nil
}
