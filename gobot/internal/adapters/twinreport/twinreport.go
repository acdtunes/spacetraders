// Package twinreport is a test-gated seam that lets the bootstrap coordinator
// report daemon-internal operations to the digital-twin.
//
// Some coordinator ops have NO SpaceTraders /v2 API call (fleet-unassign,
// batch-contract, construction-start, executor-bounce, repurpose,
// launch-autosizer/siting/worker-rebalancer, scout-assign), so the e2e harness
// cannot observe them by watching the game API. Instead the twin exposes
// POST /_twin/report {call, detail?} which flips the paired flag and appends a
// mutationLog entry; the coordinator calls Report right where each such op fires.
//
// This is a strict no-op in production: Report does nothing unless the
// TWIN_REPORT_URL environment variable is set (the harness sets it to
// http://127.0.0.1:8080/_twin/report). When it is unset, Report returns before
// touching the network, so the coordinator's production behaviour is unchanged.
package twinreport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"time"
)

// reportTimeout caps the fire-and-forget POST so a slow or hung twin can never
// stall the reconcile loop.
const reportTimeout = 2 * time.Second

// Report POSTs {"call":call,"detail":detail} to the URL in TWIN_REPORT_URL when
// that env var is set, and otherwise returns immediately (production no-op).
//
// It is fire-and-forget: it swallows every error and never panics, so it can be
// dropped in next to a coordinator op-site without changing that op's
// success/failure semantics or slowing the reconcile loop.
func Report(call string, detail map[string]any) {
	url := os.Getenv("TWIN_REPORT_URL")
	if url == "" {
		return // production: no reporter wired, do nothing
	}

	body, err := json.Marshal(struct {
		Call   string         `json:"call"`
		Detail map[string]any `json:"detail"`
	}{Call: call, Detail: detail})
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	// Drain and close so the connection can be reused and nothing leaks.
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}
