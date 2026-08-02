// Package strip removes bare bead-id provenance tokens from Go line comments.
//
// The scope is deliberately narrow and deterministic: it strips citation
// tokens and the punctuation that leaves with them. It never deletes prose,
// never deletes a whole comment, never reflows, and never inserts a byte.
// Archaeological prose removal needs judgment and is a separate phase.
package strip

import (
	"regexp"
	"strings"
)

// The leading \b is load-bearing, not decoration: without it the pattern also
// matches inside ordinary English compounds (lowe|st-effort, late|st-write-wins,
// wor|st-case), which is over a thousand extra "ids" across the tree. The
// pattern is case-sensitive so uppercase symbols can never match.
const idRe = `\b(?:sp|st)-[a-z0-9]{3,}(?:-[a-z0-9]+)*(?:\.[0-9]+)?\b`

// An id-list is ONE unit: its internal separators leave with it, so a list can
// never be half-deleted into a dangling comma.
const idListRe = idRe + `(?:[ \t]*(?:[,;/+&]|and)[ \t]*` + idRe + `)*`

var (
	IDPattern     = regexp.MustCompile(idRe)
	IDListPattern = regexp.MustCompile(idListRe)
)

// Citation furniture: a closed, removable set. "per", "from", "closes" and
// "fixes" are deliberately absent -- they are ordinary prepositions and verbs,
// and removing them deletes prose.
var preWords = map[string]bool{
	"bead": true, "beads": true, "epic": true, "epics": true,
	"spec": true, "see": true, "ref": true, "cf": true, "cf.": true,
}

// Function words that cannot end a clause. Used ONLY positionally, to detect
// that a deletion would strand a governor with nothing to govern.
var funcWords = map[string]bool{
	"by": true, "of": true, "to": true, "for": true, "from": true, "since": true,
	"until": true, "per": true, "in": true, "on": true, "at": true, "via": true,
	"see": true, "cf": true, "ref": true, "with": true, "and": true, "or": true,
	"before": true, "after": true, "under": true, "over": true, "the": true,
	"a": true, "an": true, "bead": true, "beads": true, "epic": true,
	"epics": true, "spec": true, "than": true, "vs": true, "versus": true,
	"into": true, "onto": true, "about": true, "against": true, "between": true,
	"during": true, "without": true, "within": true,
}

// A preceding determiner marks an ATTRIBUTIVE position: the id modifies the
// noun that follows rather than being the head noun itself.
var dets = map[string]bool{
	"the": true, "a": true, "an": true, "this": true, "that": true,
	"these": true, "those": true, "its": true, "our": true, "their": true,
	"same": true, "one": true, "each": true, "every": true, "both": true,
	"no": true, "any": true, "all": true, "new": true, "per": true,
}

var (
	preWordRe    = regexp.MustCompile(`(?i)\b(beads|bead|epics|epic|spec|see|ref|cf)\b`)
	decorationRe = regexp.MustCompile(`^[-=*#~]{2,}[ ]*$`)
	sepSuffixRe  = regexp.MustCompile(`[ \t]*[,;/+&][ \t]*$`)
	sepPrefixRe  = regexp.MustCompile(`^[ \t]*[,;/+&][ \t]*`)
	elidedTailRe = regexp.MustCompile(`^/[a-z0-9]{3,}`)
	dashSuffixRe = regexp.MustCompile(`[ \t](?:\x{2014}|\x{2013}|--)[ \t]$`)
	initialSepRe = regexp.MustCompile(`^[ \t]*(?:[:;,/+&]|\x{2014}|\x{2013}|--)[ \t]*`)
	directiveRe  = regexp.MustCompile(`^//(?:go:|line |export |sys |sysnb |nolint|lint:|\+build)`)
	frozenRe     = regexp.MustCompile(`^//\t.*[ ]{3,}`)
	wordRe       = regexp.MustCompile(`[A-Za-z]+`)
	runRe        = regexp.MustCompile(` {3,}`)
	semiSuffixRe = regexp.MustCompile(`;[ \t]*$`)

	// bannerRe matches a hand-drawn ASCII rule that CLOSES the line, which is
	// the only kind whose width is alignment: its siblings are padded to the
	// same column. runOffsets fingerprints runs of SPACES only, so a dash ruler
	// carries no protection and every deletion silently shortens it. A
	// leading-only "// --- Title" has nothing to misalign and stays processable.
	bannerRe = regexp.MustCompile(`[ \t][-=*~]{3,}[ \t]*$`)

	// rulerRe is the broader shape, used only to decide that the line ABOVE was
	// a divider or heading rather than a wrapped sentence.
	rulerRe = regexp.MustCompile(`(?:^|[ \t])[-=*~]{3,}(?:[ \t]|$)`)

	// indexedLabelRe matches a bare, INDEXED sub-reference: "invariant 2",
	// "phase 2.5", "Fix #2", "feeder crash #4", "build-decomp #5", "L2", "C2c",
	// "§4a". Alone it names nothing -- its antecedent is the citation in front
	// of it -- so that citation is not deletable.
	indexedLabelRe = regexp.MustCompile(`^(?:` +
		`\x{00a7}` +
		`|#[ \t]*\d` +
		`|[A-Z]\d[A-Za-z0-9.]*(?:[^A-Za-z0-9]|$)` +
		`|[A-Za-z][A-Za-z]*(?:-[A-Za-z]+)*(?:[ \t]+[A-Za-z][A-Za-z-]*)?[ \t]+#?\d` +
		`)`)
)

