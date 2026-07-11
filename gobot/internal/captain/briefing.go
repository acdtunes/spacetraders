package watchkeeper

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// briefing.go composes the wake-briefing block (sp-g2w6, Admiral ask): a
// compact fleet+financial snapshot the watchkeeper prepends to every captain
// wake, replacing ~4 psql queries per wake. The captain-ruled final spec
// (14dl1) is 14 items merged to 13 lines in a STRICT money-first ordering:
// TREASURY, TREND, RUNWAY, GUARDS, POSTURE, EARNERS, PROFIT MIX, BURN WATCH,
// API, ALERTS, COVERAGE, INVENTORY, ENGINEERING(omit-if-empty).
//
// DOCTRINE — FAIL OPEN. A wake briefing is read-only observability. Every line
// degrades to "n/a" on read error and the block NEVER blocks or fails a wake:
// renderBriefing performs no IO and cannot error; the composer that feeds it
// fetches each element best-effort and leaves the corresponding field nil on
// any failure, which renders as "n/a". A nil field is the ONLY failure path,
// and it is total: no panic, no partial-line abort.

// briefingRenderCap bounds the rendered block (~900 chars per the captain cut
// once the financial-analysis section landed). The 13 compact lines sit well
// under it in practice; the cap is a backstop against a pathological read
// (e.g. a hundred stranded hulls) blowing up the wake mail.
const briefingRenderCap = 900

// briefingEraPromotionWindow is the T-6h era-end threshold (operator
// requirement b, 14dl1): once the era reset is within this window, INVENTORY
// promotes to position 2 with a dump-deadline countdown appended, so the
// end-of-era rundown is the first thing after treasury.
const briefingEraPromotionWindow = 6 * time.Hour

// briefingGuardNameThreshold is the operator requirement (a) cutoff: when the
// chain-kill count is at or below this, the killed chain(s) are named inline
// (a bare number sends the captain to the logs anyway); above it, only the
// count renders.
const briefingGuardNameThreshold = 2

// briefingData is the fully-resolved snapshot renderBriefing turns into text.
// Every element is a pointer/slice whose nil/empty state means "this read did
// not produce a value" and renders "n/a" — the fail-open contract. renderBriefing
// does NO IO; the composer builds this struct best-effort.
type briefingData struct {
	Treasury    *treasuryData
	Trend       *trendData
	Floors      []int // credit floor thresholds for RUNWAY (captain-configured)
	Guards      *guardsData
	Posture     *postureData
	Earners     *earnersData
	ProfitMix   *profitMixData
	BurnWatch   *burnWatchData
	API         *apiData
	Alerts      *alertsData
	Coverage    *coverageData
	Inventory   *inventoryData
	Engineering *engineeringData
	// EraReset, when set, is the wall-clock time of the next universe reset. It
	// drives the T-6h INVENTORY promotion (operator req b); nil (unreadable era
	// status) fails open to no promotion.
	EraReset *time.Time
}

type treasuryData struct {
	Balance        int  // credits now
	DeltaSinceWake *int // net change since the previous wake; nil omits the annotation
}

type trendData struct {
	NetLastHour         int  // net cr in the last hour
	NetPriorHour        int  // net cr in the hour before that
	Net6hAvg            int  // average hourly net over the last 6h
	ExCapexSlopePerHour *int // slope of the smoothed ex-capex operating series (cr/hr); nil omits
}

type killedChain struct {
	Name        string // the good/chain the kill-guard parked (chain_pnl_kills_total{good})
	RatePerHour *int   // realized cr/hr at kill, when available; nil omits the rate
}

type guardsData struct {
	CeilingParks *int          // API approach-ceiling parks since last wake
	SupplyParks  *int          // supply-gate parks since last wake
	InputPauses  *int          // chain_input_pause_total delta
	Kills        int           // chain_pnl_kills_total delta
	KilledChains []killedChain // named inline when Kills <= briefingGuardNameThreshold
}

type strandedHull struct {
	Waypoint string
	Reason   string
}

type postureData struct {
	Laden          int
	Idle           int
	Stranded       int
	StrandedDetail []strandedHull // named inline (waypoint + structured reason)
}

type earnersData struct {
	ActiveTours       int
	HeavySellsPerHour *int // heavy sells/hr last hour (the EarnerDark question, answered)
}

