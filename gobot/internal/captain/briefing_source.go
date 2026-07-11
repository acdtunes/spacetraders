package watchkeeper

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"gorm.io/gorm"
)

// briefing_source.go composes the sp-g2w6 wake-briefing from the watchkeeper's
// EXISTING readers: the same *gorm.DB the detectors query and the same
// Prometheus base URL the alert detector polls (no new pools, per the spec).
// Every read is FAIL-OPEN: a reader returns nil (→ "n/a") on any error, and the
// pure renderBriefing (briefing.go) turns the assembled snapshot into text.
// Nothing here can block or fail a wake.

// briefingPromTimeout bounds each Prometheus HTTP read. Short: a briefing is
// best-effort observability prepended to a wake, never worth stalling delivery.
const briefingPromTimeout = 3 * time.Second

// briefingStaleMarket is the COVERAGE staleness cutoff (a market unscanned
// longer than this is "stale"), matching the ~3h operator threshold in the spec.
const briefingStaleMarket = 3 * time.Hour

// Prometheus metric names the briefing reads (namespaced spacetraders_daemon_*,
// the registry's prefix). All confirmed present in internal/adapters/metrics.
const (
	metricChainKills     = "spacetraders_daemon_chain_pnl_kills_total"
	metricChainInputPause = "spacetraders_daemon_chain_input_pause_total"
	metricAPIRequests    = "spacetraders_daemon_api_requests_total"
	metricAPIWaitBucket  = "spacetraders_daemon_api_rate_limit_wait_seconds_bucket"
)

// Briefing composes the wake-briefing block from live PG + Prometheus state.
// Construct once (the composer caches the era-reset lookup) and reuse across
// wakes.
type Briefing struct {
	db       *gorm.DB
	playerID int
	// promURL is the Prometheus base URL; empty disables every Prometheus line
	// (they render "n/a"), matching the alert detector's "empty means off" idiom.
	promURL string
	floors  []int // credit floor thresholds for RUNWAY
	http    *http.Client
	// eraReset resolves the next universe-reset time for the T-6h INVENTORY
	// promotion. nil (unwired) fails open to no promotion.
	eraReset func(context.Context) *time.Time
}

// NewBriefing builds a composer over the given db and Prometheus base URL.
// floors are the captain-configured credit thresholds used for RUNWAY.
func NewBriefing(db *gorm.DB, playerID int, promURL string, floors []int) *Briefing {
	return &Briefing{
		db:       db,
		playerID: playerID,
		promURL:  strings.TrimRight(promURL, "/"),
		floors:   floors,
		http:     &http.Client{Timeout: briefingPromTimeout},
	}
}

// SetEraResetSource wires the era-end countdown to the watchkeeper's existing
// server-status source (the universe-reset detector's), cached so a wake costs
// at most one status call per TTL. Optional: unset means no era promotion.
func (b *Briefing) SetEraResetSource(status serverStatusSource) {
	b.eraReset = newCachedEraResetReader(status)
}

// Compose builds the briefing block for a wake at now, with sinceLastWake the
// gap to the previous wake (drives the "since wake" deltas). Total and
// fail-open: never errors, never panics on a failed read.
func (b *Briefing) Compose(ctx context.Context, now time.Time, sinceLastWake time.Duration) string {
	var eraReset *time.Time
	if b.eraReset != nil {
		eraReset = b.eraReset(ctx)
	}
	d := briefingData{
		Treasury:    b.readTreasury(ctx, now, sinceLastWake),
		Trend:       b.readTrend(ctx, now),
		Floors:      b.floors,
		Guards:      b.readGuards(ctx, sinceLastWake),
		Posture:     b.readPosture(ctx),
		Earners:     b.readEarners(ctx, now),
		ProfitMix:   b.readProfitMix(ctx, now),
		BurnWatch:   b.readBurnWatch(ctx, now),
		API:         b.readAPI(ctx),
		Alerts:      b.readAlerts(ctx),
		Coverage:    b.readCoverage(ctx, now),
		Inventory:   b.readInventory(ctx),
		Engineering: b.readEngineering(ctx, now, sinceLastWake),
		EraReset:    eraReset,
	}
	return renderBriefing(d, now)
}

