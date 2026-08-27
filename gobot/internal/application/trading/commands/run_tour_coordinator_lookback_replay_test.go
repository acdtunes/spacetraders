package commands

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// Replaying the look-back manifest builder over the reposition seams the fleet actually hit.
//
// The manifest is a pure function of two systems' listings, a hold and a floor, so it can be
// re-decided on recorded inputs: every look-back load in the telemetry window names a
// departure system, a destination system and the waypoint the hull stood on, and
// market_price_history holds the quotes readable at that instant. Rebuilding the snapshot and
// re-running the builder at several prices is what turns "fewer stops" from an arithmetic
// claim into a measured trade of credits against requests.
//
// It reads Postgres through psql, as the routing service's own replays do, and is SKIPPED
// unless the window is named — the merge gate has no database and must not couple to one, and
// a skip here is honestly "not checked", never "checked and fine".
//
//	ST_LOOKBACK_REPLAY_HOURS=24 go test ./internal/application/trading/commands/ \
//	    -run TestReplay_LookbackSourceCharge -v
const (
	replayHoursEnvVar = "ST_LOOKBACK_REPLAY_HOURS"
	// The hull every tour ship flies, and the coordinator's own listing freshness window.
	replayHoldCapacity  = 490
	replayFreshnessMins = 75
	// The solver's own calibration of a stop and a buy chunk, so this measurement and the
	// plan's call model cannot disagree about what a stop is worth. A hull standing on a
	// source still pays its dock; what it saves is the navigate and orbit.
	replayCallsPerVisit         = 3.3
	replayCallsPerTransaction   = 1.0
	replayCallsPerStandingVisit = 1.0
	replayFieldSep              = "\x1f"
)

// replayQuote is one market's quote for one good, as the coordinator would have read it.
type replayQuote struct {
	at     int64
	ask    int
	bid    int
	volume int
}

// replaySeam is one reposition the fleet committed: a look-back load bought in fromSystem
// while the hull stood on standWaypoint, carried to toSystem.
type replaySeam struct {
	at            int64
	fromSystem    string
	toSystem      string
	standWaypoint string
}

// replayArm is what one sourcing rule spent and earned on one manifest. sources counts the
// DISTINCT markets it shops; docks counts what flying it actually costs, which is larger
// whenever the order returns to a source it has already left.
type replayArm struct {
	sources int
	docks   int
	items   int
	units   int
	gross   int
	calls   float64
}

func (a replayArm) grossPerCall() float64 {
	if a.calls <= 0 {
		return 0
	}
	return float64(a.gross) / a.calls
}