type mixShare struct {
	Label string
	Pct   int
}

type profitMixData struct {
	Shares []mixShare // tours / contracts / factory / arb shares of the last 6h
}

type burnWatchData struct {
	CostCenter  string // largest cost center last hour
	RatePerHour int    // its cr/hr (negative = spend)
	SharePct    int    // its share of total costs
}

type apiData struct {
	UtilPct        *int     // utilization % (5m)
	P95WaitSeconds *float64 // p95 limiter wait
}

type alertsData struct {
	Firing []string // firing prometheus alert names; empty renders "none"
}

type coverageData struct {
	Stale int // markets stale > 3h
	Total int // total markets
}

type inventoryData struct {
	TotalValue  int  // mark-to-bid total
	StoredValue *int // warehouse-stored split; nil omits
}

type engineeringData struct {
	DeploysAwaitingAcceptance int // deploys since last wake awaiting acceptance
}

// briefingLine pairs a label with its rendered text so the ordering (and the
// era-end INVENTORY promotion) operate on structured lines rather than raw
// strings.
type briefingLine struct {
	label string
	text  string
}

// renderBriefing turns a resolved briefingData into the wake-briefing block.
// Pure and total: no IO, never errors, never panics on nil fields — the
// fail-open heart of sp-g2w6.
func renderBriefing(d briefingData, now time.Time) string {
	lines := []briefingLine{
		{"TREASURY", renderTreasury(d.Treasury)},
		{"TREND", renderTrend(d.Trend)},
		{"RUNWAY", renderRunway(d.Treasury, d.Trend, d.Floors)},
		{"GUARDS", renderGuards(d.Guards)},
		{"POSTURE", renderPosture(d.Posture)},
		{"EARNERS", renderEarners(d.Earners)},
		{"MIX", renderProfitMix(d.ProfitMix)},
		{"BURN", renderBurnWatch(d.BurnWatch)},
		{"API", renderAPI(d.API)},
		{"ALERTS", renderAlerts(d.Alerts)},
		{"COVERAGE", renderCoverage(d.Coverage)},
		{"INVENTORY", renderInventory(d.Inventory, d.EraReset, now)},
	}
	// ENGINEERING is omit-if-empty (9, 14dl1): no line at all when there are no
	// deploys awaiting acceptance.
	if d.Engineering != nil && d.Engineering.DeploysAwaitingAcceptance > 0 {
		lines = append(lines, briefingLine{"ENGINEERING", renderEngineering(d.Engineering)})
	}

	lines = promoteInventoryAtEraEnd(lines, d.EraReset, now)

	var b strings.Builder
	b.WriteString("WAKE BRIEFING\n")
	for _, ln := range lines {
		fmt.Fprintf(&b, "%s: %s\n", ln.label, ln.text)
	}
	return capBriefing(b.String())
}

// promoteInventoryAtEraEnd moves INVENTORY to position 2 (right after
// TREASURY) once the era reset is within briefingEraPromotionWindow — operator
// requirement (b): the end-of-era rundown becomes the first thing after
// treasury so the captain dumps before the wipe. Outside the window (or with an
// unreadable reset) ordering is untouched — fail-open to no promotion.
func promoteInventoryAtEraEnd(lines []briefingLine, eraReset *time.Time, now time.Time) []briefingLine {
	if _, ok := eraPromotionActive(eraReset, now); !ok {
		return lines
	}
	const promotedIndex = 1 // position 2: right after TREASURY (index 0)
	src := -1
	for i, ln := range lines {
		if ln.label == "INVENTORY" {
			src = i
			break
		}
	}
	if src <= promotedIndex {
		return lines
	}
	inv := lines[src]
	lines = append(lines[:src], lines[src+1:]...)
	out := make([]briefingLine, 0, len(lines)+1)
	out = append(out, lines[:promotedIndex]...)
	out = append(out, inv)
	out = append(out, lines[promotedIndex:]...)
	return out
}

// eraPromotionActive reports whether the era reset is close enough (within
// briefingEraPromotionWindow, and not already past) to trigger the INVENTORY
// promotion and dump-deadline countdown. nil reset (unreadable era status)
// fails open to inactive.
func eraPromotionActive(eraReset *time.Time, now time.Time) (time.Duration, bool) {
	if eraReset == nil {
		return 0, false
	}
	remaining := eraReset.Sub(now)
	if remaining <= 0 || remaining > briefingEraPromotionWindow {
		return 0, false
	}
	return remaining, true
}

