package highlight

import (
	"strings"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"

	"github.com/eugenioenko/ttt/internal/term"
)

type Span struct {
	Start int
	End   int
	Style term.Style
}

// noRegion marks a line that starts outside every multi-line region.
const noRegion int8 = -1

// spanKey keys the span cache; the same text differs by incoming state.
type spanKey struct {
	line   string
	region int8
}

// A region is a delimiter pair the language lets span lines: a block comment,
// a docstring, a raw or template string. ttt tokenises one line at a time and
// chroma exposes no way to resume a lexer's state stack on the next line, so a
// line that starts inside a region is coloured from the region's own style
// until its closing delimiter, and only the remainder is lexed.
type region struct {
	open  string
	close string
	style term.Style
	// tokenType is what the lexer called the opening delimiter during the
	// probe. Matching it exactly is what keeps a backtick inside a
	// single-quoted string from opening a template literal.
	tokenType chroma.TokenType
	// escapes reports that a backslash escapes the closing delimiter: true for
	// strings, false for comments, where nothing is special.
	escapes bool
}

// openAt is where a line opens a region it never closes, region noRegion when
// it opens none.
type openAt struct {
	col    int
	region int8
}

var noOpen = openAt{col: -1, region: noRegion}

type Highlighter struct {
	lexer chroma.Lexer
	cache map[spanKey][]Span

	// Empty when the language has no multi-line region; state tracking is
	// then skipped entirely.
	regions []region
	// openLeads holds the distinct first bytes of every region's opening
	// delimiter, for the per-line prescan.
	openLeads string

	// opens memoizes where a line opens a region that outlives it. A pure
	// function of the text, so it survives ClearCache.
	opens map[string]openAt

	// states[i] is the region line i starts inside, noRegion for none.
	states []int8
	// stateSrc[i] is the text of line i that produced states[i+1], so an edit
	// can be located by comparison instead of discarding the whole table.
	stateSrc []string
	// Set by ClearCache; the table is revalidated on the next lookup.
	statesDirty bool
}

func New(filename string) *Highlighter {
	externalOnce.Do(loadExternalLexers)
	lexer := lexers.Match(filename)
	if lexer == nil {
		return nil
	}
	h := &Highlighter{lexer: chroma.Coalesce(lexer)}
	h.regions = detectRegions(lexer)
	for _, r := range h.regions {
		if !strings.ContainsAny(h.openLeads, r.open[:1]) {
			h.openLeads += r.open[:1]
		}
	}
	return h
}

func (h *Highlighter) Language() string {
	return h.lexer.Config().Name
}

// HighlightLine highlights a line in isolation, ignoring multi-line regions.
func (h *Highlighter) HighlightLine(line string) []Span {
	return h.highlight(line, noRegion)
}

// HighlightLineAt highlights lines[idx], carrying multi-line region state down
// from the top of the buffer.
func (h *Highlighter) HighlightLineAt(lines []string, idx int) []Span {
	if idx < 0 || idx >= len(lines) {
		return nil
	}
	return h.highlight(lines[idx], h.stateAt(lines, idx))
}

func (h *Highlighter) ClearCache() {
	h.cache = make(map[spanKey][]Span)
	h.statesDirty = true
}

func (h *Highlighter) highlight(line string, reg int8) []Span {
	key := spanKey{line: line, region: reg}
	if h.cache != nil {
		if cached, ok := h.cache[key]; ok {
			return cached
		}
	}
	spans := h.computeSpans(line, reg)
	if h.cache == nil {
		h.cache = make(map[spanKey][]Span)
	}
	h.cache[key] = spans
	return spans
}

