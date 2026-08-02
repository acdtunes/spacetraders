package strip

import "strings"

// Scope selects how far a string literal's mention of an id reaches when
// deciding that the id is live vocabulary rather than provenance.
type Scope string

const (
	ScopeFile   Scope = "file"
	ScopeDecl   Scope = "decl"
	ScopePkg    Scope = "pkg"
	ScopeHybrid Scope = "hybrid"
	ScopeRepo   Scope = "repo"
)

type Options struct {
	LiteralScope Scope
	MaxPasses    int
	Rules        map[string]bool // nil or empty enables every rule
	Explain      bool            // record every non-stripped occurrence
}

func (o Options) passes() int {
	if o.MaxPasses <= 0 {
		return 8
	}
	return o.MaxPasses
}

func (o Options) on(rule string) bool {
	if len(o.Rules) == 0 {
		return true
	}
	return o.Rules[rule]
}

// LineCtx carries the only file-level facts a single comment line needs.
//
// PrevLine is the comment line immediately above this one inside the SAME
// comment group, empty when this line opens the group. Without it every
// line-initial id reads as a leading tag, including the ones that are simply
// where a wrapped sentence happened to break.
type LineCtx struct {
	IsDocFirstLine bool
	DeclName       string
	PrevLine       string
}

type Skip struct {
	ID     string
	Reason string
}

type Veto struct {
	Rule string
	ID   string
}

type LineResult struct {
	Text   string
	Rules  []string
	Skips  []Skip
	Vetoes []Veto
}

// ClassifyComment reports the gate that skips this comment kind entirely, or ""
// if the comment is processable.
func ClassifyComment(text string) string {
	switch {
	case strings.HasPrefix(text, "/*"):
		// The only shape where one comment node spans several physical lines.
		// Skipping it is what lets every rule reason line-locally.
		return "S-BLOCK"
	case directiveRe.MatchString(text):
		return "S-DIRECTIVE"
	case frozenRe.MatchString(text):
		// A tab-indented block with space-padded columns. Content-based, so it
		// protects any hand-aligned table without the tool naming a file.
		return "S-FROZEN-TABLE"
	case bannerRe.MatchString(text):
		// A hand-drawn ASCII rule. Its width is the alignment, and every
		// deletion is a shortening, so the only safe edit is none.
		return "S-BANNER"
	}
	return ""
}

type occ struct {
	a, b      int
	ids       []string
	ga, gb    int
	inBracket bool
	head      bool
}

type editSpan struct {
	start, end int
	capitalise bool
}

func (e editSpan) apply(s string) string {
	tail := s[e.end:]
	if e.capitalise {
		tail = cap1(tail)
	}
	return s[:e.start] + tail
}

// bracketOf finds the innermost same-line () pair enclosing [a,b). Because the
// stack pops innermost-first, the first enclosing pair completed is the one.
func bracketOf(s string, bs, a, b int) (int, int, bool) {
	var stack []int
	for i := bs; i < len(s); i++ {
		switch s[i] {
		case '(':
			stack = append(stack, i)
		case ')':
			if n := len(stack); n > 0 {
				open := stack[n-1]
				stack = stack[:n-1]
				if open < a && i >= b {
					return open, i, true
				}
			}
		}
	}
	return 0, 0, false
}

// crossLine reports that the id sits in a parenthetical that opens or closes on
// another physical line. Depth is clamped at zero: a ')' seen at depth 0 is a
// continuation-close, not an error, and treating it as one over-skips hundreds
// of safe occurrences.
func crossLine(s string, a, b int) bool {
	depth := 0
	for k := 0; k < a; k++ {
		switch s[k] {
		case '(':
			depth++
		case ')':
			if depth--; depth < 0 {
				depth = 0
			}
		}
	}
	if depth > 0 {
		d, closed := depth, false
		for k := a; k < len(s) && !closed; k++ {
			switch s[k] {
			case '(':
				d++
			case ')':
				if d--; d == 0 {
					closed = true
				}
			}
		}
		if !closed {
			return true
		}
	}
	for k := b; k < len(s); k++ {
		if s[k] == '(' {
			break
		}
		if s[k] == ')' {
			return depth == 0
		}
	}
	return false
}

