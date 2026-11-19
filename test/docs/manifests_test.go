// Package docs checks the documentation. Every code block fenced as "json manifest" or
// "yaml manifest" in the Markdown and MDX sources must pass the real manifest validator, so an
// example in the docs cannot drift from what the gateway accepts.
package docs

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
)

var open = regexp.MustCompile("^\\s*(`{3,}|~{3,})\\s*(json|yaml|yml)\\s+manifest\\b")

type block struct {
	file   string
	line   int // line of the opening fence
	lang   string
	source string
}

// blocks returns the manifest blocks of one file. The closing fence must use the same character
// and at least as many of them as the opening one. Indentation (inside MDX components) is ignored.
func blocks(t *testing.T, path string) []block {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // test reads files under the repository
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	var out []block
	var cur *block
	var fence string
	var buf []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if cur == nil {
			if m := open.FindStringSubmatch(line); m != nil {
				cur = &block{file: path, line: n, lang: m[2]}
				fence = m[1]
				buf = buf[:0]
			}
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, fence[:1]) == "" {
			cur.source = strings.Join(buf, "\n")
			out = append(out, *cur)
			cur = nil
			continue
		}
		buf = append(buf, line)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if cur != nil {
		t.Errorf("%s:%d: manifest block is never closed", path, cur.line)
	}
	return out
}

func TestManifestBlocksValidate(t *testing.T) {
	root := filepath.Join("..", "..")
	var files []string
	_ = filepath.WalkDir(filepath.Join(root, "docs"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Ext(p) == ".md" {
			files = append(files, p)
		}
		return nil
	})
	// The landing page is written by hand. The rest of site/src/content/docs is a generated copy
	// of docs/, so it is not scanned twice.
	files = append(files, filepath.Join(root, "site", "src", "content", "docs", "index.mdx"))
	// README.md is read on GitHub first, so its examples count too.
	files = append(files, filepath.Join(root, "README.md"))

	total := 0
	for _, f := range files {
		for _, b := range blocks(t, f) {
			total++
			name := filepath.ToSlash(strings.TrimPrefix(b.file, root+string(filepath.Separator)))
			t.Run(name+":"+strconv.Itoa(b.line), func(t *testing.T) {
				r := strings.NewReader(b.source)
				var err error
				if b.lang == "json" {
					_, err = manifest.Parse(r)
				} else {
					_, err = manifest.ParseYAML(r)
				}
				if err != nil {
					t.Errorf("the manifest in the docs is not valid:\n%v", err)
				}
			})
		}
	}
	// The landing page carries one. If none is found the scanner is broken, not the docs.
	if total == 0 {
		t.Fatal("no manifest blocks found: expected at least the one on the landing page")
	}
}

// TestScanner pins the fence rules, so a change to blocks() cannot quietly stop checking.
func TestScanner(t *testing.T) {
	src := "# t\n\n```json manifest\n{\"a\":1}\n```\n\n  ````yaml manifest\n  version: 1\n  ```\n  nested\n  ````\n\n```json\nnot a manifest\n```\n"
	p := filepath.Join(t.TempDir(), "x.md")
	if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	got := blocks(t, p)
	if len(got) != 2 {
		t.Fatalf("found %d blocks, want 2: %+v", len(got), got)
	}
	if got[0].lang != "json" || got[0].line != 3 || got[0].source != `{"a":1}` {
		t.Errorf("first block = %+v", got[0])
	}
	if got[1].lang != "yaml" || !strings.Contains(got[1].source, "nested") {
		t.Errorf("a shorter fence must not close a longer one: %+v", got[1])
	}
}
