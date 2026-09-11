package highlight

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/eugenioenko/ttt/internal/config"
	"github.com/eugenioenko/ttt/internal/term"
)

const miniLexer = `<lexer>
  <config>
    <name>Mini</name>
    <alias>mini</alias>
    <filename>*.mini</filename>
    <dot_all>true</dot_all>
    <ensure_nl>true</ensure_nl>
  </config>
  <rules>
    <state name="root">
      <rule pattern="/\*.*?\*/"><token type="CommentMultiline" /></rule>
      <rule pattern="\bwhen\b"><token type="Keyword" /></rule>
      <rule pattern="."><token type="Text" /></rule>
    </state>
  </rules>
</lexer>`

// writeLexerDir points config lookup at a fresh dir holding the given
// lexers/<name> files, and rearms the one-shot external scan.
func writeLexerDir(t *testing.T, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "lexers"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, "lexers", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prev := config.OverrideConfigDir
	config.OverrideConfigDir = dir
	t.Cleanup(func() {
		config.OverrideConfigDir = prev
		externalOnce = sync.Once{}
		externalErrors = nil
	})
	externalOnce = sync.Once{}
	externalErrors = nil
}

func TestExternalLexerHighlightsUnknownExtension(t *testing.T) {
	writeLexerDir(t, map[string]string{"mini.xml": miniLexer})

	h := New("example.mini")
	if h == nil {
		t.Fatal("no highlighter for .mini after installing an external lexer")
	}
	if got := h.Language(); got != "Mini" {
		t.Fatalf("language = %q, want Mini", got)
	}

	spans := h.HighlightLine("when x")
	if len(spans) == 0 || spans[0].Style != term.StyleSyntaxKeyword {
		t.Fatalf("spans = %+v, want a keyword span at the start", spans)
	}
}

// A file dropped in by hand can be malformed. The editor must keep running,
// report why, and fall back to no highlighting for that extension.
func TestExternalLexerRejectsMalformedFile(t *testing.T) {
	writeLexerDir(t, map[string]string{"broken.xml": "<lexer><config><name>Broken</name>"})

	if h := New("example.broken"); h != nil {
		t.Fatalf("got a highlighter from a malformed lexer: %q", h.Language())
	}
	if errs := ExternalLexerErrors(); len(errs) == 0 {
		t.Fatal("malformed lexer produced no error")
	}
}

// Cross-line block comment state depends on the lexer emitting one token for
// a comment spanning newlines; an external lexer must get that same support.
func TestExternalLexerCarriesBlockCommentState(t *testing.T) {
	writeLexerDir(t, map[string]string{"mini.xml": miniLexer})

	h := New("example.mini")
	if h == nil {
		t.Fatal("no highlighter for .mini")
	}
	lines := []string{"/* open", "still inside", "close */ when"}
	spans := h.HighlightLineAt(lines, 1)
	if len(spans) != 1 || spans[0].Style != term.StyleSyntaxComment {
		t.Fatalf("spans = %+v, want the whole line as a comment", spans)
	}
}
