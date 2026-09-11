package highlight

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"

	"github.com/eugenioenko/ttt/internal/config"
)

// External lexers add languages chroma does not ship. Every *.xml file in a
// config dir's lexers/ folder is a serialised chroma lexer; registering it
// makes each highlight.New caller (editor tabs, diff views, markdown fences,
// readonly tabs) match the language by filename, with no code change per
// language.
var (
	externalOnce sync.Once

	// externalErrors records rejected files so the caller can surface them in
	// the OUTPUT panel instead of failing silently.
	externalErrors []error
)

// ExternalLexerErrors returns the load failures from the external lexer scan.
// Safe to call after any highlight.New; the scan runs once.
func ExternalLexerErrors() []error {
	externalOnce.Do(loadExternalLexers)
	return externalErrors
}

func loadExternalLexers() {
	seen := map[string]bool{}
	for _, dir := range config.ConfigDirs() {
		lexerDir := filepath.Join(dir, "lexers")
		entries, err := os.ReadDir(lexerDir)
		if err != nil {
			continue
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".xml") {
				continue
			}
			if seen[e.Name()] {
				continue
			}
			seen[e.Name()] = true
			names = append(names, e.Name())
		}
		// Deterministic order: registration order decides ties in Match.
		sort.Strings(names)
		for _, name := range names {
			path := filepath.Join(lexerDir, name)
			lexer, err := chroma.NewXMLLexer(os.DirFS(lexerDir), name)
			if err != nil {
				externalErrors = append(externalErrors, fmt.Errorf("%s: %w", path, err))
				continue
			}
			// A registry must be attached before probing: `<using lexer="…"/>`
			// resolves the delegate through it and panics without one.
			lexer.SetRegistry(lexers.GlobalLexerRegistry)
			if err := probeLexer(lexer); err != nil {
				externalErrors = append(externalErrors, fmt.Errorf("%s: %w", path, err))
				continue
			}
			lexers.Register(lexer)
		}
	}
}

// probeLexer rejects a lexer that cannot tokenise at all. Chroma panics on a
// missing `using` delegate or a bad state name, so a broken third-party file
// is caught here rather than at the first keystroke.
func probeLexer(lx chroma.Lexer) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("lexer panicked: %v", r)
		}
	}()
	iter, err := lx.Tokenise(nil, "probe\n")
	if err != nil {
		return err
	}
	if iter == nil {
		return fmt.Errorf("lexer returned no iterator")
	}
	iter.Tokens()
	return nil
}