func (h *Highlighter) computeSpans(line string, reg int8) []Span {
	if reg >= 0 && int(reg) < len(h.regions) {
		r := h.regions[reg]
		end := closesAt(line, r)
		if end < 0 {
			if line == "" {
				return nil
			}
			return []Span{{Start: 0, End: len([]rune(line)), Style: r.style}}
		}
		spans := []Span{{Start: 0, End: end, Style: r.style}}
		for _, s := range h.computeSpans(string([]rune(line)[end:]), noRegion) {
			spans = append(spans, Span{Start: s.Start + end, End: s.End + end, Style: s.Style})
		}
		return spans
	}

	spans := h.lexLine(line)
	open := h.opensAt(line)
	if open.region < 0 {
		return spans
	}
	// A region opens here and runs past end of line. Fresh slice: spans is cached.
	out := make([]Span, 0, len(spans)+1)
	for _, s := range spans {
		if s.End <= open.col {
			out = append(out, s)
		} else if s.Start < open.col {
			out = append(out, Span{Start: s.Start, End: open.col, Style: s.Style})
		}
	}
	style := h.regions[open.region].style
	return append(out, Span{Start: open.col, End: len([]rune(line)), Style: style})
}

// A third-party XML lexer can panic mid-tokenise (missing `using` delegate,
// bad state). Losing color on one line beats taking the editor down.
func (h *Highlighter) lexLine(line string) (spansOut []Span) {
	defer func() {
		if recover() != nil {
			spansOut = nil
		}
	}()
	iter, err := h.lexer.Tokenise(nil, line+"\n")
	if err != nil {
		return nil
	}
	var spans []Span
	pos := 0
	for _, tok := range iter.Tokens() {
		text := strings.TrimRight(tok.Value, "\n")
		if text == "" {
			continue
		}
		runeLen := len([]rune(text))
		style := mapTokenType(tok.Type)
		if style != term.StyleDefault {
			spans = append(spans, Span{
				Start: pos,
				End:   pos + runeLen,
				Style: style,
			})
		}
		pos += runeLen
	}
	return spans
}

// stateAt reports which region lines[idx] starts inside, extending the state
// table as needed. Transitions are memoized, so this is a map lookup per line
// rather than a re-lex.
func (h *Highlighter) stateAt(lines []string, idx int) int8 {
	if len(h.regions) == 0 || idx <= 0 {
		return noRegion
	}
	if h.statesDirty {
		h.statesDirty = false
		h.truncateToEdit(lines)
	}
	if len(h.states) == 0 {
		h.states = append(h.states, noRegion)
	}
	for len(h.states) <= idx && len(h.states) <= len(lines) {
		i := len(h.states) - 1
		h.states = append(h.states, h.nextState(lines[i], h.states[i]))
		h.stateSrc = append(h.stateSrc, lines[i])
	}
	if idx < len(h.states) {
		return h.states[idx]
	}
	return noRegion
}

// truncateToEdit drops the state table from the first line whose text changed,
// keeping everything above it. Comparing strings that were never rewritten hits
// Go's identical-pointer fast path, so unchanged lines cost no scanning.
func (h *Highlighter) truncateToEdit(lines []string) {
	keep := min(len(h.stateSrc), len(lines))
	for i := range keep {
		if h.stateSrc[i] != lines[i] {
			keep = i
			break
		}
	}
	h.stateSrc = h.stateSrc[:keep]
	if len(h.states) > keep+1 {
		h.states = h.states[:keep+1]
	}
}

// nextState advances region state across one line.
func (h *Highlighter) nextState(line string, reg int8) int8 {
	for reg >= 0 && int(reg) < len(h.regions) {
		end := closesAt(line, h.regions[reg])
		if end < 0 {
			return reg
		}
		line = string([]rune(line)[end:])
		reg = noRegion
	}
	return h.opensAt(line).region
}

// closesAt returns the rune index just past the region's closing delimiter, or
// -1. Only a string region honours backslash escapes; inside a comment nothing
// is special, so that case is a plain substring search. Neither path
// materialises the line as runes: this runs once per line when the state table
// is rebuilt over a whole buffer.
func closesAt(line string, r region) int {
	if r.close == "" {
		return -1
	}
	if !r.escapes {
		i := strings.Index(line, r.close)
		if i < 0 {
			return -1
		}
		return utf8.RuneCountInString(line[:i+len(r.close)])
	}
	runes := 0
	for i := 0; i < len(line); {
		if line[i] == '\\' {
			i++
			runes++
			if i < len(line) {
				_, w := utf8.DecodeRuneInString(line[i:])
				i += w
				runes++
			}
			continue
		}
		if strings.HasPrefix(line[i:], r.close) {
			return runes + utf8.RuneCountInString(r.close)
		}
		_, w := utf8.DecodeRuneInString(line[i:])
		i += w
		runes++
	}
	return -1
}