func TestReplay_LookbackSourceCharge(t *testing.T) {
	hours := os.Getenv(replayHoursEnvVar)
	if hours == "" {
		t.Skipf("set %s to replay look-back sourcing against the live telemetry window", replayHoursEnvVar)
	}
	if _, err := strconv.Atoi(hours); err != nil {
		t.Fatalf("%s must be a whole number of hours, got %q", replayHoursEnvVar, hours)
	}

	quotes := replayLoadQuotes(t, hours)
	tradeTypes := replayLoadTradeTypes(t)
	seams := replayLoadSeams(t, hours)
	t.Logf("replay window %sh: %d seams, %d quoted (waypoint,good) series", hours, len(seams), len(quotes))
	if len(seams) == 0 {
		t.Skip("no look-back reposition seams in the window")
	}

	// Arm 0 is today's engine. Arm 1 changes only the flown ORDER — the same goods at the same
	// prices, grouped so a source is docked once — and isolates what that costs nothing to get.
	// The rest price a further source at rising charges.
	arms := []struct {
		name    string
		grouped bool
		charge  int
	}{
		{name: "today", grouped: false},
		{name: "grouped", grouped: true},
		{name: "charge 5k", grouped: true, charge: 5_000},
		{name: "charge 10k", grouped: true, charge: 10_000},
		{name: "charge 15k", grouped: true, charge: 15_000},
		{name: "charge 20k", grouped: true, charge: 20_000},
		{name: "charge 30k", grouped: true, charge: 30_000},
		{name: "charge 40k", grouped: true, charge: 40_000},
		{name: "charge 60k", grouped: true, charge: 60_000},
		{name: "charge 100k", grouped: true, charge: 100_000},
	}
	type row struct {
		name    string
		cases   int
		total   replayArm
		wins    int
		losses  int
		ties    int
		lossPct []float64
	}
	rows := make([]row, len(arms))
	for i, arm := range arms {
		rows[i].name = arm.name
	}

	for _, seam := range seams {
		src, dst := replaySnapshot(quotes, tradeTypes, seam)
		if len(src) == 0 || len(dst) == 0 {
			continue
		}
		var base replayArm
		for i, arm := range arms {
			sourcing := lookbackSourcing{}
			free := ""
			if arm.grouped {
				sourcing = lookbackSourcing{StandWaypoint: seam.standWaypoint, VisitCharge: arm.charge}
				free = seam.standWaypoint
			}
			scored := replayScore(src, dst, sourcing, free, seam.standWaypoint, !arm.grouped)
			if i == 0 {
				base = scored
				if base.items == 0 {
					break
				}
			}
			rows[i].cases++
			rows[i].total = replayAdd(rows[i].total, scored)
			switch b, o := base.grossPerCall(), scored.grossPerCall(); {
			case o > b*1.0001:
				rows[i].wins++
			case o < b*0.9999:
				rows[i].losses++
				if b > 0 {
					rows[i].lossPct = append(rows[i].lossPct, (o-b)/b*100)
				}
			default:
				rows[i].ties++
			}
		}
	}
	for i := range rows {
		sort.Float64s(rows[i].lossPct)
	}

	t.Logf("%12s %7s %8s %7s %8s %8s %10s %11s %11s %6s %6s %6s",
		"arm", "cases", "src/man", "docks", "calls", "units", "gross", "gross/call",
		"gross/unit", "win", "loss", "tie")
	for _, r := range rows {
		if r.cases == 0 {
			continue
		}
		n := float64(r.cases)
		t.Logf("%12s %7d %8.2f %7.2f %8.2f %8.1f %10.0f %11.0f %11.1f %6d %6d %6d",
			r.name, r.cases,
			float64(r.total.sources)/n, float64(r.total.docks)/n, r.total.calls/n,
			float64(r.total.units)/n, float64(r.total.gross)/n, r.total.grossPerCall(),
			float64(r.total.gross)/math.Max(1, float64(r.total.units)),
			r.wins, r.losses, r.ties)
	}
	base := rows[0].total
	for _, r := range rows[1:] {
		if r.cases == 0 || base.calls == 0 {
			continue
		}
		t.Logf("%12s: gross/call %+7.2f%%  gross %+7.2f%%  units %+7.2f%%  calls %+7.2f%%  "+
			"margin/unit %+6.2f%%  W%d/L%d/T%d  loss p50 %.1f%% p90 %.1f%% worst %.1f%%",
			r.name,
			pctDelta(r.total.grossPerCall(), base.grossPerCall()),
			pctDelta(float64(r.total.gross), float64(base.gross)),
			pctDelta(float64(r.total.units), float64(base.units)),
			pctDelta(r.total.calls, base.calls),
			pctDelta(float64(r.total.gross)/math.Max(1, float64(r.total.units)),
				float64(base.gross)/math.Max(1, float64(base.units))),
			r.wins, r.losses, r.ties,
			replayPercentile(r.lossPct, 0.50), replayPercentile(r.lossPct, 0.10),
			replayPercentile(r.lossPct, 0))
	}
}

func pctDelta(now, was float64) float64 {
	if was == 0 {
		return 0
	}
	return (now - was) / was * 100
}

// replayPercentile reads an ascending slice of (negative) loss percentages; q is measured from
// the shallow end, so q=0.10 is the deep tail.
func replayPercentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(q * float64(len(sorted)-1))
	return sorted[idx]
}

func replayAdd(acc, one replayArm) replayArm {
	return replayArm{
		sources: acc.sources + one.sources,
		docks:   acc.docks + one.docks,
		items:   acc.items + one.items,
		units:   acc.units + one.units,
		gross:   acc.gross + one.gross,
		calls:   acc.calls + one.calls,
	}
}