// refLabels are nouns whose only identifying content is the citation in front
// of them. "(the <id> invariant)" reduces to "(the invariant)", a definite
// description with no referent -- and the prose-word bag cannot see it, because
// every surviving word is still there.
var refLabels = map[string]bool{
	"invariant": true, "invariants": true, "phase": true, "phases": true,
	"part": true, "parts": true, "fix": true, "fixes": true,
	"round": true, "rounds": true, "gap": true, "gaps": true,
	"layer": true, "layers": true, "step": true, "steps": true,
	"stage": true, "stages": true, "item": true, "items": true,
	"amendment": true, "amendments": true, "decomp": true,
	"extension": true, "extensions": true, "review": true,
	"ruling": true, "rulings": true, "section": true, "sections": true,
	"clause": true, "clauses": true, "appendix": true,
	"milestone": true, "milestones": true, "wave": true, "waves": true,
	"tranche": true, "tranches": true, "iteration": true, "iterations": true,
	"crash": true, "bug": true, "bugs": true, "defect": true, "defects": true,
	"finding": true, "findings": true, "lesson": true, "lessons": true,
	"win": true, "change": true, "changes": true, "regression": true,
	"regressions": true, "discipline": true, "pattern": true, "patterns": true,
	"variant": true, "variants": true, "scenario": true, "scenarios": true,
	"cutover": true, "revision": true, "addendum": true,
}

// finiteVerbs are FINITE forms only: auxiliaries, modals, irregular pasts and
// the third-person singulars this corpus actually uses. Bare infinitives are
// deliberately absent -- "<id> pick the resolver" is a wrapped sentence, caught
// by the previous-line gate, and admitting infinitives here would swallow
// ordinary noun phrases ("<id> cost model", "<id> run loop").
var finiteVerbs = map[string]bool{
	"is": true, "was": true, "are": true, "were": true, "has": true,
	"have": true, "had": true, "does": true, "did": true, "will": true,
	"would": true, "shall": true, "should": true, "can": true, "could": true,
	"may": true, "might": true, "must": true, "cannot": true,

	// Irregular pasts.
	"made": true, "took": true, "gave": true, "went": true, "became": true,
	"kept": true, "left": true, "broke": true, "brought": true, "built": true,
	"said": true, "told": true, "saw": true, "ran": true, "won": true,
	"lost": true, "found": true, "meant": true, "sent": true, "spent": true,
	"held": true, "drove": true, "chose": true, "wrote": true, "got": true,
	"came": true, "knew": true, "thought": true, "felt": true, "grew": true,
	"threw": true, "caught": true, "taught": true, "bought": true, "sold": true,
	"paid": true, "fed": true, "led": true, "met": true, "stood": true,
	"sat": true, "rose": true, "fell": true, "drew": true, "began": true,
	"gone": true, "done": true, "seen": true, "run": true,

	// Third-person singular present.
	"retires": true, "extends": true, "removes": true, "moves": true,
	"proves": true, "makes": true, "takes": true, "adds": true, "pins": true,
	"fixes": true, "keeps": true, "holds": true, "drops": true, "stops": true,
	"starts": true, "returns": true, "requires": true, "ensures": true,
	"prevents": true, "allows": true, "forces": true, "causes": true,
	"treats": true, "reads": true, "writes": true, "sets": true, "gets": true,
	"uses": true, "needs": true, "means": true, "shows": true, "leaves": true,
	"lands": true, "ships": true, "lets": true, "puts": true, "runs": true,
	"calls": true, "handles": true, "covers": true, "applies": true,
	"replaces": true, "restores": true, "refuses": true, "rejects": true,
	"accepts": true, "skips": true, "blocks": true, "gates": true,
	"raises": true, "lowers": true, "widens": true, "narrows": true,
	"tightens": true, "retains": true, "becomes": true, "remains": true,
	"exists": true, "works": true, "fails": true, "passes": true,
	"wins": true, "loses": true, "sends": true, "spends": true, "picks": true,
	"happens": true, "turns": true, "closes": true, "opens": true,
	"breaks": true, "builds": true, "brings": true, "chooses": true,
	"drives": true, "feeds": true, "leads": true, "meets": true,
	"introduces": true, "supersedes": true, "supplies": true, "carries": true,
	"teaches": true, "buys": true, "sells": true, "pays": true, "hits": true,
	"costs": true, "kills": true, "splits": true, "merges": true,
}

