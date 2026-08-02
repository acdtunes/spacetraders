package strip

import (
	"regexp"
	"strings"
)

// Defect is one shape the rewrite must never introduce.
type Defect struct {
	ID string
	Re *regexp.Regexp
}

// V7HeadPunct is exported so a test can prove it is LIVE. The obvious-looking
// `^//\s*[^\w\t]` form matches EVERY normal comment -- \s* takes zero characters
// and [^\w\t] then eats the space -- and because the veto is differential, a
// clause that always matches makes the entire veto set inert.
var V7HeadPunct = regexp.MustCompile(`^//[ \t]*[^\w \t]`)

// DefectChecks are applied differentially: a hit only counts when the shape is
// present after the rewrite and absent before it.
var DefectChecks = []Defect{
	{"V1", regexp.MustCompile(`\(\s*\)`)},
	{"V2", regexp.MustCompile(`\(\s*[,;/:)]`)},
	{"V3", regexp.MustCompile(`,\s*,`)},
	{"V4", regexp.MustCompile(`\s[.,;:)]`)},
	{"V5", regexp.MustCompile(`\S {2,}\S`)},
	{"V6", regexp.MustCompile(`[(,/;]\s*$`)},
	{"V7", V7HeadPunct},
	{"V8", regexp.MustCompile(`\b(?:pre|post)-[\s.,)]`)},
	{"V14", regexp.MustCompile(`^//\s*:`)},
}

// VetoName gives the report a human label for a veto id.
var VetoName = map[string]string{
	"V1": "empty-parens", "V2": "open-sep", "V3": "double-comma",
	"V4": "space-before-punct", "V5": "double-space", "V6": "dangling-sep-eol",
	"V7": "head-punct", "V8": "compound-stub", "V14": "dangling-colon",
	"V9": "body-prefix", "V10": "column-runs", "V11": "trailing-ws",
	"V12": "empty-body", "V12D": "godoc-lowercase", "V13": "orphan-seam",
}

// checkVetoes runs the differential regex set plus the structural
// post-conditions. Repair is by CONSTRUCTION, never by a cleanup pass -- a
// post-hoc global whitespace collapse is a corruption engine that eats godoc
// list indentation while the AST hash stays green.
func checkVetoes(before, after string, e editSpan, rule string, ctx LineCtx) *Veto {
	for _, d := range DefectChecks {
		if d.Re.MatchString(after) && !d.Re.MatchString(before) {
			return &Veto{Rule: rule, ID: d.ID}
		}
	}

	bs := bodyStart(before)
	if !strings.HasPrefix(after, before[:bs]) {
		return &Veto{Rule: rule, ID: "V9"}
	}
	if !sameRunOffsets(before, after) {
		return &Veto{Rule: rule, ID: "V10"}
	}
	if strings.TrimRight(after, " \t") != after && strings.TrimRight(before, " \t") == before {
		return &Veto{Rule: rule, ID: "V11"}
	}
	// An empty // is a godoc paragraph break, not a comment to be emptied.
	if strings.Trim(strings.NewReplacer("/", "", "*", "").Replace(after), " \t") == "" {
		return &Veto{Rule: rule, ID: "V12"}
	}
	// Differential: the tool may never REGRESS a godoc first line into opening
	// with a lowercase word, but a line that already opened that way is not the
	// tool's doing and is left alone.
	if ctx.IsDocFirstLine && opensLowercase(after, ctx.DeclName) && !opensLowercase(before, ctx.DeclName) {
		return &Veto{Rule: rule, ID: "V12D"}
	}
	// The orphan-preamble check is POSITIONAL, at the deletion seam. A line-wide
	// regex false-fires on legitimate prose such as "...re-apply spec. The
	// launch config".
	if orphanGovernor(before, e.start, e.end) {
		return &Veto{Rule: rule, ID: "V13"}
	}
	return nil
}

// opensLowercase reports that the comment body starts with a lowercase word
// other than the declared identifier's own name.
func opensLowercase(s, declName string) bool {
	body := s[bodyStart(s):]
	if body == "" || !isLower(body[0]) {
		return false
	}
	j := 0
	for j < len(body) && (isLetter(body[j]) || body[j] == '_' || (body[j] >= '0' && body[j] <= '9')) {
		j++
	}
	return body[:j] != declName
}
