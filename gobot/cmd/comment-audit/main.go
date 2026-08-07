// Command comment-audit censuses comment density per package and flags
// archaeology markers.
//
// It answers two questions. "How much of this package is comment?" is absolute
// state (-max-ratio). "Did this lane make it worse?" is regression (-baseline),
// and it is the one a gate wants: inherited density must not block a lane that
// merely touches a package. -gate applies the standing limits without naming them.
//
// Standalone: nothing in the daemon, CLI or watchkeeper imports it, nor it them.
//
// Exit codes: 0 pass, 1 violations found, 2 usage or I/O error.
//
//	comment-audit -root gobot -top 20
//	comment-audit -root gobot -gate -baseline .comment-baseline.json -only internal/application/fleet
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

// cliFlags holds the parsed command line, registrable against a throwaway
// FlagSet so a test can check that gateOwnedFlags names flags that exist.
type cliFlags struct {
	root       *string
	maxRatio   *float64
	baseline   *string
	writeBase  *string
	tolerance  *float64
	only       *string
	maxMarkers *int
	maxFile    *float64
	gate       *bool
	top        *int
	explain    *bool
	asJSON     *bool
	quiet      *bool
}

func registerFlags(fs *flag.FlagSet) *cliFlags {
	return &cliFlags{
		root:       fs.String("root", ".", "directory to scan"),
		maxRatio:   fs.Float64("max-ratio", 0, "absolute ceiling on a package's comment ratio (0 disables)"),
		baseline:   fs.String("baseline", "", "baseline file to check for REGRESSION against"),
		writeBase:  fs.String("write-baseline", "", "write the current census to this file and exit 0"),
		tolerance:  fs.Float64("tolerance", 0, "ratio increase forgiven before a regression fires; 0 is strict, and strict is the default because a ratio slack is really a per-line budget that grows with the package"),
		only:       fs.String("only", "", "package prefixes to check (a lane's touched set), separated by commas or spaces; QUOTE the value so the shell keeps it as one argument"),
		maxMarkers: fs.Int("max-markers", -1, "fail a checked package carrying more archaeology markers than this (-1 disables)"),
		maxFile: fs.Float64("max-file-prose", 0, fmt.Sprintf(
			"per-FILE ceiling on comment beyond the doc credit — the package doc, plus the first %d lines of each exported declaration's godoc; the package ratchet cannot see a single essay inside a large package (0 disables)", DocAllowance)),
		gate:    fs.Bool("gate", false, "apply the standing gate policy: baseline regression plus the built-in per-file ceiling. Prefer this to spelling the limits out, so a caller cannot drift from the policy the gate test enforces. Refuses the limit flags it would otherwise override"),
		top:     fs.Int("top", 0, "print only the N densest packages (0 prints all)"),
		explain: fs.Bool("explain", false, "list every archaeology marker with file:line"),
		asJSON:  fs.Bool("json", false, "emit the census as JSON"),
		quiet:   fs.Bool("quiet", false, "print violations only"),
	}
}