// nonVerbEds keeps the -ed SUFFIX rule from swallowing nouns. Anything shorter
// than five letters ("need", "feed", "seed", "deed") is already excluded by
// length, so only the longer ones need naming.
var nonVerbEds = map[string]bool{
	"speed": true, "breed": true, "greed": true, "creed": true, "steed": true,
	"tweed": true, "indeed": true, "hundred": true, "sacred": true,
	"naked": true, "embed": true, "exceed": true, "proceed": true,
	"succeed": true, "misdeed": true, "seaweed": true, "hatred": true,
}

// isFiniteVerb reports that a word can head a finite clause, which makes an id
// sitting immediately in front of it the clause SUBJECT. Over-answering true
// only costs a citation that stays; answering false wrongly ships a sentence
// that says something the original did not.
func isFiniteVerb(w string) bool {
	w = strings.ToLower(strings.Trim(w, "-'’"))
	if w == "" {
		return false
	}
	if finiteVerbs[w] {
		return true
	}
	// A hyphenated compound is judged by its head: "re-homed", "de-scoped".
	if i := strings.LastIndexByte(w, '-'); i >= 0 && i+1 < len(w) {
		w = w[i+1:]
		if finiteVerbs[w] {
			return true
		}
	}
	return len(w) >= 5 && strings.HasSuffix(w, "ed") && !nonVerbEds[w]
}

// refLabel reports that a word is a sub-reference noun once its hyphen prefix
// ("layer-b" -> "layer") is stripped.
func refLabel(w string) bool {
	w = strings.ToLower(strings.Trim(w, "-'’.,;:)]"))
	if w == "" {
		return false
	}
	if refLabels[w] {
		return true
	}
	if i := strings.IndexByte(w, '-'); i > 0 {
		return refLabels[w[:i]]
	}
	return false
}

// nextWord returns the word starting at the first non-space byte at or after i,
// taking a hyphenated compound as ONE word.
func nextWord(s string, i int) string {
	j := firstNonSpaceFrom(s, i)
	k := j
	for k < len(s) && (isLetter(s[k]) || (s[k] >= '0' && s[k] <= '9') || s[k] == '-' || s[k] == '\'') {
		k++
	}
	return s[j:k]
}

// endsSentence reports that the previous comment line closed its sentence,
// which is the only context in which a line-initial id is a LEADING TAG. On a
// wrapped sentence the same id is mid-clause. Empty means the line opens its
// comment group, so there is no wrapped sentence to be inside of.
func endsSentence(prev string) bool {
	p := strings.TrimRight(prev, " \t")
	if p == "" || rulerRe.MatchString(p) {
		return true
	}
	if strings.Trim(strings.TrimPrefix(p, "//"), " \t") == "" {
		return true // a bare "//" paragraph break
	}
	p = strings.TrimRight(p, ")]\"'`”’")
	if p == "" {
		return false
	}
	switch p[len(p)-1] {
	case '.', '!', '?', ':':
		return true
	}
	return false
}

