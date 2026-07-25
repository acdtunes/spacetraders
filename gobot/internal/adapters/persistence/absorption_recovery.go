package persistence

import (
	"encoding/json"
	"math"
	"os"
	"time"
)

// absorptionRecoveryModel is the Go-side reader of the fitted market model's
// recovery curve. The daemon loads the SAME market_model.json the routing service
// fits (RULINGS #5: path is a flag; the fit pipeline and artifact are untouched),
// and this holds only the `recovery` section: a per-activity-tier
// half-life in minutes. An EXECUTED absorption shadow of `units` decays as
// units × 0.5^(elapsed / half_life(tier)) — the honest, fail-closed expression of
// the recovery externality in QUANTITY space (design §3(b)): nobody, including the
// absorber's own next plan, can step into the hole until the model says it has
// regrown, and the absorber pays no synthetic tax.
//
// UNTAGGED sinks (empty activity) decay on the artifact's POOLED ("") fit. They used to
// be excluded — no shadow at all — which meant consecutive plans saw virgin depth and
// lawfully rebuilt the whole tranche ladder at the same sink. Untagged
// is roughly a quarter of the live market universe, so that was where the ladder was
// actually being rebuilt. The pooled fit is the SAME number the solver's thin-tier
// fallback prices against; the 12h hard cap remains the outer bound on any shadow.
type absorptionRecoveryModel struct {
	// halfLives maps activity tier → fitted recovery half-life, including the pooled
	// ("") fit that untagged sinks decay on.
	halfLives map[string]time.Duration
	// loaded is false when the artifact could not be read/parsed. A reader then
	// treats every EXECUTED residual as UNDECAYED (full units) until its 12h hard
	// cap — fail closed on reserved depth (RULINGS #4), never freeing depth we
	// cannot confirm has regrown.
	loaded bool
}

// recoveryArtifact is the minimal shape decoded from market_model.json — only the
// `recovery` map this reader needs. Unknown top-level fields (impact, diagnostics,
// era, …) are ignored by the JSON decoder, so the artifact schema can grow without
// touching this loader.
type recoveryArtifact struct {
	Recovery map[string]struct {
		HalfLifeMinutes float64 `json:"half_life_minutes"`
	} `json:"recovery"`
}

// loadAbsorptionRecoveryModel reads the recovery half-lives from the fitted market
// artifact at path. An unreadable or unparseable artifact yields an UNLOADED model
// (loaded=false) rather than an error: the ledger still functions (writes shadows,
// enforces the hard cap) and reads fail closed by treating residuals as undecayed
// until the hard cap — the daemon must never refuse to boot because a model file is
// momentarily absent (design §2, Go-side model access).
func loadAbsorptionRecoveryModel(path string) *absorptionRecoveryModel {
	m := &absorptionRecoveryModel{halfLives: map[string]time.Duration{}}
	if path == "" {
		return m
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	var art recoveryArtifact
	if err := json.Unmarshal(raw, &art); err != nil {
		return m
	}
	for tier, entry := range art.Recovery {
		if entry.HalfLifeMinutes <= 0 {
			continue // a non-positive half-life would make decay undefined
		}
		m.halfLives[tier] = time.Duration(entry.HalfLifeMinutes * float64(time.Minute))
	}
	m.loaded = len(m.halfLives) > 0
	return m
}

// decayedUnits returns the still-occupied depth of an EXECUTED shadow of `units`
// after `elapsed`, per units × 0.5^(elapsed/half_life(tier)). A tier with no known
// half-life (unknown, or the artifact unloaded) decays not at all — the full units
// stand until the hard cap sweeps the row (fail closed, RULINGS #4).
// elapsed ≤ 0 returns the full units (a just-written shadow).
func (m *absorptionRecoveryModel) decayedUnits(units int, tier string, elapsed time.Duration) float64 {
	if units <= 0 {
		return 0
	}
	if elapsed <= 0 {
		return float64(units)
	}
	hl, ok := m.halfLives[tier]
	if !ok || hl <= 0 {
		return float64(units) // fail closed: undecayed until the hard cap
	}
	return float64(units) * math.Pow(0.5, elapsed.Seconds()/hl.Seconds())
}