// replayScore builds one manifest and prices what flying it costs, walking it in the order it
// is returned — which is how the executor flies it, and therefore how a manifest that
// interleaves two sources pays for the same waypoint twice.
func replayScore(src, dst []trading.GoodListing, sourcing lookbackSourcing, freeWaypoint, standWaypoint string, ungrouped bool) replayArm {
	manifest := buildLookbackManifest(src, dst, replayHoldCapacity, lookbackMinMarginDefault, sourcing)
	if ungrouped {
		manifest = replayUngrouped(manifest, src, dst)
	}
	arm := replayArm{items: len(manifest)}
	distinct := map[string]bool{}
	prev, first := "", true
	for _, item := range manifest {
		arm.units += item.Units
		arm.gross += item.Units * (item.DestBid - item.SourceAsk)
		arm.calls += replayCallsPerTransaction
		if item.SourceWaypoint == prev {
			continue
		}
		distinct[item.SourceWaypoint] = true
		arm.docks++
		// The standing waypoint is free only for as long as the hull has not left it: the
		// first dock there costs no navigate, a return to it costs the full bundle.
		if first && item.SourceWaypoint == freeWaypoint {
			arm.calls += replayCallsPerStandingVisit
		} else {
			arm.calls += replayCallsPerVisit
		}
		prev, first = item.SourceWaypoint, false
	}
	arm.sources = len(distinct)
	return arm
}

// replayUngrouped restores the capped-spread sequence a manifest carries before it is grouped
// by waypoint, so the baseline arm pays for the re-docks that sequence causes. The key is the
// ranker's own: capped spread, then per-unit spread, then good.
func replayUngrouped(manifest []lookbackItem, src, dst []trading.GoodListing) []lookbackItem {
	volume := map[string]int{}
	for _, l := range src {
		volume[l.Waypoint+"|"+l.Good] = l.Volume
	}
	sink := map[string]int{}
	for _, l := range dst {
		if l.Volume > sink[l.Good] {
			sink[l.Good] = l.Volume
		}
	}
	capped := func(item lookbackItem) int {
		cap := volume[item.SourceWaypoint+"|"+item.Good]
		if s := sink[item.Good]; s < cap {
			cap = s
		}
		return (item.DestBid - item.SourceAsk) * cap
	}
	out := append([]lookbackItem(nil), manifest...)
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := capped(out[i]), capped(out[j]); a != b {
			return a > b
		}
		a := out[i].DestBid - out[i].SourceAsk
		b := out[j].DestBid - out[j].SourceAsk
		if a != b {
			return a > b
		}
		return out[i].Good < out[j].Good
	})
	return out
}

// replaySnapshot reconstructs the two systems' listings as of the seam, keeping the freshest
// quote at or before that instant inside the coordinator's own freshness window.
func replaySnapshot(quotes map[string][]replayQuote, tradeTypes map[string]string, seam replaySeam) (src, dst []trading.GoodListing) {
	horizon := seam.at - int64(replayFreshnessMins*60)
	for key, series := range quotes {
		waypoint, good, ok := strings.Cut(key, "|")
		if !ok {
			continue
		}
		system := replaySystemOf(waypoint)
		if system != seam.fromSystem && system != seam.toSystem {
			continue
		}
		idx := sort.Search(len(series), func(i int) bool { return series[i].at > seam.at }) - 1
		if idx < 0 || series[idx].at <= horizon {
			continue
		}
		q := series[idx]
		listing := trading.GoodListing{
			Good: good, Waypoint: waypoint, TradeType: tradeTypes[key],
			Bid: q.bid, Ask: q.ask, Volume: q.volume,
		}
		if system == seam.fromSystem {
			src = append(src, listing)
		} else {
			dst = append(dst, listing)
		}
	}
	return src, dst
}

func replaySystemOf(waypoint string) string {
	parts := strings.Split(waypoint, "-")
	if len(parts) < 2 {
		return waypoint
	}
	return parts[0] + "-" + parts[1]
}