// endsWithGovernor reports that the previous comment line ends on a function
// word whose object is the id at the start of this one. Even a parenthetical
// deletion, which never capitalises, strands "...proven by" if it fires here.
func endsWithGovernor(prev string) bool {
	w := strings.ToLower(lastWord(strings.TrimRight(prev, " \t")))
	return w != "" && funcWords[w]
}

// residueTrimSet is the punctuation a bracket may be left holding once every
// id-list and citation preamble is notionally removed. If only these survive,
// the bracket is pure citation furniture and goes as a unit.
const residueTrimSet = " \t,;/+&:—–-"

// bodyStart returns the index just past "//" and its following space/tab run.
// No deletion may ever begin before it, which is what makes the comment marker
// and a line's indent structurally unreachable.
func bodyStart(s string) int {
	i := 2
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

// lastWord returns the trailing word, ignoring any opening bracket or quote
// glued to its front. "(the" must read as "the" or the attributive tests are
// blind to every determiner that opens a parenthetical.
func lastWord(x string) string {
	x = strings.TrimRight(x, " \t")
	i := len(x)
	for i > 0 {
		c := x[i-1]
		if !isLetter(c) && !(c >= '0' && c <= '9') && c != '_' && c != '.' && c != '\'' && c != '-' {
			break
		}
		i--
	}
	return x[i:]
}

func isDeriv(id string) bool { return strings.Count(id, "-") >= 2 }

func idsIn(list string) []string { return IDPattern.FindAllString(list, -1) }

func isLetter(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }

func isLower(b byte) bool { return b >= 'a' && b <= 'z' }

// firstNonSpaceFrom returns the index of the first non-space/tab byte at or
// after i, or len(s).
func firstNonSpaceFrom(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

// cap1 is the ONLY non-deletion operation in the tool, and it is
// length-preserving: it uppercases exactly one ASCII letter. It refuses
// anything that might be an identifier, a call, or a selector.
func cap1(s string) string {
	i := firstNonSpaceFrom(s, 0)
	if i >= len(s) {
		return s
	}
	j := i
	for j < len(s) && (isLetter(s[j]) || (s[j] >= '0' && s[j] <= '9') || s[j] == '_') {
		j++
	}
	w := s[i:j]
	if w == "" || !isLower(w[0]) {
		return s
	}
	if strings.Contains(w, "_") || strings.ToLower(w) != w {
		return s
	}
	if j < len(s) && (s[j] == '(' || s[j] == '.') {
		return s
	}
	if i > 0 && (s[i-1] == '`' || s[i-1] == '"') {
		return s
	}
	return s[:i] + strings.ToUpper(w[:1]) + s[i+1:]
}

func leadWS(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return s[:i]
}

// runOffsets fingerprints every run of three-or-more spaces as offset:length.
// Equality of the fingerprint is what protects an ASCII table's columns
// tree-wide, without the tool needing to know any table exists.
func runOffsets(s string) string {
	var b strings.Builder
	for _, m := range runRe.FindAllStringIndex(s, -1) {
		b.WriteString(itoa(m[0]))
		b.WriteByte(':')
		b.WriteString(itoa(m[1] - m[0]))
		b.WriteByte(',')
	}
	return b.String()
}

func sameRunOffsets(a, b string) bool { return runOffsets(a) == runOffsets(b) }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// proseWords is the machine proof of "never delete prose": the bag of
// alphabetic words with every id-list removed first, so the id's own syllables
// do not count as prose.
func proseWords(src []byte) map[string]int {
	m := map[string]int{}
	for _, line := range strings.Split(string(src), "\n") {
		clean := IDListPattern.ReplaceAllString(line, "")
		for _, w := range wordRe.FindAllString(clean, -1) {
			m[strings.ToLower(w)]++
		}
	}
	return m
}

// missingWords reports words the rewrite lost. preWords are excluded because
// they are the one class the tool is licensed to remove; nothing else may go.
func missingWords(before, after map[string]int) []string {
	var lost []string
	for w, n := range before {
		if preWords[w] {
			continue
		}
		if after[w] < n {
			lost = append(lost, w)
		}
	}
	return lost
}