// leftOK is the governance gate. It is false when the id is the object of a
// verb or preposition, where no pure deletion is grammatical.
func leftOK(s string, a, bs int) bool {
	if a == bs {
		return true
	}
	left := s[bs:a]
	if decorationRe.MatchString(left) {
		return true
	}
	switch s[a-1] {
	case '(', '[', ',', ';', '/', '+', '&', ':':
		return true
	}
	if sepSuffixRe.MatchString(left) || dashSuffixRe.MatchString(left) {
		return true
	}
	w := strings.ToLower(lastWord(left))
	return dets[w] || preWords[w]
}

// orphanGovernor reports that removing [delStart,delEnd) would strand a
// function word with nothing to govern -- "proven by ." and friends.
func orphanGovernor(s string, delStart, delEnd int) bool {
	if !funcWords[strings.ToLower(lastWord(s[:delStart]))] {
		return false
	}
	r := strings.TrimLeft(s[delEnd:], " \t")
	if r == "" {
		return true
	}
	switch r[0] {
	case '.', ',', ';', ':', ')':
		return true
	}
	return false
}

// residueEmpty evaluates a bracket ONCE with all id-lists removed
// simultaneously. Per-token residue is the bug that turns
// "(sp-a, epic sp-b). sp-c" into "( sp-c".
func residueEmpty(inner string) bool {
	r := IDListPattern.ReplaceAllString(inner, "")
	r = preWordRe.ReplaceAllString(r, "")
	return strings.Trim(r, residueTrimSet) == ""
}

func terminalAfter(s string, b int) bool {
	k := firstNonSpaceFrom(s, b)
	if k >= len(s) {
		return true
	}
	return s[k] == ')' || s[k] == ':'
}

// contentAfter reports that real content -- not punctuation, not end of line --
// follows position b. It is what distinguishes a citation that TRAILS its
// clause from one that OPENS the next.
func contentAfter(s string, b, hi int) bool {
	k := firstNonSpaceFrom(s, b)
	return k < hi && (isLetter(s[k]) || (s[k] >= '0' && s[k] <= '9'))
}

// phrase returns the region a citation shares with its neighbours: the
// enclosing bracket, or the citation alone. The guards below are evaluated over
// the whole region so a citation LIST is refused as one unit -- half-stripping
// "(sp-a/sp-b, epic sp-c C2c)" into "(epic sp-c C2c)" is the worst of both
// outcomes, and it is what makes id survival look arbitrary.
func phrase(s string, o occ) (lo, hi int, bracketed bool) {
	if o.inBracket {
		return o.ga + 1, o.gb, true
	}
	return o.a, len(s), false
}

// anyCitationFollowedBy reports that some citation in this occurrence's phrase
// is immediately followed by material the predicate rejects. A BARE citation
// binds only what directly follows itself; a bracketed one shares the verdict
// with every citation in its bracket.
func anyCitationFollowedBy(s string, o occ, pred func(string, int, int) bool) bool {
	lo, hi, bracketed := phrase(s, o)
	if !bracketed {
		if o.b < len(s) && (s[o.b] == ' ' || s[o.b] == '\t') {
			return pred(s, o.b, len(s))
		}
		return false
	}
	for _, m := range IDListPattern.FindAllStringIndex(s[lo:hi], -1) {
		b := lo + m[1]
		if b < hi && (s[b] == ' ' || s[b] == '\t') && pred(s, b, hi) {
			return true
		}
	}
	return false
}

// subjectVerbAt reports that the word after the citation is a finite verb, so
// the citation is that verb's SUBJECT. Deleting it does not shorten a sentence,
// it re-attributes the action to whatever noun happens to sit further left --
// and because no prose word is lost, the AST hash, the prose-word bag and the
// defect regexes all stay green while the meaning inverts.
func subjectVerbAt(s string, b, hi int) bool {
	return isFiniteVerb(nextWord(s[:hi], b))
}

