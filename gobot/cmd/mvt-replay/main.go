// mvt-replay runs the MVT ranker and departure rule over the last N hours of recorded tour
// legs and prints the spec §7 gate: jumps down AND margin per hull not down.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt/replay"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func main() {
	hours := flag.Int("hours", 24, "window of legs to replay")
	horizon := flag.Duration("horizon", time.Hour, "look-ahead used to value a decision")
	gap := flag.Duration("boundary-gap", 10*time.Minute, "idle gap that ends a visit")
	windowSells := flag.Int("yield-window-sells", 8, "EWMA window")
	minSells := flag.Int("yield-min-sells", 3, "cold-start guard")
	reach := flag.Int("claim-reach-hops", 2, "candidate radius")
	maxReach := flag.Int("claim-reach-max-hops", 4, "radius an empty ranking widens to (2 = escalation off at -claim-reach-hops 2)")
	minSpread := flag.Float64("ranker-min-spread", 200, "credits/unit a sell must clear to count as a candidate's depth (0 = off)")
	toll := flag.Int("toll-seconds", 361, "seconds per gate hop (jump cooldown)")
	fee := flag.Int64("gate-fee", 0, "credits per hop from the departure system")
	spanFloor := flag.Duration("rate-span-floor", 0, "visit span below which travel is priced on the FLEET rate, not the hull's (0 = off)")
	asJSON := flag.Bool("json", false, "print the full report as JSON")
	flag.Parse()

	cfg := config.MustLoadConfig("")
	db, err := database.NewConnection(&cfg.Database)
	if err != nil {
		fmt.Fprintln(os.Stderr, "db:", err)
		os.Exit(2)
	}
	ctx := context.Background()
	since := time.Now().Add(-time.Duration(*hours) * time.Hour)
	legs, err := persistence.NewTourTelemetryRepository(db).ListByPlayer(ctx, cfg.Captain.PlayerID, since)
	if err != nil {
		fmt.Fprintln(os.Stderr, "legs:", err)
		os.Exit(2)
	}
	var era persistence.EraModel
	q := db.WithContext(ctx).Model(&persistence.GateEdgeModel{}).Where("under_construction = ?", false)
	if err := db.WithContext(ctx).Where("closed_at IS NULL").Order("era_id DESC").First(&era).Error; err == nil {
		q = q.Where("era_id = ?", era.EraID)
	}
	var edges []persistence.GateEdgeModel
	if err := q.Find(&edges).Error; err != nil {
		fmt.Fprintln(os.Stderr, "gate edges:", err)
		os.Exit(2)
	}
	neighbours := map[string][]string{}
	for _, e := range edges {
		neighbours[e.SystemSymbol] = append(neighbours[e.SystemSymbol], e.ConnectedSystem)
	}
	rep := replay.Run(legs, neighbours, replay.Config{
		Window: time.Duration(*hours) * time.Hour, Horizon: *horizon, BoundaryGap: *gap,
		YieldWindowSells: *windowSells, YieldMinSells: *minSells, ClaimReachHops: *reach, ClaimReachMaxHops: *maxReach,
		TollSecondsPerHop: *toll, GateFee: *fee, RateSpanFloor: *spanFloor, RankerMinSpread: *minSpread,
	})
	if *asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(rep)
		return
	}
	pass, why := rep.Gate()
	fmt.Printf("legs=%d hulls=%d boundaries=%d\n", len(legs), rep.Hulls, rep.Boundaries)
	fmt.Printf("jumps: actual=%d loop=%d\n", rep.ActualJumps, rep.LoopJumps)
	fmt.Printf("margin/hull over %s after each boundary: actual=%.0f loop=%.0f\n", *horizon, rep.ActualMarginPerHull, rep.LoopMarginPerHull)
	zero := 0
	for _, d := range rep.Stranded {
		if d.LoopCredit == 0 {
			zero++
		}
	}
	fmt.Printf("unobservable (no sells at the loop's destination within %s; valued on its trailing rate): %d, credited zero: %d\n", *horizon, len(rep.Stranded), zero)
	for _, d := range rep.Stranded {
		if d.LoopCredit == 0 {
			fmt.Printf("  %s %s %s→%s actual=%.0f\n", d.At.Format(time.RFC3339), d.Hull, d.From, d.LoopNext, d.ActualCredit)
		}
	}
	fmt.Println("gate by valuation of the unobservable decisions:")
	for _, v := range rep.Valuations() {
		ratio := "n/a"
		if v.ActualMarginPerHull != 0 {
			ratio = fmt.Sprintf("%.3f", v.LoopMarginPerHull/v.ActualMarginPerHull)
		}
		vpass, vwhy := v.Gate()
		fmt.Printf("  %-13s n=%d jumps %d→%d margin/hull %.0f→%.0f loop/actual=%s %v — %s\n",
			v.Name, v.Boundaries, v.ActualJumps, v.LoopJumps, v.ActualMarginPerHull, v.LoopMarginPerHull, ratio, vpass, vwhy)
	}
	robust, rwhy := rep.Robust()
	fmt.Printf("ROBUST (every valuation passes): %v — %s\n", robust, rwhy)
	fmt.Printf("GATE: %v — %s\n", pass, why)
	if !pass {
		os.Exit(1)
	}
}
