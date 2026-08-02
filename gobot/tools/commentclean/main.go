// Command commentclean strips bare bead-id provenance tokens from Go line
// comments.
//
// It is a standalone maintenance tool. Nothing in the daemon, the CLI or the
// watchkeeper imports it, and it imports nothing from them.
//
// Default mode is --dry-run. Writing requires --write, and a file is only
// written after its rewritten form is proven AST-identical to the original.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/andrescamacho/spacetraders-go/tools/commentclean/internal/strip"
)

type totals struct {
	Files              int            `json:"files"`
	FilesChanged       int            `json:"filesChanged"`
	LinesChanged       int            `json:"linesChanged"`
	Occurrences        int            `json:"occurrences"`
	LiteralOccurrences int            `json:"literalOccurrences"`
	Stripped           int            `json:"stripped"`
	IDsRemoved         int            `json:"idsRemoved"`
	Skipped            int            `json:"skipped"`
	Vetoed             int            `json:"vetoed"`
	Rejected           int            `json:"rejected"`
	ByRule             map[string]int `json:"byRule"`
	BySkip             map[string]int `json:"bySkip"`
	ByVeto             map[string]int `json:"byVeto"`
}

type report struct {
	Scope     string              `json:"scope"`
	MaxPasses int                 `json:"maxPasses"`
	Totals    totals              `json:"totals"`
	Rejects   []string            `json:"rejected"`
	Files     []*strip.FileResult `json:"files"`
}

func main() {
	var (
		dryRun     = flag.Bool("dry-run", true, "analyse and report; write nothing")
		write      = flag.Bool("write", false, "apply changes in place")
		diff       = flag.Bool("diff", false, "print a unified diff to stdout")
		check      = flag.Bool("check", false, "exit 1 if any occurrence would be stripped")
		scope      = flag.String("literal-scope", "hybrid", "file | decl | pkg | hybrid | repo")
		maxPasses  = flag.Int("max-passes", 8, "fixpoint bound")
		rules      = flag.String("rules", "", "restrict to a comma-separated rule id list")
		explain    = flag.Bool("explain", false, "print every skipped occurrence with its gate")
		reportFmt  = flag.String("report", "text", "text | json")
		failOnVeto = flag.Bool("fail-on-veto", false, "exit 2 if any veto fires")
	)
	flag.Parse()

	if *write && *check {
		fatal("--write and --check are mutually exclusive")
	}
	if *write {
		*dryRun = false
	}
	if *check {
		*dryRun = true
	}

	switch strip.Scope(*scope) {
	case strip.ScopeFile, strip.ScopeDecl, strip.ScopePkg, strip.ScopeHybrid, strip.ScopeRepo:
	default:
		fatal("unknown --literal-scope %q", *scope)
	}

	root, err := moduleRoot()
	if err != nil {
		fatal("%v", err)
	}
	paths := flag.Args()
	if len(paths) == 0 {
		paths = []string{root}
	}

	opts := strip.Options{
		LiteralScope: strip.Scope(*scope),
		MaxPasses:    *maxPasses,
		Rules:        parseRules(*rules),
		Explain:      *explain,
	}

	// The literal index is always built over the WHOLE module: a non-test
	// literal couples repo-wide, so narrowing it to the requested paths would
	// silently widen what the tool is willing to delete.
	idx, err := strip.BuildLiteralIndex(root)
	if err != nil {
		fatal("building literal index: %v", err)
	}
	files, err := strip.InScopeFiles(root, paths)
	if err != nil {
		fatal("%v", err)
	}

	rep := report{
		Scope:     *scope,
		MaxPasses: *maxPasses,
		Totals:    totals{ByRule: map[string]int{}, BySkip: map[string]int{}, ByVeto: map[string]int{}},
	}
	var pending []*strip.FileResult

	for _, abs := range files {
		src, err := os.ReadFile(abs)
		if err != nil {
			fatal("%v", err)
		}
		res, err := strip.ProcessFile(root, abs, src, idx, opts)
		if err != nil {
			fatal("%s: %v", abs, err)
		}
		rep.Totals.Files++
		rep.Totals.Occurrences += res.Occurrences
		rep.Totals.LiteralOccurrences += res.LiteralOccurrences
		rep.Totals.Stripped += res.Stripped
		rep.Totals.IDsRemoved += res.IDsRemoved
		rep.Totals.LinesChanged += res.LinesChanged
		for k, v := range res.Skips {
			rep.Totals.BySkip[k] += v
			rep.Totals.Skipped += v
		}
		for k, v := range res.Vetoes {
			rep.Totals.ByVeto[k] += v
			rep.Totals.Vetoed += v
		}
		for _, e := range res.Edits {
			rep.Totals.ByRule[e.Rule]++
		}
		if res.Rejected != "" {
			rep.Totals.Rejected++
			rep.Rejects = append(rep.Rejects, res.Rel+": "+res.Rejected)
		}
		if res.Changed {
			rep.Totals.FilesChanged++
			pending = append(pending, res)
		}
		if res.Stripped > 0 || res.Rejected != "" || (*explain && len(res.Explanations) > 0) {
			rep.Files = append(rep.Files, res)
		}
		if *diff && res.Changed {
			printDiff(res.Rel, src, res.New)
		}
		if *explain {
			for _, e := range res.Explanations {
				fmt.Printf("%s:%d\t%s\t%s\t%s\n", res.Rel, e.Line, e.Reason, e.ID, strings.TrimSpace(e.Text))
			}
		}
	}

	// A rejection anywhere aborts the whole run BEFORE any write.
	if rep.Totals.Rejected > 0 {
		emit(rep, *reportFmt)
		fmt.Fprintf(os.Stderr, "\nverification FAILED for %d file(s); nothing written\n", rep.Totals.Rejected)
		os.Exit(2)
	}

	if !*dryRun && *write {
		for _, res := range pending {
			info, err := os.Stat(res.Path)
			if err != nil {
				fatal("%v", err)
			}
			if err := os.WriteFile(res.Path, res.New, info.Mode().Perm()); err != nil {
				fatal("writing %s: %v", res.Rel, err)
			}
		}
	}

	emit(rep, *reportFmt)

	if *failOnVeto && rep.Totals.Vetoed > 0 {
		os.Exit(2)
	}
	if *check && rep.Totals.Stripped > 0 {
		os.Exit(1)
	}
}