// composeBriefing builds the wake-briefing block for the Supervisor, honoring
// the live-by-default briefing_disabled knob (RULINGS #5). It is belt-and-
// suspenders fail-open: the disabled check short-circuits, and a recover()
// guarantees that even a panic inside a reader degrades to an empty block
// rather than aborting the wake (the doctrine: a briefing must NEVER block a
// wake). now is the wake time; the "since last wake" gap is now - lastSession.
func (s *Supervisor) composeBriefing(ctx context.Context, now time.Time) (block string) {
	if s.cfg.BriefingDisabled {
		return ""
	}
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("watchkeeper: briefing panicked, omitting from wake (fail-open): %v\n", r)
			block = ""
		}
	}()
	if s.briefing == nil {
		s.briefing = NewBriefing(s.db, s.cfg.PlayerID, s.promAlertsURL, s.cfg.CreditsThresholds)
		if s.status != nil {
			s.briefing.SetEraResetSource(s.status)
		}
	}
	since := time.Duration(0)
	if !s.lastSession.IsZero() {
		since = now.Sub(s.lastSession)
	}
	return s.briefing.Compose(ctx, now, since)
}

// sumAmount runs COALESCE(SUM(amount),0) over the player's ledger with an
// optional extra predicate. ok=false on a query error (fail-open).
func (b *Briefing) sumAmount(ctx context.Context, extraWhere string, args ...interface{}) (int, bool) {
	q := b.db.WithContext(ctx).Model(&persistence.TransactionModel{}).Where("player_id = ?", b.playerID)
	if extraWhere != "" {
		q = q.Where(extraWhere, args...)
	}
	var sum int64
	if err := q.Select("COALESCE(SUM(amount),0)").Scan(&sum).Error; err != nil {
		return 0, false
	}
	return int(sum), true
}

func (b *Briefing) readTreasury(ctx context.Context, now time.Time, since time.Duration) *treasuryData {
	var balances []int
	if err := b.db.WithContext(ctx).Model(&persistence.TransactionModel{}).
		Where("player_id = ?", b.playerID).Order("created_at DESC").Limit(1).
		Pluck("balance_after", &balances).Error; err != nil || len(balances) == 0 {
		return nil // no ledger row -> no live balance to show
	}
	t := &treasuryData{Balance: balances[0]}
	if since > 0 {
		if delta, ok := b.sumAmount(ctx, "created_at >= ?", now.Add(-since)); ok {
			t.DeltaSinceWake = &delta
		}
	}
	return t
}

func (b *Briefing) readTrend(ctx context.Context, now time.Time) *trendData {
	last, ok1 := b.sumAmount(ctx, "created_at >= ?", now.Add(-time.Hour))
	prior, ok2 := b.sumAmount(ctx, "created_at >= ? AND created_at < ?", now.Add(-2*time.Hour), now.Add(-time.Hour))
	sum6, ok3 := b.sumAmount(ctx, "created_at >= ?", now.Add(-6*time.Hour))
	if !ok1 || !ok2 || !ok3 {
		return nil
	}
	tr := &trendData{NetLastHour: last, NetPriorHour: prior, Net6hAvg: sum6 / 6}
	// Ex-capex slope: the 1h-over-1h change in operating (ex-SHIP_INVESTMENTS)
	// rate — a plain-SQL stand-in for the smoothed dashboard series' slope.
	exLast, okA := b.sumAmount(ctx, "created_at >= ? AND category != ?", now.Add(-time.Hour), "SHIP_INVESTMENTS")
	exPrior, okB := b.sumAmount(ctx, "created_at >= ? AND created_at < ? AND category != ?",
		now.Add(-2*time.Hour), now.Add(-time.Hour), "SHIP_INVESTMENTS")
	if okA && okB {
		slope := exLast - exPrior
		tr.ExCapexSlopePerHour = &slope
	}
	return tr
}