// sublabelAt reports that the citation is the ANTECEDENT of the label that
// follows it. Deleting it leaves "(invariant 2)", "(§4a)", "Fix 3:" -- a live
// cross-reference reduced to an unresolvable fragment.
func sublabelAt(s string, b, hi int) bool {
	j := firstNonSpaceFrom(s, b)
	if j >= hi {
		return false
	}
	seg := s[j:hi]
	if indexedLabelRe.MatchString(seg) {
		return true
	}
	w1 := nextWord(seg, 0)
	if refLabel(w1) {
		return true
	}
	return w1 != "" && refLabel(nextWord(seg, len(w1)))
}

// clauseBoundary reports that a citation in this phrase OPENS an independent
// clause introduced by ';'. The BACK list rules take the ';' away with the id
// and fuse two clauses into a run-on: "the override; sp-fjvlm moved only"
// becomes "the override moved only", the opposite of what it said. ';' is a
// clause boundary, never a list separator, once real content follows.
func clauseBoundary(s string, bs int, o occ) bool {
	lo, hi, bracketed := phrase(s, o)
	if !bracketed {
		return semiSuffixRe.MatchString(s[bs:o.a]) && contentAfter(s, o.b, len(s))
	}
	for _, m := range IDListPattern.FindAllStringIndex(s[lo:hi], -1) {
		a, b := lo+m[0], lo+m[1]
		if semiSuffixRe.MatchString(s[lo:a]) && contentAfter(s, b, hi) {
			return true
		}
	}
	return false
}

func occurrences(s string, bs int) []occ {
	var out []occ
	for _, m := range IDListPattern.FindAllStringIndex(s, -1) {
		a, b := m[0], m[1]
		if a < bs {
			continue
		}
		o := occ{a: a, b: b, ids: idsIn(s[a:b])}
		o.ga, o.gb, o.inBracket = bracketOf(s, bs, a, b)
		o.head = a == bs || (o.inBracket && o.ga == bs && o.gb == b)
		out = append(out, o)
	}
	return out
}