// fmtCountdown renders a dump-deadline as "T-3.0h" (>=1h) or "T-45m" (<1h).
func fmtCountdown(d time.Duration) string {
	if d >= time.Hour {
		return fmt.Sprintf("T-%.1fh", d.Hours())
	}
	return fmt.Sprintf("T-%dm", int(d.Minutes()))
}

// capBriefing enforces briefingRenderCap by dropping whole trailing lines
// (never a partial line) and marking the truncation. A backstop only.
func capBriefing(s string) string {
	if len(s) <= briefingRenderCap {
		return s
	}
	trimmed := s[:briefingRenderCap]
	if i := strings.LastIndexByte(trimmed, '\n'); i >= 0 {
		trimmed = trimmed[:i+1]
	}
	return trimmed + "…(capped)\n"
}

func renderTreasury(t *treasuryData) string {
	if t == nil {
		return "n/a"
	}
	s := fmtCr(t.Balance)
	if t.DeltaSinceWake != nil {
		s += fmt.Sprintf(" (Δ%s since wake)", fmtSignedCr(*t.DeltaSinceWake))
	}
	return s
}

func renderTrend(t *trendData) string {
	if t == nil {
		return "n/a"
	}
	s := fmt.Sprintf("net/hr: 1h %s | prior %s | 6h avg %s (%s)",
		fmtSignedCr(t.NetLastHour), fmtSignedCr(t.NetPriorHour), fmtSignedCr(t.Net6hAvg),
		trendDirection(t.NetLastHour, t.NetPriorHour))
	if t.ExCapexSlopePerHour != nil {
		s += fmt.Sprintf(" | ex-capex slope %s", fmtSignedCr(*t.ExCapexSlopePerHour))
	}
	return s
}

// trendDirection reads the compounding-knee: a rising net rate is
// accelerating, a falling one decelerating (the read the captain does by hand).
func trendDirection(last, prior int) string {
	switch {
	case last > prior:
		return "accel"
	case last < prior:
		return "decel"
	default:
		return "flat"
	}
}

func renderRunway(t *treasuryData, tr *trendData, floors []int) string {
	if t == nil || tr == nil {
		return "n/a"
	}
	if tr.NetLastHour >= 0 {
		return "compounding"
	}
	floor, ok := nextFloorBelow(t.Balance, floors)
	if !ok {
		return "n/a"
	}
	hours := float64(t.Balance-floor) / float64(-tr.NetLastHour)
	return fmt.Sprintf("%.1fh to %s floor", hours, fmtCr(floor))
}

// nextFloorBelow returns the greatest configured floor strictly below balance.
func nextFloorBelow(balance int, floors []int) (int, bool) {
	best, ok := 0, false
	for _, f := range floors {
		if f < balance && (!ok || f > best) {
			best, ok = f, true
		}
	}
	return best, ok
}

func renderGuards(g *guardsData) string {
	if g == nil {
		return "n/a"
	}
	parts := []string{
		"ceiling " + fmtIntPtr(g.CeilingParks),
		"supply " + fmtIntPtr(g.SupplyParks),
		"input-pause " + fmtIntPtr(g.InputPauses),
		renderKills(g),
	}
	return strings.Join(parts, " | ")
}

// renderKills applies operator requirement (a): name the killed chain(s)
// inline when the count is at or below the threshold.
func renderKills(g *guardsData) string {
	s := fmt.Sprintf("kills %d", g.Kills)
	if g.Kills > 0 && g.Kills <= briefingGuardNameThreshold && len(g.KilledChains) > 0 {
		names := make([]string, 0, len(g.KilledChains))
		for _, c := range g.KilledChains {
			n := c.Name
			if c.RatePerHour != nil {
				n += fmt.Sprintf(" %s/hr", fmtSignedCr(*c.RatePerHour))
			}
			names = append(names, n)
		}
		s += " (" + strings.Join(names, ", ") + ")"
	}
	return s
}

