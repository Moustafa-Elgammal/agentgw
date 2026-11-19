// Command gwctl is the developer CLI: validate and inspect manifests.
package main

import (
	"fmt"
	"os"

	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
)

// version is set at release time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version", "--version", "-version":
		fmt.Println(version)
	case "validate":
		if len(os.Args) != 3 {
			usage()
			os.Exit(2)
		}
		m, err := manifest.LoadFile(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("ok: %d tools\n", len(m.Tools))
	case "tools":
		if len(os.Args) != 3 {
			usage()
			os.Exit(2)
		}
		m, err := manifest.LoadFile(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		for _, t := range m.Tools {
			fmt.Printf("%-10s %s\n", t.Tier, t.Name)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: gwctl version | validate <manifest.json> | tools <manifest.json>")
}