func (b *Briefing) readProfitMix(ctx context.Context, now time.Time) *profitMixData {
	type row struct {
		OperationType string
		Net           int64
	}
	var rows []row
	if err := b.db.WithContext(ctx).Model(&persistence.TransactionModel{}).
		Select("operation_type, COALESCE(SUM(amount),0) AS net").
		Where("player_id = ? AND created_at >= ?", b.playerID, now.Add(-6*time.Hour)).
		Group("operation_type").Scan(&rows).Error; err != nil {
		return nil
	}
	// Bucket each engine's operation_type into the four mix labels; only
	// positive-net engines count toward "which engine carries the hour".
	buckets := map[string]int{}
	for _, r := range rows {
		label := mixLabel(r.OperationType)
		if label == "" || r.Net <= 0 {
			continue
		}
		buckets[label] += int(r.Net)
	}
	total := 0
	for _, v := range buckets {
		total += v
	}
	if total <= 0 {
		return nil
	}
	shares := make([]mixShare, 0, len(buckets))
	for label, v := range buckets {
		shares = append(shares, mixShare{Label: label, Pct: int(float64(v)/float64(total)*100 + 0.5)})
	}
	sort.Slice(shares, func(i, j int) bool {
		if shares[i].Pct != shares[j].Pct {
			return shares[i].Pct > shares[j].Pct
		}
		return shares[i].Label < shares[j].Label
	})
	return &profitMixData{Shares: shares}
}

// mixLabel maps a ledger operation_type to a profit-mix engine label, or "" to
// exclude it. The values mirror incomeEngines (detectors.go): tour, contract,
// trade_route, factory_workflow/manufacturing.
func mixLabel(op string) string {
	switch op {
	case "tour":
		return "tours"
	case "contract":
		return "contracts"
	case "factory_workflow", "manufacturing":
		return "factory"
	case "trade_route", "arbitrage":
		return "arb"
	default:
		return ""
	}
}

func (b *Briefing) readBurnWatch(ctx context.Context, now time.Time) *burnWatchData {
	type row struct {
		Category string
		Spend    int64
	}
	var rows []row
	if err := b.db.WithContext(ctx).Model(&persistence.TransactionModel{}).
		Select("category, COALESCE(-SUM(amount),0) AS spend").
		Where("player_id = ? AND amount < 0 AND created_at >= ?", b.playerID, now.Add(-time.Hour)).
		Group("category").Order("spend DESC").Scan(&rows).Error; err != nil || len(rows) == 0 {
		return nil
	}
	total := int64(0)
	for _, r := range rows {
		total += r.Spend
	}
	top := rows[0]
	if top.Spend <= 0 || total <= 0 {
		return nil
	}
	return &burnWatchData{
		CostCenter:  top.Category,
		RatePerHour: -int(top.Spend),
		SharePct:    int(float64(top.Spend)/float64(total)*100 + 0.5),
	}
}

func (b *Briefing) readEarners(ctx context.Context, now time.Time) *earnersData {
	var tours int64
	if err := b.db.WithContext(ctx).Model(&persistence.ContainerModel{}).
		Where("player_id = ? AND command_type = ? AND status = ?", b.playerID, "tour_run", "RUNNING").
		Count(&tours).Error; err != nil {
		return nil
	}
	e := &earnersData{ActiveTours: int(tours)}
	// heavy sells/hr: income (amount>0) ledger rows in the last hour — the
	// EarnerDark "are sells happening" read, answered preemptively.
	var sells int64
	if err := b.db.WithContext(ctx).Model(&persistence.TransactionModel{}).
		Where("player_id = ? AND amount > 0 AND created_at >= ?", b.playerID, now.Add(-time.Hour)).
		Count(&sells).Error; err == nil {
		s := int(sells)
		e.HeavySellsPerHour = &s
	}
	return e
}

func (b *Briefing) readPosture(ctx context.Context) *postureData {
	var laden int64
	if err := b.db.WithContext(ctx).Model(&persistence.ShipModel{}).
		Where("player_id = ? AND cargo_units > 0", b.playerID).Count(&laden).Error; err != nil {
		return nil
	}
	var idle int64
	if err := b.db.WithContext(ctx).Model(&persistence.ShipModel{}).
		Where("player_id = ? AND cargo_units = 0 AND nav_status IN ?", b.playerID, []string{"DOCKED", "IN_ORBIT"}).
		Count(&idle).Error; err != nil {
		return nil
	}
	// Stranded (a hull parked with a structured reason) has no grounded fleet-wide
	// source yet; left at zero rather than fabricated. renderPosture omits the
	// detail when empty. A follow-up can wire it from the stranded-hull signal.
	return &postureData{Laden: int(laden), Idle: int(idle)}
}