// tryOcc returns the edit the ordered rule list produces for one occurrence,
// or the id of the skip that refused it. Exactly one of the two is set.
func tryOcc(s string, bs int, o occ, lits map[string]bool, opts Options, ctx LineCtx) (*editSpan, string, string) {
	for _, id := range o.ids {
		if isDeriv(id) {
			return nil, "", "S-TOKEN-DERIV"
		}
	}
	for _, id := range o.ids {
		if lits[id] {
			return nil, "", "S-TOKEN-LITERAL"
		}
	}
	if o.a > 0 && s[o.a-1] == '-' {
		return nil, "", "S-COMPOUND"
	}
	// A "/bare" tail is a prefix-elided id list — "sp-9ptm/2me2/yfzi" names three
	// beads with the "sp-" left off the last two. IDListPattern only reaches the
	// head, so stripping it strands the tail as "(2me2/yfzi intact)", a reference
	// to nothing. Same compound class as the "-" case above.
	if elidedTailRe.MatchString(s[o.b:]) {
		return nil, "", "S-COMPOUND"
	}
	if o.a > 0 && (s[o.a-1] == '\'' || s[o.a-1] == '"' || s[o.a-1] == '`') {
		return nil, "", "S-QUOTED"
	}
	if strings.HasPrefix(s[o.b:], "'s") || strings.HasPrefix(s[o.b:], "’s") {
		return nil, "", "S-POSSESSIVE"
	}
	if crossLine(s, o.a, o.b) {
		return nil, "", "S-CROSSLINE"
	}

	// Grammar gates. All three are invisible to every downstream check: they
	// describe rewrites that lose no word, balance every bracket and keep the
	// AST byte-identical while saying something the original did not. They are
	// ordered before the rule cascade because no rule may reach the shape.
	if clauseBoundary(s, bs, o) {
		return nil, "", "S-CLAUSE-BOUNDARY"
	}
	if anyCitationFollowedBy(s, o, subjectVerbAt) {
		return nil, "", "S-SUBJECT-VERB"
	}
	if anyCitationFollowedBy(s, o, sublabelAt) {
		return nil, "", "S-SUBLABEL"
	}

	parenHead := o.inBracket && o.ga == bs && o.gb == o.b

	// A line-initial citation reads as a LEADING TAG only when the previous
	// comment line closed its sentence. On a wrapped sentence the same id is
	// mid-clause, and the head rules delete it AND capitalise the next word --
	// "That belief is what let / sp-e46yc happen" becomes "...what let /
	// Happen". A parenthetical (R8) never capitalises and so survives a wrap,
	// but not a governor dangling at the end of the line above.
	tagPos := o.head && endsSentence(ctx.PrevLine)
	contOK := o.head && !endsWithGovernor(ctx.PrevLine)
	// The bracket holds the id-list and NOTHING else. Closing at the id is not
	// enough: without the opening test, a leading bracket that also carries real
	// prose ("(panel 502 / <id>):", "(pkg.SymbolName, <id>):") reads as a bare
	// tag and its prose is deleted along with the id.
	soleParenHead := parenHead && o.a == o.ga+1

	// 1. R5b -- "(id):" at head is ONE lexical tag; the colon leaves with it.
	if opts.on("R5b-LEAD-PAREN-COLON") && tagPos && soleParenHead && o.gb+1 < len(s) && s[o.gb+1] == ':' {
		if k := firstNonSpaceFrom(s, o.gb+2); k > o.gb+2 && k < len(s) {
			return &editSpan{start: bs, end: k, capitalise: true}, "R5b-LEAD-PAREN-COLON", ""
		}
	}
	// 2. R5a -- "id:" at head.
	if opts.on("R5a-LEAD-COLON") && tagPos && o.a == bs && o.b < len(s) && s[o.b] == ':' {
		if k := firstNonSpaceFrom(s, o.b+1); k > o.b+1 && k < len(s) {
			return &editSpan{start: bs, end: k, capitalise: true}, "R5a-LEAD-COLON", ""
		}
	}
	// 3. The survivor would open the line on punctuation, so the governing word
	// is on the previous line. The test is on the CHARACTER CLASS, not a
	// punctuation list: V7 is differential and a line that already opened with
	// '(' can be rewritten into one that opens with an em-dash without ever
	// tripping it.
	if o.head {
		hend := o.b
		if parenHead {
			hend = o.gb + 1
		}
		if k := firstNonSpaceFrom(s, hend); k < len(s) && !isLetter(s[k]) && !(s[k] >= '0' && s[k] <= '9') {
			return nil, "", "S-HEADPUNCT"
		}
	}
	// 3b. Wrapped sentence. Reported here rather than by silent fall-through so
	// the gate is visible in the skip histogram and cannot go inert unnoticed.
	if o.head && !tagPos && !(parenHead && contOK) {
		return nil, "", "S-WRAPPED-SENTENCE"
	}
	// 4. R8 -- "(id)" at head on a continuation line. No capitalisation: the
	// survivor continues the previous sentence.
	if opts.on("R8-LEAD-PAREN") && parenHead && contOK && residueEmpty(s[o.ga+1:o.gb]) {
		if k := firstNonSpaceFrom(s, o.gb+1); k > o.gb+1 && k < len(s) {
			return &editSpan{start: bs, end: k}, "R8-LEAD-PAREN", ""
		}
	}
	// 5. R5c -- bare leading tag. ID-ONLY: the qualifier after it is prose and
	// survives -- unless it is an INDEXED sub-label, which the gate above has
	// already refused because it would survive with nothing to refer to.
	if opts.on("R5c-LEAD-SPACE") && tagPos && o.a == bs && o.b < len(s) && (s[o.b] == ' ' || s[o.b] == '\t') {
		if k := firstNonSpaceFrom(s, o.b); k < len(s) {
			return &editSpan{start: bs, end: k, capitalise: true}, "R5c-LEAD-SPACE", ""
		}
	}
	// 6. R7 -- a whole parenthetical that is nothing but citation. Evaluated
	// before the governance gate, with its own governor check.
	if opts.on("R7-PAREN-SOLE") && o.inBracket && !o.head && residueEmpty(s[o.ga+1:o.gb]) &&
		o.ga-1 >= bs && s[o.ga-1] == ' ' && (o.ga-2 < bs || s[o.ga-2] != ' ') {
		if orphanGovernor(s, o.ga-1, o.gb+1) {
			return nil, "", "S-ORPHAN-GOVERNOR"
		}
		return &editSpan{start: o.ga - 1, end: o.gb + 1}, "R7-PAREN-SOLE", ""
	}
	// 7. Governance gate for every remaining deletion.
	if !o.head && !leftOK(s, o.a, bs) {
		return nil, "", "S-GOVERNED"
	}
	// 8. R6 -- a citation preamble immediately left of the id, inside a bracket
	// that keeps other content.
	if opts.on("R6-PREAMBLE") && o.inBracket && !residueEmpty(s[o.ga+1:o.gb]) {
		left := strings.TrimRight(s[o.ga+1:o.a], " \t")
		w := lastWord(left)
		if preWords[strings.ToLower(w)] && len(left) >= len(w) {
			pStart := o.ga + 1 + len(left) - len(w)
			end := firstNonSpaceFrom(s, o.b)
			if end > o.gb {
				end = o.gb
			}
			if strings.TrimSpace(s[o.ga+1:pStart]+s[end:o.gb]) != "" {
				return &editSpan{start: pStart, end: end}, "R6-PREAMBLE", ""
			}
		}
	}
	// 9. R9 -- bracket-initial id in a bracket that keeps other content.
	if opts.on("R9-BRACKET-INITIAL") && o.inBracket && o.a == o.ga+1 && !residueEmpty(s[o.ga+1:o.gb]) {
		end := o.b
		if m := initialSepRe.FindStringIndex(s[o.b:o.gb]); m != nil {
			end = o.b + m[1]
		} else {
			end = firstNonSpaceFrom(s, o.b)
			if end > o.gb {
				end = o.gb
			}
		}
		if strings.TrimSpace(s[end:o.gb]) != "" {
			return &editSpan{start: o.a, end: end}, "R9-BRACKET-INITIAL", ""
		}
	}
	// 10. R10 -- attributive inside a bracket. Ordered BEFORE the separator
	// tests: inverting them turns "(+ sp-x broadening)" into "( broadening)".
	if opts.on("R10-PAREN-ATTRIB") && o.inBracket && !o.head &&
		o.b < len(s) && s[o.b] == ' ' && o.b+1 < o.gb && isLetter(s[o.b+1]) {
		return &editSpan{start: o.a, end: o.b + 1}, "R10-PAREN-ATTRIB", ""
	}
	// 11/12. R11 -- one list separator leaves with the id, never both sides.
	if o.inBracket {
		if opts.on("R11-PAREN-LIST-FWD") {
			if m := sepPrefixRe.FindStringIndex(s[o.b:o.gb]); m != nil && m[1] > 0 {
				return &editSpan{start: o.a, end: o.b + m[1]}, "R11-PAREN-LIST-FWD", ""
			}
		}
		if opts.on("R11-PAREN-LIST-BACK") {
			if m := sepSuffixRe.FindStringIndex(s[o.ga+1 : o.a]); m != nil {
				return &editSpan{start: o.ga + 1 + m[0], end: o.b}, "R11-PAREN-LIST-BACK", ""
			}
		}
		// 13. R12 -- em-dash citation, TERMINAL only. A non-terminal id is the
		// SUBJECT of the following clause; deleting it changes who did what.
		if opts.on("R12-PAREN-DASH") && o.b == o.gb {
			if m := dashSuffixRe.FindStringIndex(s[o.ga+1 : o.a]); m != nil {
				return &editSpan{start: o.ga + 1 + m[0], end: o.b}, "R12-PAREN-DASH", ""
			}
		}
	} else {
		// 14. R16 -- bare em-dash citation, TERMINAL only.
		if opts.on("R16-BARE-DASH") && dashSuffixRe.MatchString(s[bs:o.a]) && terminalAfter(s, o.b) {
			m := dashSuffixRe.FindStringIndex(s[bs:o.a])
			return &editSpan{start: bs + m[0], end: o.b}, "R16-BARE-DASH", ""
		}
		// 15/16. R17 -- bare list separators.
		if opts.on("R17-BARE-LIST-FWD") {
			if m := sepPrefixRe.FindStringIndex(s[o.b:]); m != nil && m[1] > 0 {
				return &editSpan{start: o.a, end: o.b + m[1]}, "R17-BARE-LIST-FWD", ""
			}
		}
		if opts.on("R17-BARE-LIST-BACK") {
			if m := sepSuffixRe.FindStringIndex(s[bs:o.a]); m != nil {
				return &editSpan{start: bs + m[0], end: o.b}, "R17-BARE-LIST-BACK", ""
			}
		}
		// 17. R14 -- bare attributive. The following-lowercase-word test is what
		// proves MODIFIER rather than head noun or clause subject.
		if opts.on("R14-BARE-ATTRIB") && o.b+1 < len(s) && s[o.b] == ' ' && isLower(s[o.b+1]) &&
			dets[strings.ToLower(lastWord(s[bs:o.a]))] {
			return &editSpan{start: o.a, end: o.b + 1}, "R14-BARE-ATTRIB", ""
		}
	}

	if dashSuffixRe.MatchString(s[bs:o.a]) && !terminalAfter(s, o.b) {
		return nil, "", "S-DASH-NONTERMINAL"
	}
	if o.inBracket {
		return nil, "", "S-PAREN-OTHER"
	}
	return nil, "", "S-BARE-OTHER"
}

