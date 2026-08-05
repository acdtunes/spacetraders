// Command comment-audit censuses comment density per package and flags
// archaeology markers.
//
// It answers two separate questions. "How much of this package is comment?" is
// absolute state, checked with -max-ratio. "Did this lane make it worse?" is
// regression, checked with -baseline; that is the one a gate wants, because a
// package inherited at 40% must not block every lane that touches it while a
// lane that pushes it to 41% must.
//
// Standalone maintenance tool: nothing in the daemon, the CLI or the
// watchkeeper imports it, and it imports nothing from them.
//
// Exit codes: 0 pass, 1 violations found, 2 usage or I/O error.
//
//	comment-audit -root gobot -top 20
//	comment-audit -root gobot -write-baseline .comment-baseline.json
//	comment-audit -root gobot -baseline .comment-baseline.json -only internal/application/fleet
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

func main() {
	var (
		root       = flag.String("root", ".", "directory to scan")
		maxRatio   = flag.Float64("max-ratio", 0, "absolute ceiling on a package's comment ratio (0 disables)")
		baseline   = flag.String("baseline", "", "baseline file to check for REGRESSION against")
		writeBase  = flag.String("write-baseline", "", "write the current census to this file and exit 0")
		tolerance  = flag.Float64("tolerance", 0, "ratio increase forgiven before a regression fires; 0 is strict, and strict is the default because a proportional slack is a per-line budget on a big package (0.005 buys ~45 free comment lines across 9k)")
		only       = flag.String("only", "", "comma-separated package prefixes to check (a lane's touched set)")
		maxMarkers = flag.Int("max-markers", -1, "fail a checked package carrying more archaeology markers than this (-1 disables)")
		top        = flag.Int("top", 0, "print only the N densest packages (0 prints all)")
		explain    = flag.Bool("explain", false, "list every archaeology marker with file:line")
		asJSON     = flag.Bool("json", false, "emit the census as JSON")
		quiet      = flag.Bool("quiet", false, "print violations only")
	)
	flag.Parse()

	pkgs, err := Scan(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "comment-audit: %v\n", err)
		os.Exit(2)
	}
	if len(pkgs) == 0 {
		fmt.Fprintf(os.Stderr, "comment-audit: no non-test Go files under %s\n", *root)
		os.Exit(2)
	}

	if *writeBase != "" {
		if err := WriteBaseline(*writeBase, NewBaseline(pkgs)); err != nil {
			fmt.Fprintf(os.Stderr, "comment-audit: %v\n", err)
			os.Exit(2)
		}
		fmt.Printf("wrote baseline for %d packages to %s\n", len(pkgs), *writeBase)
		return
	}

	opts := CheckOpts{
		MaxRatio:   *maxRatio,
		Tolerance:  *tolerance,
		MaxMarkers: *maxMarkers,
	}
	if *only != "" {
		opts.Only = strings.Split(*only, ",")
	}
	if *baseline != "" {
		bl, err := LoadBaseline(*baseline)
		if err != nil {
			fmt.Fprintf(os.Stderr, "comment-audit: %v\n", err)
			os.Exit(2)
		}
		opts.Baseline = bl
	}

	if *asJSON {
		emitJSON(pkgs)
	} else if !*quiet {
		emitText(pkgs, *top, *explain)
	}

	violations := Check(pkgs, opts)
	if len(violations) == 0 {
		if !*quiet {
			fmt.Println("comment-audit: OK")
		}
		return
	}
	fmt.Fprintf(os.Stderr, "\ncomment-audit: %d violation(s)\n", len(violations))
	for _, v := range violations {
		fmt.Fprintf(os.Stderr, "  %s\n", v)
	}
	fmt.Fprintln(os.Stderr, "\nSee PLAYBOOK §13 for the keep/cut rule. History belongs in docs/retrospectives/.")
	os.Exit(1)
}

func emitText(pkgs map[string]*PkgStat, top int, explain bool) {
	list := Sorted(pkgs)
	if top > 0 && top < len(list) {
		list = list[:top]
	}
	fmt.Printf("%-58s %7s %8s %8s %8s\n", "PACKAGE", "RATIO", "COMMENT", "TOTAL", "MARKERS")
	for _, p := range list {
		fmt.Printf("%-58s %6.1f%% %8d %8d %8d\n",
			p.Package, p.Ratio()*100, p.Comment, p.Total, p.MarkerTotal())
	}
	t := Total(pkgs)
	fmt.Printf("%-58s %6.1f%% %8d %8d %8d\n",
		fmt.Sprintf("TOTAL (%d packages, %d files)", len(pkgs), t.Files),
		t.Ratio()*100, t.Comment, t.Total, t.MarkerTotal())

	if explain {
		fmt.Println("\nARCHAEOLOGY MARKERS")
		var hits []MarkerHit
		for _, p := range pkgs {
			hits = append(hits, p.Hits...)
		}
		sort.Slice(hits, func(i, j int) bool {
			if hits[i].File != hits[j].File {
				return hits[i].File < hits[j].File
			}
			return hits[i].Line < hits[j].Line
		})
		for _, h := range hits {
			fmt.Printf("  %s:%d [%s] %s\n", h.File, h.Line, h.Marker, h.Text)
		}
	}
}

func emitJSON(pkgs map[string]*PkgStat) {
	out := struct {
		Packages []*PkgStat `json:"packages"`
		Total    *PkgStat   `json:"total"`
	}{Packages: Sorted(pkgs), Total: Total(pkgs)}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "comment-audit: %v\n", err)
		os.Exit(2)
	}
}