func (b *Briefing) readCoverage(ctx context.Context, now time.Time) *coverageData {
	// Per-waypoint newest scan, then count how many are stale (> cutoff). Plain
	// SQL (subquery + MAX + CASE), sqlite- and postgres-safe.
	var res struct {
		Total int
		Stale int
	}
	sub := b.db.WithContext(ctx).Model(&persistence.MarketData{}).
		Select("waypoint_symbol, MAX(last_updated) AS last_max").
		Where("player_id = ?", b.playerID).Group("waypoint_symbol")
	if err := b.db.WithContext(ctx).Table("(?) AS t", sub).
		Select("COUNT(*) AS total, COALESCE(SUM(CASE WHEN last_max < ? THEN 1 ELSE 0 END),0) AS stale",
			now.Add(-briefingStaleMarket)).
		Scan(&res).Error; err != nil || res.Total == 0 {
		return nil
	}
	return &coverageData{Stale: res.Stale, Total: res.Total}
}

func (b *Briefing) readInventory(ctx context.Context) *inventoryData {
	// Mark-to-bid total (financial.json "Fleet Inventory Value" shape): this uses
	// Postgres jsonb functions and so degrades to "n/a" on a non-Postgres store —
	// exactly the fail-open contract.
	total, ok := b.inventoryValue(ctx, "")
	if !ok {
		return nil
	}
	inv := &inventoryData{TotalValue: total}
	if stored, ok := b.inventoryValue(ctx,
		"AND EXISTS (SELECT 1 FROM storage_operations so, jsonb_array_elements_text(so.storage_ships::jsonb) ss(ship_symbol) "+
			"WHERE so.status='RUNNING' AND so.player_id=s.player_id AND ss.ship_symbol = s.ship_symbol)"); ok {
		inv.StoredValue = &stored
	}
	return inv
}

// inventoryValue runs the mark-to-bid jsonb rollup with an optional extra
// ship-scope predicate (e.g. stored-only). Postgres-only by construction.
func (b *Briefing) inventoryValue(ctx context.Context, shipScope string) (int, bool) {
	q := fmt.Sprintf(`WITH fleet_inv AS (
		SELECT g->>'symbol' AS good, (g->>'units')::int AS units
		FROM ships s, jsonb_array_elements(s.cargo_inventory::jsonb) g
		WHERE s.cargo_units > 0 AND s.player_id = ? %s),
	px AS (SELECT good_symbol, MAX(purchase_price) AS best_bid FROM market_data WHERE player_id = ? GROUP BY 1)
	SELECT COALESCE(SUM(f.units*COALESCE(p.best_bid,0)),0) AS value
	FROM fleet_inv f LEFT JOIN px p ON p.good_symbol = f.good`, shipScope)
	var value int64
	if err := b.db.WithContext(ctx).Raw(q, b.playerID, b.playerID).Scan(&value).Error; err != nil {
		return 0, false
	}
	return int(value), true
}

func (b *Briefing) readEngineering(ctx context.Context, now time.Time, since time.Duration) *engineeringData {
	if since <= 0 {
		return nil
	}
	var n int64
	if err := b.db.WithContext(ctx).Model(&persistence.CaptainEventModel{}).
		Where("player_id = ? AND type = ? AND created_at >= ?",
			b.playerID, string(captain.EventDeployCompleted), now.Add(-since)).
		Count(&n).Error; err != nil || n == 0 {
		return nil // omit-if-empty
	}
	return &engineeringData{DeploysAwaitingAcceptance: int(n)}
}

// --- Prometheus readers (empty promURL disables; any error -> nil -> "n/a") ---

