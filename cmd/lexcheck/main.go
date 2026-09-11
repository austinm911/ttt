// Command lexcheck exercises ttt's real highlighter against a source file, so
// an external chroma lexer can be verified without opening the editor. It
// lexes exactly the way the editor does: one line at a time through
// highlight.HighlightLineAt, with block-comment state carried down the buffer.
//
//	lexcheck dump   <file>                 print every span, line by line
//	lexcheck check  <file> [<assertfile>]  verify <file>.assert expectations
//
// Assertion lines are `<line>|<substring>[#<occurrence>]|<style>`, where style
// is one of the names in styleNames below and `none` means "no color".
package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/eugenioenko/ttt/internal/highlight"
	"github.com/eugenioenko/ttt/internal/term"
)

var styleNames = map[term.Style]string{
	term.StyleDefault:           "none",
	term.StyleSyntaxComment:     "comment",
	term.StyleSyntaxString:      "string",
	term.StyleSyntaxKeyword:     "keyword",
	term.StyleSyntaxNumber:      "number",
	term.StyleSyntaxOperator:    "operator",
	term.StyleSyntaxFunction:    "function",
	term.StyleSyntaxType:        "type",
	term.StyleSyntaxBuiltin:     "builtin",
	term.StyleSyntaxVariable:    "variable",
	term.StyleSyntaxPunctuation: "punct",
	term.StyleSyntaxTag:         "tag",
	term.StyleSyntaxAttribute:   "attribute",
}

func styleName(s term.Style) string {
	if n, ok := styleNames[s]; ok {
		return n
	}
	return fmt.Sprintf("style(%d)", int(s))
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: lexcheck dump|check <file> [<assertfile>]")
		os.Exit(2)
	}
	mode, path := os.Args[1], os.Args[2]

	for _, err := range highlight.ExternalLexerErrors() {
		fmt.Fprintf(os.Stderr, "external lexer rejected: %v\n", err)
	}

	lines, err := readLines(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	h := highlight.New(path)
	if h == nil {
		fmt.Fprintf(os.Stderr, "FAIL: no lexer matched %s (is the lexer XML installed in a config lexers/ dir?)\n", path)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "language: %s\n", h.Language())

	switch mode {
	case "dump":
		dump(h, lines, nil)
	case "check":
		assertPath := path + ".assert"
		if len(os.Args) > 3 {
			assertPath = os.Args[3]
		}
		os.Exit(check(h, lines, path, assertPath))
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q\n", mode)
		os.Exit(2)
	}
}

// dump prints spans for the given 1-based line numbers, or all lines when
// only is nil.
func dump(h *highlight.Highlighter, lines []string, only map[int]bool) {
	for i, line := range lines {
		if only != nil && !only[i+1] {
			continue
		}
		spans := h.HighlightLineAt(lines, i)
		runes := []rune(line)
		fmt.Printf("%4d| %s\n", i+1, line)
		pos := 0
		for _, s := range spans {
			if s.Start > pos {
				fmt.Printf("      %-10s %q\n", "none", string(runes[pos:s.Start]))
			}
			fmt.Printf("      %-10s %q\n", styleName(s.Style), string(runes[s.Start:s.End]))
			pos = s.End
		}
		if pos < len(runes) {
			fmt.Printf("      %-10s %q\n", "none", string(runes[pos:]))
		}
	}
}

type assertion struct {
	src        string
	line       int
	needle     string
	occurrence int
	want       string
}

func check(h *highlight.Highlighter, lines []string, path, assertPath string) int {
	asserts, err := readAssertions(assertPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	failed := map[int]bool{}
	pass := 0
	for _, a := range asserts {
		if a.line < 1 || a.line > len(lines) {
			fmt.Printf("FAIL %s: line %d out of range for %s\n", a.src, a.line, path)
			failed[a.line] = true
			continue
		}
		text := lines[a.line-1]
		col, ok := runeIndex(text, a.needle, a.occurrence)
		if !ok {
			fmt.Printf("FAIL %s: %q occurrence %d not found on line %d: %s\n",
				a.src, a.needle, a.occurrence, a.line, text)
			failed[a.line] = true
			continue
		}
		got := styleAt(h.HighlightLineAt(lines, a.line-1), col)
		if got != a.want {
			fmt.Printf("FAIL %s: %q on line %d: want %s, got %s\n",
				a.src, a.needle, a.line, a.want, got)
			failed[a.line] = true
			continue
		}
		pass++
	}
	if len(failed) > 0 {
		fmt.Printf("\n--- spans for failing lines ---\n")
		dump(h, lines, failed)
		fmt.Printf("\n%d passed, %d failed\n", pass, len(asserts)-pass)
		return 1
	}
	fmt.Printf("%d passed, 0 failed\n", pass)
	return 0
}

func styleAt(spans []highlight.Span, col int) string {
	for _, s := range spans {
		if col >= s.Start && col < s.End {
			return styleName(s.Style)
		}
	}
	return "none"
}

// runeIndex returns the 0-based rune column where the nth (1-based)
// occurrence of needle starts.
func runeIndex(text, needle string, occurrence int) (int, bool) {
	from := 0
	for n := 1; ; n++ {
		idx := strings.Index(text[from:], needle)
		if idx < 0 {
			return 0, false
		}
		byteAt := from + idx
		if n == occurrence {
			return len([]rune(text[:byteAt])), true
		}
		from = byteAt + len(needle)
	}
}

func readAssertions(path string) ([]assertion, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []assertion
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		parts := strings.Split(raw, "|")
		if len(parts) != 3 {
			return nil, fmt.Errorf("%s:%d: want `line|substring|style`, got %q", path, n, raw)
		}
		lineNo, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			return nil, fmt.Errorf("%s:%d: bad line number: %w", path, n, err)
		}
		needle, occurrence := parts[1], 1
		if i := strings.LastIndex(needle, "#"); i > 0 {
			if k, err := strconv.Atoi(needle[i+1:]); err == nil {
				needle, occurrence = needle[:i], k
			}
		}
		out = append(out, assertion{
			src:        fmt.Sprintf("%s:%d", path, n),
			line:       lineNo,
			needle:     needle,
			occurrence: occurrence,
			want:       strings.TrimSpace(parts[2]),
		})
	}
	return out, sc.Err()
}

func readLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSuffix(string(data), "\n")
	return strings.Split(text, "\n"), nil
}