func (h *Highlighter) opensAt(line string) openAt {
	// Fast path, and the common one: no delimiter on the line means no lex
	// and no cache entry, which keeps rebuilding the state table over a large
	// buffer to one scan per line.
	if !h.mayOpen(line) {
		return noOpen
	}
	if at, ok := h.opens[line]; ok {
		return at
	}
	at := h.computeOpensAt(line)
	if h.opens == nil {
		h.opens = make(map[string]openAt)
	}
	h.opens[line] = at
	return at
}

// mayOpen is the line's first filter: one pass looking for the leading byte of
// any region's opening delimiter.
func (h *Highlighter) mayOpen(line string) bool {
	return h.openLeads != "" && strings.ContainsAny(line, h.openLeads)
}

// computeOpensAt returns the earliest region the line opens and never closes.
// Appending the closer makes the region well formed, so chroma's own rules
// decide: an opener inside a string or after a line comment is ignored. For a
// symmetric delimiter such as a backtick the appended closer can itself look
// like an opener, which is why an opener at or past the end of the original
// line is rejected.
func (h *Highlighter) computeOpensAt(line string) openAt {
	best := noOpen
	lineLen := len([]rune(line))
	for i := range h.regions {
		r := h.regions[i]
		if !strings.Contains(line, r.open) {
			continue
		}
		if best.col >= 0 && strings.Index(line, r.open) > best.col {
			continue
		}
		start := h.trailingRegionStart(line+r.close, r)
		if start < 0 || start >= lineLen {
			continue
		}
		if best.col < 0 || start < best.col {
			best = openAt{col: start, region: int8(i)}
		}
	}
	return best
}

// trailingRegionStart returns the rune index where the region that reaches the
// end of text begins, or -1 when the text does not end inside one.
func (h *Highlighter) trailingRegionStart(text string, r region) int {
	iter, err := h.lexer.Tokenise(nil, text)
	if err != nil {
		return -1
	}
	pos, start := 0, -1
	for _, tok := range iter.Tokens() {
		switch {
		case tok.Type == r.tokenType && strings.HasPrefix(tok.Value, r.open):
			start = pos
		case tok.Type != r.tokenType:
			start = -1
		}
		pos += len([]rune(tok.Value))
	}
	return start
}

// Probed in order to discover which multi-line regions the language has. The
// probe spans two lines because a single-line construct cannot, which is what
// stops Haskell's "--" from matching the "--[[" candidate.
//
// A symmetric delimiter needs a second, stricter test. Chroma coalesces
// adjacent tokens of the same type, so `""" a\nb """` looks like one string
// in Go and Rust, neither of which has a triple-quoted literal; they read it
// as `""`, `" a\nb "`, `""`. Requiring the raw lexer to emit the delimiter as
// a token of its own separates a real region, where the opener pushes a
// state, from three literals in a row.
var regionCandidates = []struct {
	open      string
	close     string
	style     term.Style
	escapes   bool
	ownsDelim bool
	match     func(chroma.TokenType) bool
}{
	{open: "/*", close: "*/", style: term.StyleSyntaxComment, match: isCommentToken},
	{open: "<!--", close: "-->", style: term.StyleSyntaxComment, match: isCommentToken},
	{open: "{-", close: "-}", style: term.StyleSyntaxComment, match: isCommentToken},
	{open: "(*", close: "*)", style: term.StyleSyntaxComment, match: isCommentToken},
	{open: "--[[", close: "]]", style: term.StyleSyntaxComment, match: isCommentToken},
	{open: "<#", close: "#>", style: term.StyleSyntaxComment, match: isCommentToken},
	{open: `"""`, close: `"""`, style: term.StyleSyntaxString, escapes: true, ownsDelim: true, match: isStringToken},
	{open: "'''", close: "'''", style: term.StyleSyntaxString, escapes: true, ownsDelim: true, match: isStringToken},
	{open: "`", close: "`", style: term.StyleSyntaxString, escapes: true, ownsDelim: true, match: isStringToken},
}

