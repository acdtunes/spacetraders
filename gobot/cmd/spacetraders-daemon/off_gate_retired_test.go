package main

// OFF-GATE WARP EXPANSION IS RETIRED, and this is the guard that keeps it retired (sp-mn3it).
//
// WHY A SOURCE-LEVEL CHECK AND NOT A BEHAVIOURAL ONE. The thing being asserted is that no code
// path can EMIT explorer demand onto a latching bridge. A behavioural test needs a port to observe,
// and the port is exactly what was deleted — an assertion cannot outlive its own mechanism. Nor can
// the claim be made by grepping for an interface's implementations: Go interfaces are satisfied
// IMPLICITLY, so an adapter can satisfy one without ever naming it, and a "no implementations
// found" search proves nothing. What CAN be asserted soundly is absence of the vocabulary itself:
// if no file declares OffGateDemandSink, then no value can be of that type, so no shape can satisfy
// it, whatever it is called.
//
// It lives in the composition root because that is where the wiring it forbids would have to appear.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// retiredOffGateIdentifiers is the vocabulary of the retired slice. Any one of them reappearing in
// Go source means the demand path is being rebuilt.
var retiredOffGateIdentifiers = []string{
	"OffGateDemandSink",
	"OffGateDemandSignal",
	"OffGateDemandSource",
	"EmitOffGateDemand",
	"ExplorerOffGateBridge",
	"ExplorerDemandProvider",
	"advanceOffGate",
	"retractOffGateDemand",
	"OffGatePorts",
}

// minScannedGoFiles calibrates the walk. An absence claim is only as good as the ground it covered,
// and a walk aimed at the wrong root covers nothing while reporting success. The module holds well
// over a thousand Go files; anything near the size of this one package means the root drifted.
const minScannedGoFiles = 500

// compositionRootFile is the second calibration: the one file the forbidden wiring would have to be
// written into. If the walk never reaches it, the guard is not watching the place it exists to watch.
const compositionRootFile = "cmd/spacetraders-daemon/main.go"

func TestOffGateWarpExpansionStaysRetired(t *testing.T) {
	// go test runs with the package directory as cwd, and cmd/spacetraders-daemon is two levels
	// below the module root. A tree-wide absence claim needs the whole tree, not one file.
	root := "../.."
	found := map[string][]string{}
	scanned := 0
	sawCompositionRoot := false

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip build output and anything vendored; this test reads OUR source only.
			if d.Name() == "vendor" || d.Name() == ".git" || d.Name() == "bin" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// The guard must not find itself.
		if strings.HasSuffix(path, "off_gate_retired_test.go") {
			return nil
		}
		scanned++
		if strings.HasSuffix(filepath.ToSlash(path), compositionRootFile) {
			sawCompositionRoot = true
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		text := string(src)
		for _, ident := range retiredOffGateIdentifiers {
			if strings.Contains(text, ident) {
				found[ident] = append(found[ident], path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the source tree: %v", err)
	}

	// Calibration before the subject: a mis-aimed walk finds nothing and reads as a pass, which is
	// absence of evidence dressed up as evidence of absence. Both checks fail loudly instead.
	if scanned < minScannedGoFiles {
		t.Fatalf("scanned only %d Go files under %q (want >= %d) — the walk is mis-aimed, so its absence claim proves nothing",
			scanned, root, minScannedGoFiles)
	}
	if !sawCompositionRoot {
		t.Fatalf("the walk under %q never reached %s — the guard is not watching the composition root, where the forbidden wiring would have to appear",
			root, compositionRootFile)
	}

	if len(found) != 0 {
		for ident, files := range found {
			t.Errorf("%q reappeared in %v — off-gate warp expansion is RETIRED (sp-mn3it): the demand bridge LATCHES, so re-introducing a writer strands a standing demand nobody serves",
				ident, files)
		}
	}
}
