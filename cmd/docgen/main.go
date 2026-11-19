// Command docgen writes the generated reference pages under docs/. Run it from the repository
// root with `make docs-gen`. A test fails when a committed page is out of date.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Moustafa-Elgammal/agentgw/internal/docgen"
)

func main() {
	root, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	files, err := docgen.Files(root)
	if err != nil {
		fail(err)
	}
	for path, data := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			fail(err)
		}
		if err := os.WriteFile(full, data, 0o644); err != nil { //nolint:gosec // docs are public files
			fail(err)
		}
		fmt.Println("wrote", path)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "docgen:", err)
	os.Exit(1)
}