func (b *Briefing) readGuards(ctx context.Context, since time.Duration) *guardsData {
	if b.promURL == "" {
		return nil
	}
	window := promRange(since)
	killSamples, ok := b.promQuery(ctx, fmt.Sprintf("sum by (good) (increase(%s[%s]))", metricChainKills, window))
	if !ok {
		return nil // prometheus unreachable -> whole line n/a
	}
	g := &guardsData{}
	for _, s := range killSamples {
		n := int(s.value + 0.5)
		if n <= 0 {
			continue
		}
		g.Kills += n
		if good := s.labels["good"]; good != "" {
			g.KilledChains = append(g.KilledChains, killedChain{Name: good})
		}
	}
	sort.Slice(g.KilledChains, func(i, j int) bool { return g.KilledChains[i].Name < g.KilledChains[j].Name })
	if pause, ok := b.promScalar(ctx, fmt.Sprintf("sum(increase(%s[%s]))", metricChainInputPause, window)); ok {
		p := int(pause + 0.5)
		g.InputPauses = &p
	}
	return g
}

func (b *Briefing) readAPI(ctx context.Context) *apiData {
	if b.promURL == "" {
		return nil
	}
	a := &apiData{}
	got := false
	if util, ok := b.promScalar(ctx, fmt.Sprintf("sum(rate(%s[5m])) / 2.0 * 100", metricAPIRequests)); ok {
		u := int(util + 0.5)
		a.UtilPct = &u
		got = true
	}
	if p95, ok := b.promScalar(ctx,
		fmt.Sprintf("histogram_quantile(0.95, sum(rate(%s[5m])) by (le))", metricAPIWaitBucket)); ok {
		a.P95WaitSeconds = &p95
		got = true
	}
	if !got {
		return nil
	}
	return a
}

func (b *Briefing) readAlerts(ctx context.Context) *alertsData {
	if b.promURL == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.promURL+"/api/v1/alerts", nil)
	if err != nil {
		return nil
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var parsed prometheusAlertsAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil
	}
	firing := []string{}
	for _, al := range parsed.Data.Alerts {
		if al.State == "firing" {
			if name := al.Labels["alertname"]; name != "" {
				firing = append(firing, name)
			}
		}
	}
	sort.Strings(firing)
	return &alertsData{Firing: firing}
}

// promSample is one instant-vector result: its label set and float value.
type promSample struct {
	labels map[string]string
	value  float64
}

// promQuery runs a Prometheus instant query and returns its result vector.
// ok=false on any transport/decode error (fail-open).
func (b *Briefing) promQuery(ctx context.Context, query string) ([]promSample, bool) {
	u := b.promURL + "/api/v1/query?query=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, false
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var parsed struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]interface{}    `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil || parsed.Status != "success" {
		return nil, false
	}
	out := make([]promSample, 0, len(parsed.Data.Result))
	for _, r := range parsed.Data.Result {
		raw, _ := r.Value[1].(string)
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			continue
		}
		out = append(out, promSample{labels: r.Metric, value: v})
	}
	return out, true
}

// promScalar runs an instant query expected to yield a single value.
func (b *Briefing) promScalar(ctx context.Context, query string) (float64, bool) {
	samples, ok := b.promQuery(ctx, query)
	if !ok || len(samples) == 0 {
		return 0, false
	}
	return samples[0].value, true
}

// promRange renders a duration as a Prometheus range selector like "120m",
// clamped to a sane floor so a zero/negative "since" still queries a window.
func promRange(since time.Duration) string {
	m := int(since.Minutes())
	if m < 1 {
		m = 60
	}
	return strconv.Itoa(m) + "m"
}

// newCachedEraResetReader returns a fail-open reader of the next universe-reset
// time, cached for briefingEraCacheTTL so a wake costs at most one status call.
// Any error keeps the last cached value (or nil) — never blocks.
func newCachedEraResetReader(status serverStatusSource) func(context.Context) *time.Time {
	const briefingEraCacheTTL = time.Hour
	var (
		mu      sync.Mutex
		cached  *time.Time
		fetched time.Time
	)
	return func(ctx context.Context) *time.Time {
		mu.Lock()
		defer mu.Unlock()
		if !fetched.IsZero() && time.Since(fetched) < briefingEraCacheTTL {
			return cached
		}
		st, err := status.GetServerStatus(ctx)
		if err != nil || st == nil {
			return cached // keep last known on a transient status error
		}
		fetched = time.Now()
		next, err := time.Parse(time.RFC3339, st.ServerResets.Next)
		if err != nil {
			cached = nil
			return nil
		}
		cached = &next
		return cached
	}
}
