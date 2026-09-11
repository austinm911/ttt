package highlight

import (
	"testing"

	"github.com/eugenioenko/ttt/internal/term"
)

func allStyled(t *testing.T, spans []Span, line string, want term.Style, ctx string) {
	t.Helper()
	for i := range []rune(line) {
		if got := styleAt(spans, i); got != want {
			t.Errorf("%s: rune %d of %q = %v, want %v", ctx, i, line, got, want)
			return
		}
	}
}

func TestTemplateLiteralSpansLines(t *testing.T) {
	lines := []string{
		"const sql = `",
		"  SELECT * FROM todos",
		"  WHERE id = 3",
		"`;",
		"const after = 1;",
	}
	h := New("query.js")
	for _, i := range []int{1, 2} {
		allStyled(t, h.HighlightLineAt(lines, i), lines[i], term.StyleSyntaxString, "inside template")
	}
	if got := styleAt(h.HighlightLineAt(lines, 3), 0); got != term.StyleSyntaxString {
		t.Errorf("closing backtick = %v, want string", got)
	}
	if got := styleAt(h.HighlightLineAt(lines, 4), 0); got != term.StyleSyntaxKeyword {
		t.Errorf("line after the template = %v, want keyword", got)
	}
}

// A template that opens and closes on one line must not leak state: the
// appended closer used to detect an open region is itself an opener.
func TestClosedTemplateDoesNotOpenRegion(t *testing.T) {
	lines := []string{"const a = `x`;", "const b = 2;"}
	h := New("test.js")
	if got := styleAt(h.HighlightLineAt(lines, 1), 0); got != term.StyleSyntaxKeyword {
		t.Errorf("line after a closed template = %v, want keyword", got)
	}
}

func TestGoRawStringSpansLines(t *testing.T) {
	lines := []string{"var q = `", "  select 1", "`", "var n = 2"}
	h := New("main.go")
	allStyled(t, h.HighlightLineAt(lines, 1), lines[1], term.StyleSyntaxString, "inside raw string")
	if got := styleAt(h.HighlightLineAt(lines, 3), 0); got != term.StyleSyntaxKeyword {
		t.Errorf("line after the raw string = %v, want keyword", got)
	}
}

func TestPythonDocstringSpansLines(t *testing.T) {
	lines := []string{`def f():`, `    """`, `    docs`, `    """`, `    return 1`}
	h := New("mod.py")
	allStyled(t, h.HighlightLineAt(lines, 2), lines[2], term.StyleSyntaxString, "inside docstring")
	if got := styleAt(h.HighlightLineAt(lines, 4), 4); got != term.StyleSyntaxKeyword {
		t.Errorf("line after the docstring = %v, want keyword", got)
	}
}

// Chroma coalesces adjacent strings, so `""" a\nb """` reads as one token in
// languages that have no triple-quoted literal. Those must not gain a region.
func TestLanguagesWithoutTripleQuoteRegion(t *testing.T) {
	for _, file := range []string{"main.go", "lib.rs", "app.js"} {
		h := New(file)
		for _, r := range h.regions {
			if r.open == `"""` || r.open == "'''" {
				t.Errorf("%s: unexpected %q region", file, r.open)
			}
		}
	}
}

// Markdown's inline code is not a multi-line region; treating it as one would
// gray out half a document after a stray backtick.
func TestMarkdownHasNoStringRegion(t *testing.T) {
	h := New("notes.md")
	for _, r := range h.regions {
		if r.style == term.StyleSyntaxString {
			t.Errorf("markdown: unexpected string region %q", r.open)
		}
	}
}

// A backslash escapes the closer inside a string region, but not inside a
// comment, where nothing is special.
func TestEscapedCloserInsideStringRegion(t *testing.T) {
	str := region{open: "`", close: "`", style: term.StyleSyntaxString, escapes: true}
	if got := closesAt("a \\` b` rest", str); got != 7 {
		t.Errorf("escaped closer: got %d, want 7", got)
	}
	comment := region{open: "/*", close: "*/", style: term.StyleSyntaxComment}
	if got := closesAt("a \\*/ rest", comment); got != 5 {
		t.Errorf("comment closer: got %d, want 5", got)
	}
}

// Two region kinds in one language: whichever opens first on the line wins.
func TestEarliestRegionWins(t *testing.T) {
	lines := []string{"const a = `x /* y", "still string", "`;"}
	h := New("test.js")
	allStyled(t, h.HighlightLineAt(lines, 1), lines[1], term.StyleSyntaxString, "template beats comment")
}

// Go raw strings have no escapes, so a backslash before the closing backtick
// must not swallow it. Guessing left the region open for the whole buffer.
func TestGoRawStringCloserIsNotEscaped(t *testing.T) {
	lines := []string{"const help = `usage:", `  ttt -d C:\` + "`", "func main() {}"}
	h := New("main.go")
	if got := styleAt(h.HighlightLineAt(lines, 2), 0); got != term.StyleSyntaxKeyword {
		t.Errorf("line after a raw string ending in a backslash = %v, want keyword", got)
	}
}

// JavaScript template literals do have escapes, which is the same probe
// answering the other way.
func TestTemplateLiteralCloserIsEscaped(t *testing.T) {
	h := New("app.js")
	for _, r := range h.regions {
		if r.open == "`" && !r.escapes {
			t.Error("template literal region: escapes = false, want true")
		}
	}
}

// A language has one block comment. Collecting every matching candidate gave
// TypeScript an "<!-- -->" region, and `a <!--b` then commented out the rest
// of the buffer.
func TestOneBlockCommentPerLanguage(t *testing.T) {
	lines := []string{"if (a <!--b) { c() }", "const d = 2"}
	h := New("app.ts")
	comments := 0
	for _, r := range h.regions {
		if r.style == term.StyleSyntaxComment {
			comments++
		}
	}
	if comments != 1 {
		t.Errorf("typescript: %d comment regions, want 1", comments)
	}
	if got := styleAt(h.HighlightLineAt(lines, 1), 0); got != term.StyleSyntaxKeyword {
		t.Errorf("line after `a <!--b` = %v, want keyword", got)
	}
}