// detectRegions takes the raw lexer, not a coalesced one, and coalesces it
// itself: the two views answer different questions.
func detectRegions(raw chroma.Lexer) []region {
	coalesced := chroma.Coalesce(raw)
	var out []region
	for _, c := range regionCandidates {
		probe := c.open + " a\nb " + c.close
		tok, ok := wholeTextToken(coalesced, probe)
		if !ok || !c.match(tok.Type) {
			continue
		}
		if c.ownsDelim && !emitsDelimToken(raw, probe, c.open) {
			continue
		}
		out = append(out, region{
			open:      c.open,
			close:     c.close,
			style:     c.style,
			tokenType: tok.Type,
			escapes:   c.escapes,
		})
	}
	return out
}

// wholeTextToken reports the first token when it covers the whole text, which
// is what "the lexer reads this as one region" means.
func wholeTextToken(lx chroma.Lexer, text string) (chroma.Token, bool) {
	iter, err := lx.Tokenise(nil, text)
	if err != nil {
		return chroma.Token{}, false
	}
	toks := iter.Tokens()
	if len(toks) == 0 {
		return chroma.Token{}, false
	}
	return toks[0], len([]rune(toks[0].Value)) >= len([]rune(text))
}

// emitsDelimToken reports whether the lexer opens the probe by emitting the
// delimiter as a token of its own. Leading empty tokens, such as Python's
// string affix, are skipped.
func emitsDelimToken(lx chroma.Lexer, probe, delim string) bool {
	iter, err := lx.Tokenise(nil, probe)
	if err != nil {
		return false
	}
	for _, tok := range iter.Tokens() {
		if tok.Value == "" {
			continue
		}
		return tok.Value == delim
	}
	return false
}

func isCommentToken(t chroma.TokenType) bool {
	return t == chroma.Comment || t.InSubCategory(chroma.Comment)
}

func isStringToken(t chroma.TokenType) bool {
	return t == chroma.LiteralString || t.InSubCategory(chroma.LiteralString)
}

func mapTokenType(t chroma.TokenType) term.Style {
	switch {
	case t == chroma.KeywordType:
		return term.StyleSyntaxType
	case t == chroma.Keyword || t.InSubCategory(chroma.Keyword):
		return term.StyleSyntaxKeyword
	case t == chroma.Comment || t.InSubCategory(chroma.Comment):
		return term.StyleSyntaxComment
	case t == chroma.String || t.InSubCategory(chroma.String):
		return term.StyleSyntaxString
	case t == chroma.Number || t.InSubCategory(chroma.Number):
		return term.StyleSyntaxNumber
	case t == chroma.Operator || t.InSubCategory(chroma.Operator):
		return term.StyleSyntaxOperator
	case t == chroma.NameFunction || t == chroma.NameFunctionMagic:
		return term.StyleSyntaxFunction
	case t == chroma.NameBuiltin || t == chroma.NameBuiltinPseudo:
		return term.StyleSyntaxBuiltin
	case t == chroma.NameClass || t == chroma.NameDecorator:
		return term.StyleSyntaxType
	case t == chroma.NameTag:
		return term.StyleSyntaxTag
	case t == chroma.NameAttribute:
		return term.StyleSyntaxAttribute
	case t == chroma.NameVariable || t.InSubCategory(chroma.NameVariable):
		return term.StyleSyntaxVariable
	case t == chroma.GenericHeading || t == chroma.GenericSubheading:
		return term.StyleSyntaxKeyword
	case t == chroma.GenericStrong:
		return term.StyleSyntaxType
	case t == chroma.GenericEmph:
		return term.StyleSyntaxString
	case t == chroma.GenericInserted:
		return term.StyleDiffAdded
	case t == chroma.GenericDeleted:
		return term.StyleDiffDeleted
	case t == chroma.Punctuation:
		return term.StyleSyntaxPunctuation
	default:
		return term.StyleDefault
	}
}
