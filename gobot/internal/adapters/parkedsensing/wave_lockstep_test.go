package parkedsensing

// LOCKSTEP — the design's single most important invariant: the fleet-growth coordinator's heavy
// buyer and the sensing probe drain read ONE predicate.
//
// It lives beside the DRAIN'S port rather than in the application package the drain lives in,
// because the adapter layer imports that package and a test there could not construct this port
// without an import cycle. The drain's own tests pin what it DOES with the answer; this file pins
// that the answer it is handed is the predicate's, over the whole input space, and that no third
// assembly of the predicate's inputs exists anywhere in the tree.
//
// WHAT THE SWEEP BELOW ACTUALLY WITNESSES, stated plainly because the invariant is larger than the
// test and a green here must not be read as the whole of it: ONE assembly — the drain's port —
// against the pure predicate. The coordinator's own assembly does not appear in it and cannot,
// for the same import reason. The rest of the guarantee rests on the source-level call-site count
// below plus the composition root's instance pins, and two facts the two assemblies reach through
// DIFFERENT implementations are pinned by neither: the owned-heavy census (the coordinator's own
// reader versus this package's ShipRepoCensus) and the master switch (the coordinator reads its
// OWN container's config; the drain reads the declared buyer's row through HeavyBuyerCapPort).
// Those feed the target and the first clause respectively, and a drift in either is exactly the
// split-brain this file's name claims to forbid, with the sweep still green.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// waveThroughTheDrainPort drives the REAL port over fakes carrying exactly the facts in `in`, so
// the only thing varying between it and a direct DeriveWave call is the port's own assembly.
func waveThroughTheDrainPort(t *testing.T, in common.WaveInputs) (common.Wave, common.WaveProbeReason) {
	t.Helper()
	port := NewWavePort(
		&fakeSwitch{enabled: in.GrowthEnabled, exists: true},
		&fakeReserveTarget{target: in.Target},
		&fakeLanes{count: in.UnservedLanes, readable: in.UnservedLanesReadable, saturated: in.TradeSaturated},
		&fakePeak{peak: in.HighWaterTreasury, readable: in.HighWaterReadable},
		stoppedClock{time.Unix(1_700_000_000, 0)},
	)
	wave, reason, _, err := port.Wave(context.Background(), 1)
	if err != nil {
		t.Fatalf("the drain's port errored on a fully readable world: %v", err)
	}
	return wave, reason
}

// THE DRAIN'S ASSEMBLY AGAINST THE PREDICATE, across the whole input space: given identical facts
// the port must return the predicate's own verdict, so a second derivation smuggled into the port
// fails HERE rather than in the economics, where it would show up as one spender buying while the
// other withholds and nothing in any gauge saying why. The coordinator's assembly is out of reach
// from this package — see the file header for what carries that half.
func TestWaveLockstep_BothConsumersAgree(t *testing.T) {
	sawHeavy, sawProbe, sawSaturated := false, false, false
	for _, enabled := range []bool{true, false} {
		for _, lanes := range []int{0, 1, 7} {
			for _, readable := range []bool{true, false} {
				for _, saturated := range []bool{true, false} {
					for _, ask := range []int64{0, 1_000_000, 1_916_613} {
						for _, peakReadable := range []bool{true, false} {
							for highWater := int64(0); highWater <= 4_000_000; highWater += 137_119 {
								in := common.WaveInputs{
									GrowthEnabled:         enabled,
									UnservedLanes:         lanes,
									UnservedLanesReadable: readable,
									TradeSaturated:        saturated,
									// THE TWO DEMAND READS SHARE ONE READABILITY FLAG, and the sweep says so
									// deliberately rather than enumerating them apart. The count and the depth
									// come off ONE call to ONE reader over one census and one fleet, so a world
									// where the lane count is legible and its own depth is not cannot arise —
									// modelling it here would have the sweep fail the port for refusing to
									// reproduce a state the system has no way to be in.
									TradeSaturationReadable: readable,
									Target: common.HeavyReserve(common.HeavyReserveInputs{
										CapabilityOpen: ask > 0, HeaviesOwned: 0, HeavyCap: 5, TargetYardPrice: ask,
									}),
									HighWaterTreasury: highWater,
									HighWaterReadable: peakReadable,
								}
								want, wantReason := common.DeriveWave(in)
								// The drain reaches the predicate through its port; the coordinator reaches
								// it directly. Both must land on the same answer for the same facts.
								got, gotReason := waveThroughTheDrainPort(t, in)
								if got != want || gotReason != wantReason {
									t.Fatalf("split-brain at %+v: predicate=%q/%q drain=%q/%q", in, want, wantReason, got, gotReason)
								}
								switch want {
								case common.WaveHeavy:
									sawHeavy = true
								case common.WaveProbe:
									sawProbe = true
								}
								if wantReason == common.WaveProbeReasonTradeSaturated {
									sawSaturated = true
								}
							}
						}
					}
				}
			}
		}
	}
	// AND THE SWITCH-BACK CLAUSE WAS ACTUALLY EXERCISED. A sweep that carried the saturation field
	// without ever reaching the clause that reads it would agree perfectly while proving nothing
	// about whether the port assembles it at all.
	if !sawSaturated {
		t.Fatalf("the sweep never reached the trade_saturated clause — it cannot witness the port carrying that field")
	}
	// CALIBRATION. A sweep that only ever produced one regime would agree perfectly while testing
	// nothing about the clauses that flip it.
	if !sawHeavy || !sawProbe {
		t.Fatalf("the sweep never produced both regimes (heavy=%v probe=%v) — it cannot witness a divergence", sawHeavy, sawProbe)
	}
}