func renderPosture(p *postureData) string {
	if p == nil {
		return "n/a"
	}
	s := fmt.Sprintf("%d laden | %d idle | %d stranded", p.Laden, p.Idle, p.Stranded)
	if len(p.StrandedDetail) > 0 {
		names := make([]string, 0, len(p.StrandedDetail))
		for _, h := range p.StrandedDetail {
			names = append(names, fmt.Sprintf("%s %s", h.Waypoint, h.Reason))
		}
		s += " (" + strings.Join(names, ", ") + ")"
	}
	return s
}

func renderEarners(e *earnersData) string {
	if e == nil {
		return "n/a"
	}
	s := fmt.Sprintf("%d tours", e.ActiveTours)
	if e.HeavySellsPerHour != nil {
		s += fmt.Sprintf(" | heavy sells/hr %d", *e.HeavySellsPerHour)
	}
	return s
}

func renderProfitMix(m *profitMixData) string {
	if m == nil || len(m.Shares) == 0 {
		return "n/a"
	}
	parts := make([]string, 0, len(m.Shares))
	for _, sh := range m.Shares {
		parts = append(parts, fmt.Sprintf("%s %d%%", sh.Label, sh.Pct))
	}
	return "6h: " + strings.Join(parts, " | ")
}

func renderBurnWatch(bw *burnWatchData) string {
	if bw == nil {
		return "n/a"
	}
	return fmt.Sprintf("%s %s/hr (%d%%)", bw.CostCenter, fmtSignedCr(bw.RatePerHour), bw.SharePct)
}

func renderAPI(a *apiData) string {
	if a == nil {
		return "n/a"
	}
	util := "?"
	if a.UtilPct != nil {
		util = strconv.Itoa(*a.UtilPct) + "%"
	}
	wait := "?"
	if a.P95WaitSeconds != nil {
		wait = strconv.FormatFloat(*a.P95WaitSeconds, 'f', 1, 64) + "s"
	}
	return fmt.Sprintf("util %s (5m) | p95 wait %s", util, wait)
}

func renderAlerts(a *alertsData) string {
	if a == nil {
		return "n/a"
	}
	if len(a.Firing) == 0 {
		return "none"
	}
	return strings.Join(a.Firing, ", ")
}

func renderCoverage(c *coverageData) string {
	if c == nil {
		return "n/a"
	}
	return fmt.Sprintf("%d/%d markets stale (>3h)", c.Stale, c.Total)
}

func renderInventory(inv *inventoryData, eraReset *time.Time, now time.Time) string {
	if inv == nil {
		return "n/a"
	}
	s := fmt.Sprintf("%s total", fmtCr(inv.TotalValue))
	if inv.StoredValue != nil {
		s += fmt.Sprintf(" (stored %s)", fmtCr(*inv.StoredValue))
	}
	// Operator requirement (b): append the dump-deadline countdown once the era
	// reset is inside the T-6h window (the same condition that promotes this
	// line to position 2).
	if remaining, ok := eraPromotionActive(eraReset, now); ok {
		s += fmt.Sprintf(" | dump %s", fmtCountdown(remaining))
	}
	return s
}

func renderEngineering(e *engineeringData) string {
	return fmt.Sprintf("%d deploys awaiting acceptance", e.DeploysAwaitingAcceptance)
}

// fmtIntPtr renders an *int as its value or "?" (a sub-field that failed while
// the rest of its line succeeded — distinct from a whole-line "n/a").
func fmtIntPtr(p *int) string {
	if p == nil {
		return "?"
	}
	return strconv.Itoa(*p)
}

// fmtCr renders credits compactly: 1_230_000 -> "1.23M", 45_000 -> "45k",
// -2_200_000 -> "-2.2M", small values as-is.
func fmtCr(n int) string {
	if n < 0 {
		return "-" + compactMag(float64(-n))
	}
	return compactMag(float64(n))
}

// fmtSignedCr is fmtCr with an explicit leading sign, for deltas and rates.
func fmtSignedCr(n int) string {
	if n < 0 {
		return "-" + compactMag(float64(-n))
	}
	return "+" + compactMag(float64(n))
}

func compactMag(a float64) string {
	switch {
	case a >= 1e6:
		s := strconv.FormatFloat(a/1e6, 'f', 2, 64)
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
		return s + "M"
	case a >= 1e3:
		return strconv.FormatFloat(a/1e3, 'f', 0, 64) + "k"
	default:
		return strconv.FormatFloat(a, 'f', 0, 64)
	}
}