// replayLoadSeams groups the telemetry window into look-back manifests: a run of one hull's
// look-back rows within a single system, since a load is bought before the hull jumps. The row
// before it names where the hull stood; the first row afterwards in another system names where
// the load went.
func replayLoadSeams(t *testing.T, hours string) []replaySeam {
	t.Helper()
	rows := replayPsql(t, fmt.Sprintf(`SELECT ship_symbol, waypoint, coalesce(engine,''),
	        extract(epoch FROM planned_at)::bigint
	    FROM tour_leg_telemetry
	    WHERE planned_at > now() - interval '%s hours'
	    ORDER BY ship_symbol, planned_at`, hours))

	type telRow struct {
		ship, waypoint, engine string
		at                     int64
	}
	tel := make([]telRow, 0, len(rows))
	for _, r := range rows {
		if len(r) < 4 {
			continue
		}
		at, err := strconv.ParseInt(r[3], 10, 64)
		if err != nil {
			continue
		}
		tel = append(tel, telRow{ship: r[0], waypoint: r[1], engine: r[2], at: at})
	}

	var seams []replaySeam
	for i := 0; i < len(tel); i++ {
		if tel[i].engine != string(trading.LegEngineLookback) {
			continue
		}
		if i == 0 || tel[i-1].ship != tel[i].ship {
			continue // no standing waypoint is readable for this hull's first row
		}
		from := replaySystemOf(tel[i].waypoint)
		end := i
		for end+1 < len(tel) && tel[end+1].ship == tel[i].ship &&
			tel[end+1].engine == string(trading.LegEngineLookback) &&
			replaySystemOf(tel[end+1].waypoint) == from {
			end++
		}
		to := ""
		for j := end + 1; j < len(tel) && tel[j].ship == tel[i].ship; j++ {
			if s := replaySystemOf(tel[j].waypoint); s != from {
				to = s
				break
			}
		}
		if to != "" {
			seams = append(seams, replaySeam{
				at: tel[i].at, fromSystem: from, toSystem: to,
				standWaypoint: tel[i-1].waypoint,
			})
		}
		i = end
	}
	return seams
}

// replayLoadQuotes reads every recorded quote in the window plus one freshness window of
// lead-in, so the earliest seam still resolves against quotes the coordinator could have seen.
func replayLoadQuotes(t *testing.T, hours string) map[string][]replayQuote {
	t.Helper()
	rows := replayPsql(t, fmt.Sprintf(`SELECT waypoint_symbol, good_symbol, purchase_price,
	        sell_price, trade_volume, extract(epoch FROM recorded_at)::bigint
	    FROM market_price_history
	    WHERE recorded_at > now() - interval '%s hours' - interval '%d minutes'
	    ORDER BY waypoint_symbol, good_symbol, recorded_at`, hours, replayFreshnessMins))

	quotes := map[string][]replayQuote{}
	for _, r := range rows {
		if len(r) < 6 {
			continue
		}
		// purchase_price is the ASK the hull pays, sell_price the BID it receives.
		ask, err1 := strconv.Atoi(r[2])
		bid, err2 := strconv.Atoi(r[3])
		volume, err3 := strconv.Atoi(r[4])
		at, err4 := strconv.ParseInt(r[5], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			continue
		}
		key := r[0] + "|" + r[1]
		quotes[key] = append(quotes[key], replayQuote{at: at, ask: ask, bid: bid, volume: volume})
	}
	return quotes
}

// replayLoadTradeTypes reads the sink discipline's input. A market's role changes far more
// slowly than its price, so the current role is the honest reading for a recent window.
func replayLoadTradeTypes(t *testing.T) map[string]string {
	t.Helper()
	rows := replayPsql(t, `SELECT waypoint_symbol, good_symbol, coalesce(trade_type,'')
	    FROM market_data`)
	types := map[string]string{}
	for _, r := range rows {
		if len(r) < 3 {
			continue
		}
		types[r[0]+"|"+r[1]] = r[2]
	}
	return types
}

func replayPsql(t *testing.T, sql string) [][]string {
	t.Helper()
	cmd := exec.Command("psql",
		"-h", replayEnv("ST_DATABASE_HOST", "localhost"),
		"-p", replayEnv("ST_DATABASE_PORT", "5432"),
		"-U", replayEnv("ST_DATABASE_USER", "spacetraders"),
		"-d", replayEnv("ST_DATABASE_NAME", "spacetraders"),
		"-t", "-A", "-F", replayFieldSep, "-c", sql)
	cmd.Env = append(os.Environ(), "PGPASSWORD="+replayEnv("ST_DATABASE_PASSWORD", "dev_password"))
	start := time.Now()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("psql read failed after %s: %v", time.Since(start).Round(time.Millisecond), err)
	}
	lines := strings.Split(string(out), "\n")
	rows := make([][]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		rows = append(rows, strings.Split(line, replayFieldSep))
	}
	return rows
}

func replayEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