// EXACTLY ONE DEFINITION AND TWO READERS. A third DeriveWave call site is a third assembly of the
// predicate's inputs, which is how consumers drift even while all of them call the same pure
// function. The count is 3: the declaration, the growth coordinator's reconcile, and this drain's
// port.
//
// It is a SOURCE-level check because nothing else can see it. Two assemblies both compile, both
// pass their own unit tests, and disagree only against live data.
func TestWaveLockstep_ExactlyOneDefinitionAndTwoReaders(t *testing.T) {
	sites := grepNonTest(t, "DeriveWave(")

	total := 0
	for _, n := range sites {
		total += n
	}
	// OCCURRENCES, not files: a second call added beside an existing one is the same third assembly
	// wearing the same filename.
	if total != 3 {
		t.Fatalf("expected the definition plus exactly two readers, got %d occurrences: %v", total, sites)
	}
	want := []string{
		"internal/application/common/growth_wave.go",                        // the definition
		"internal/application/fleet/commands/run_fleet_growth_reconcile.go", // the SPENDER
		"internal/adapters/parkedsensing/wave_port.go",                      // the WITHHOLDER
	}
	for _, path := range want {
		if _, ok := sites[path]; !ok {
			t.Fatalf("%s no longer assembles the predicate; the sites found were %v — a reader that stopped calling it is as broken as a third one that started", path, sites)
		}
	}
}

// grepNonTest counts occurrences of `needle` in every non-test Go file under the module root,
// keyed by module-relative path.
//
// CALIBRATED, because an absence-and-count claim is only as good as the ground it covered and a
// walk aimed at the wrong root covers nothing while reporting success: it asserts a file-count
// floor and that it actually reached the predicate's own file. Following the module's existing
// source-level guard (cmd/spacetraders-daemon/off_gate_retired_test.go).
//
// It counts OCCURRENCES, not files, so a second call added beside an existing one is caught too.
// The consequence is that a comment writing the needle verbatim would inflate the count — which is
// the correct trade for a guard whose whole job is to notice a new call site.
func grepNonTest(t *testing.T, needle string) map[string]int {
	t.Helper()
	// go test runs with the package directory as cwd, and this package is three levels below the
	// module root.
	root := "../../.."
	const minScannedGoFiles = 500
	const sentinelFile = "internal/application/common/growth_wave.go"

	sites := map[string]int{}
	scanned := 0
	sawSentinel := false

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "vendor" || d.Name() == ".git" || d.Name() == "bin" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		scanned++
		rel := strings.TrimPrefix(filepath.ToSlash(path), "../../../")
		if rel == sentinelFile {
			sawSentinel = true
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if n := strings.Count(string(src), needle); n > 0 {
			sites[rel] = n
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the module root: %v", err)
	}
	if scanned < minScannedGoFiles {
		t.Fatalf("scanned only %d Go files — the walk is aimed at the wrong root and this count proves nothing", scanned)
	}
	if !sawSentinel {
		t.Fatalf("the walk never reached %s, so it is not watching the place the predicate lives", sentinelFile)
	}
	return sites
}