func main() {
	c := registerFlags(flag.CommandLine)
	flag.Parse()

	pkgs, err := Scan(*c.root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "comment-audit: %v\n", err)
		os.Exit(2)
	}
	if len(pkgs) == 0 {
		fmt.Fprintf(os.Stderr, "comment-audit: no non-test Go files under %s\n", *c.root)
		os.Exit(2)
	}

	if *c.writeBase != "" {
		if err := WriteBaseline(*c.writeBase, NewBaseline(pkgs)); err != nil {
			fmt.Fprintf(os.Stderr, "comment-audit: %v\n", err)
			os.Exit(2)
		}
		fmt.Printf("wrote baseline for %d packages to %s\n", len(pkgs), *c.writeBase)
		return
	}

	opts := CheckOpts{
		MaxRatio:          *c.maxRatio,
		Tolerance:         *c.tolerance,
		MaxMarkers:        *c.maxMarkers,
		MaxFileProseRatio: *c.maxFile,
	}
	selected, err2 := resolveOnly(*c.only, flag.Args())
	if err2 != nil {
		fmt.Fprintf(os.Stderr, "comment-audit: %v\n", err2)
		os.Exit(2)
	}
	opts.Scope = PackageSubtrees(selected)
	if *c.baseline != "" {
		bl, err := LoadBaseline(*c.baseline)
		if err != nil {
			fmt.Fprintf(os.Stderr, "comment-audit: %v\n", err)
			os.Exit(2)
		}
		opts.Baseline = bl
	}
	if *c.gate {
		gated, err := gatePolicyFor(opts.Baseline, opts.Scope, flagsSet(flag.CommandLine))
		if err != nil {
			fmt.Fprintf(os.Stderr, "comment-audit: %v\n", err)
			os.Exit(2)
		}
		opts = gated
	}

	if *c.asJSON {
		emitJSON(pkgs)
	} else if !*c.quiet {
		emitText(pkgs, *c.top, *c.explain)
	}

	violations := Check(pkgs, opts)
	if len(violations) == 0 {
		if !*c.quiet {
			fmt.Println("comment-audit: OK")
		}
		return
	}
	fmt.Fprintf(os.Stderr, "\ncomment-audit: %d violation(s)\n", len(violations))
	for _, v := range violations {
		fmt.Fprintf(os.Stderr, "  %s\n", v)
	}
	fmt.Fprintf(os.Stderr, "\nSee ENGINEERING.md §6 for the keep/cut rule. History belongs in docs/retrospectives/.\n"+
		"A per-file failure counts comment the doc credit does not cover: everything except the\n"+
		"package doc and the first %d lines of each exported declaration's godoc. Godoc past that\n"+
		"cap is counted like any other comment, so a file can fail on documentation alone.\n", DocAllowance)
	os.Exit(1)
}

// gateOwnedFlags are the limits GatePolicy sets for itself, named as a caller writes them.
var gateOwnedFlags = []string{"max-ratio", "tolerance", "max-markers", "max-file-prose"}

// flagsSet reports the flags the caller actually wrote: Visit skips defaults.
func flagsSet(fs *flag.FlagSet) map[string]bool {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}

// gatePolicyFor builds the standing gate policy, or REFUSES. -gate replaces
// every limit with the policy's own, so a limit the caller also wrote could only
// be discarded, and an OK from a run that quietly checked something else is
// indistinguishable from a real one. A missing baseline is refused for the same
// reason: the ratchet cannot run at all without it.
func gatePolicyFor(bl *Baseline, scope Scope, set map[string]bool) (CheckOpts, error) {
	var clash []string
	for _, name := range gateOwnedFlags {
		if set[name] {
			clash = append(clash, "-"+name)
		}
	}
	if len(clash) > 0 {
		return CheckOpts{}, fmt.Errorf(
			"-gate carries the standing limits, so %s cannot be set alongside it: one of the two "+
				"would have to be dropped, and this tool will not drop one silently. "+
				"Drop the flag to take the gate policy, or drop -gate to spell your own limits out",
			strings.Join(clash, " and "))
	}
	if bl == nil {
		return CheckOpts{}, fmt.Errorf(
			"-gate needs -baseline: the ratchet is a comparison against the recorded census, and " +
				"without one only the per-file ceiling would run — a package the lane made denser " +
				"would report OK")
	}
	return GatePolicy(bl, scope), nil
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

// resolveOnly turns the -only value, plus anything the flag parser could not consume, into the
// set of package prefixes to check.
//
// THE LEFTOVERS ARE THE BUG, AND REFUSING ON THEM IS THE POINT. An unquoted list arrives already
// split: -only takes the first entry, the rest become positional arguments, and Go drops every
// flag after them. The tool would check one package of several and report OK — a pass
// indistinguishable from a real one, the one failure a checking tool must not have.
func resolveOnly(value string, leftovers []string) ([]string, error) {
	if len(leftovers) > 0 {
		return nil, fmt.Errorf(
			"unexpected argument %q after -only: the package list reached this tool already split up, "+
				"so only the first entry would have been checked and every later flag ignored. "+
				"Quote it and separate with commas: -only \"pkg1,pkg2\"", leftovers[0])
	}
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	if len(fields) == 0 {
		return nil, nil
	}
	return fields, nil
}