// RewriteComment applies the ruleset to one line comment, one structural edit
// per pass, to a fixpoint. Sequencing is load-bearing: it lets
// "(sp-a/sp-b, epic sp-c C2c)" reduce to "(C2c)" in two passes, which
// simultaneous span application cannot reach.
func RewriteComment(text string, lits map[string]bool, opts Options, ctx LineCtx) LineResult {
	if r := ClassifyComment(text); r != "" {
		var skips []Skip
		for _, id := range IDPattern.FindAllString(text, -1) {
			skips = append(skips, Skip{ID: id, Reason: r})
		}
		return LineResult{Text: text, Skips: skips}
	}

	cur := text
	res := LineResult{}
	vetoed := false
	for p := 0; p < opts.passes(); p++ {
		next, rule, v, ok := onePass(cur, lits, opts, ctx)
		if v != nil {
			res.Vetoes = append(res.Vetoes, *v)
			vetoed = true
			break
		}
		if !ok {
			break
		}
		cur = next
		res.Rules = append(res.Rules, rule)
	}
	res.Text = cur
	res.Skips = classifySurvivors(cur, lits, opts, ctx, vetoed)
	return res
}

func onePass(s string, lits map[string]bool, opts Options, ctx LineCtx) (string, string, *Veto, bool) {
	bs := bodyStart(s)
	for _, o := range occurrences(s, bs) {
		e, rule, reason := tryOcc(s, bs, o, lits, opts, ctx)
		if reason != "" {
			continue
		}
		if e.start < bs {
			continue // structural clamp: the comment marker is unreachable
		}
		cand := e.apply(s)
		if v := checkVetoes(s, cand, *e, rule, ctx); v != nil {
			return s, "", v, false
		}
		return cand, rule, nil, true
	}
	return s, "", nil, false
}

func classifySurvivors(s string, lits map[string]bool, opts Options, ctx LineCtx, vetoed bool) []Skip {
	bs := bodyStart(s)
	var out []Skip
	for _, o := range occurrences(s, bs) {
		_, _, reason := tryOcc(s, bs, o, lits, opts, ctx)
		if reason == "" {
			reason = "S-MAXPASSES"
			if vetoed {
				reason = "S-VETOED"
			}
		}
		for _, id := range o.ids {
			out = append(out, Skip{ID: id, Reason: reason})
		}
	}
	return out
}