func emit(rep report, format string) {
	if format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fatal("%v", err)
		}
		return
	}
	printText(rep)
}

func printText(rep report) {
	fmt.Printf("commentclean  scope=%s  passes<=%d\n\n", rep.Scope, rep.MaxPasses)
	changed := make([]*strip.FileResult, 0, len(rep.Files))
	for _, f := range rep.Files {
		if f.Stripped > 0 {
			changed = append(changed, f)
		}
	}
	sort.Slice(changed, func(i, j int) bool { return changed[i].Stripped > changed[j].Stripped })
	if len(changed) > 0 {
		fmt.Printf("%-62s %5s %6s %5s %5s\n", "FILE", "OCC", "STRIP", "SKIP", "VETO")
		for _, f := range changed {
			skips, vetoes := 0, 0
			for _, v := range f.Skips {
				skips += v
			}
			for _, v := range f.Vetoes {
				vetoes += v
			}
			fmt.Printf("%-62s %5d %6d %5d %5d\n", trunc(f.Rel, 62), f.Occurrences, f.Stripped, skips, vetoes)
		}
		fmt.Println()
	}

	t := rep.Totals
	fmt.Println("TOTALS")
	line := func(label string, v int) {
		fmt.Printf("  %s %s %d\n", label, strings.Repeat(".", 34-len(label)), v)
	}
	line("files scanned", t.Files)
	line("files changed", t.FilesChanged)
	line("comment lines changed", t.LinesChanged)
	line("occurrences (comment)", t.Occurrences)
	line("occurrences (literal, untouched)", t.LiteralOccurrences)
	line("stripped (rule applications)", t.Stripped)
	line("ids removed", t.IDsRemoved)
	line("skipped", t.Skipped)
	line("vetoed", t.Vetoed)
	line("REJECTED by the AST guard", t.Rejected)

	printBag("\nBY RULE", t.ByRule)
	printBag("\nBY SKIP", t.BySkip)
	printBag("\nBY VETO", t.ByVeto)

	if len(rep.Rejects) > 0 {
		fmt.Println("\nREJECTED")
		for _, r := range rep.Rejects {
			fmt.Println("  " + r)
		}
	}
}

func printBag(title string, m map[string]int) {
	if len(m) == 0 {
		return
	}
	fmt.Println(title)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		label := k
		if n, ok := strip.VetoName[strings.SplitN(k, "/", 2)[0]]; ok {
			label = k + " (" + n + ")"
		}
		fmt.Printf("  %-44s %6d\n", label, m[k])
	}
}

// printDiff emits a unified diff. Line counts are provably equal, so hunks are
// built directly from the changed line numbers rather than by a diff algorithm.
func printDiff(rel string, before, after []byte) {
	bl := strings.Split(string(before), "\n")
	al := strings.Split(string(after), "\n")
	var changed []int
	for i := range bl {
		if bl[i] != al[i] {
			changed = append(changed, i)
		}
	}
	if len(changed) == 0 {
		return
	}
	fmt.Printf("--- a/%s\n+++ b/%s\n", rel, rel)
	const ctx = 3
	for i := 0; i < len(changed); {
		start, end := changed[i], changed[i]
		j := i + 1
		for j < len(changed) && changed[j]-end <= 2*ctx {
			end = changed[j]
			j++
		}
		lo, hi := max(0, start-ctx), min(len(bl)-1, end+ctx)
		fmt.Printf("@@ -%d,%d +%d,%d @@\n", lo+1, hi-lo+1, lo+1, hi-lo+1)
		for k := lo; k <= hi; k++ {
			if bl[k] == al[k] {
				fmt.Println(" " + bl[k])
				continue
			}
			fmt.Println("-" + bl[k])
			fmt.Println("+" + al[k])
		}
		i = j
	}
}

func parseRules(s string) map[string]bool {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	m := map[string]bool{}
	for _, r := range strings.Split(s, ",") {
		if r = strings.TrimSpace(r); r != "" {
			m[r] = true
		}
	}
	return m
}

func moduleRoot() (string, error) {
	d, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d, nil
		}
		p := filepath.Dir(d)
		if p == d {
			return "", fmt.Errorf("no go.mod found walking up from the working directory")
		}
		d = p
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n+3:]
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "commentclean: "+format+"\n", args...)
	os.Exit(2)
}
