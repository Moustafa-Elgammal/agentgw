package docgen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// The flags page is generated from the flag.* calls in the command sources, so a new flag
// shows up (and a removed one disappears) the next time `make docs-gen` runs.

type flagInfo struct {
	Name, Type, Default, Usage string
}

var flagTypes = map[string]string{
	"String": "string", "Bool": "bool", "Int": "int", "Int64": "int", "Uint": "int",
	"Float64": "number", "Duration": "duration", "Var": "string (repeatable)",
}

var envText = map[string]string{ //nolint:gosec // names of secrets, not secrets
	"AGENTGW_SIGNING_SECRET": "Required. HMAC secret for signing calls to the backend (key id `--key-id`) and for verifying studio requests (key id `--studio-key-id`).",
	"AGENTGW_ADMIN_SECRET":   "Secret of the admin key (`--admin-key-id`) that signs manifest and admin activity calls. Without it, and without `--admin-keys-file`, those endpoints are not mounted. Must differ from the signing secret.",
	"AGENTGW_RELAY_SECRET":   "Secret that signs relay tickets. Without it the relay is off and relay tools answer `client_offline`.",
	"AGENTGW_POSTGRES_URL":   "Default of `--postgres-url`.",
	"AGENTGW_REDIS_ADDR":     "Default of `--redis-addr`.",
	"AGENTGW_MAX_SCOPE":      "Default of `--max-scope`.",
}

var ctlText = map[string]string{
	"version":  "Print the version.",
	"validate": "`gwctl validate <manifest>`: check a manifest (JSON, or YAML by extension) and print the tool count. Exits 1 and lists every problem when it is invalid.",
	"tools":    "`gwctl tools <manifest>`: list tier and name of every tool.",
}

func flagsDoc(root string) (string, error) {
	var b strings.Builder
	b.WriteString(Header)
	b.WriteString("# Flags and environment\n\n")
	b.WriteString("Generated from the flag definitions in `cmd/`. Go flags accept `-name` and `--name`, and `-name=value` or `-name value`. Secrets are read from the environment only, never from flags, so they stay out of process listings.\n\n")

	for _, c := range []struct{ title, path, intro string }{
		{"gateway", "cmd/gateway/main.go", "The gateway server."},
		{"gwbridge", "cmd/gwbridge/main.go", "The stdio to streamable HTTP bridge for desktop MCP clients. It has no environment variables."},
	} {
		fl, envs, _, err := parseCommand(filepath.Join(root, c.path))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", c.title, c.intro)
		rows := make([][]string, 0, len(fl))
		for _, f := range fl {
			def := f.Default
			if def == "" {
				def = "none"
			}
			rows = append(rows, []string{code("--" + f.Name), f.Type, def, f.Usage})
		}
		b.WriteString(mdTable([]string{"Flag", "Type", "Default", "Meaning"}, rows))
		if len(envs) > 0 {
			b.WriteString("\nEnvironment variables:\n\n")
			erows := make([][]string, 0, len(envs))
			for _, e := range envs {
				t, ok := envText[e]
				if !ok {
					return "", fmt.Errorf("environment variable %s has no description in docgen/flags.go", e)
				}
				erows = append(erows, []string{code(e), t})
			}
			b.WriteString(mdTable([]string{"Variable", "Meaning"}, erows))
		}
		b.WriteString("\n")
	}

	_, _, cmds, err := parseCommand(filepath.Join(root, "cmd/gwctl/main.go"))
	if err != nil {
		return "", err
	}
	b.WriteString("## gwctl\n\nThe developer CLI. It has subcommands and no flags.\n\n")
	crows := make([][]string, 0, len(cmds))
	for _, c := range cmds {
		t, ok := ctlText[c]
		if !ok {
			return "", fmt.Errorf("gwctl command %q has no description in docgen/flags.go", c)
		}
		crows = append(crows, []string{code(c), t})
	}
	b.WriteString(mdTable([]string{"Command", "Meaning"}, crows))
	return b.String(), nil
}

// parseCommand reads one main.go and returns its flags (sorted by name), the AGENTGW_*
// environment variables it reads (sorted) and the string cases of its command switch.
func parseCommand(path string) (flags []flagInfo, envs []string, cmds []string, err error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, nil, nil, err
	}
	seenEnv := map[string]bool{}
	var walkErr error
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			sel, ok := n.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			switch {
			case pkg.Name == "flag" && flagTypes[sel.Sel.Name] != "":
				fi, err := flagFromCall(sel.Sel.Name, n)
				if err != nil {
					walkErr = fmt.Errorf("%s: %w", fset.Position(n.Pos()), err)
					return false
				}
				flags = append(flags, fi)
			case pkg.Name == "os" && sel.Sel.Name == "Getenv" && len(n.Args) == 1:
				if s, ok := strLit(n.Args[0]); ok && strings.HasPrefix(s, "AGENTGW_") {
					seenEnv[s] = true
				}
			}
		case *ast.CaseClause:
			for _, e := range n.List {
				if s, ok := strLit(e); ok && !strings.HasPrefix(s, "-") {
					cmds = append(cmds, s)
				}
			}
		}
		return true
	})
	if walkErr != nil {
		return nil, nil, nil, walkErr
	}
	sort.Slice(flags, func(i, j int) bool { return flags[i].Name < flags[j].Name })
	envs = sortedKeys(seenEnv)
	return flags, envs, cmds, nil
}

func flagFromCall(kind string, c *ast.CallExpr) (flagInfo, error) {
	fi := flagInfo{Type: flagTypes[kind]}
	var nameArg, defArg, usageArg ast.Expr
	switch {
	case kind == "Var" && len(c.Args) == 3:
		nameArg, usageArg = c.Args[1], c.Args[2]
	case kind != "Var" && len(c.Args) == 3:
		nameArg, defArg, usageArg = c.Args[0], c.Args[1], c.Args[2]
	default:
		return fi, fmt.Errorf("flag.%s call has an unexpected shape", kind)
	}
	var ok bool
	if fi.Name, ok = strLit(nameArg); !ok {
		return fi, fmt.Errorf("flag name is not a string literal")
	}
	if fi.Usage, ok = strLit(usageArg); !ok {
		return fi, fmt.Errorf("flag --%s: usage is not a string literal", fi.Name)
	}
	if defArg != nil {
		d, err := renderDefault(defArg)
		if err != nil {
			return fi, fmt.Errorf("flag --%s: %w", fi.Name, err)
		}
		fi.Default = d
	}
	return fi, nil
}

func strLit(e ast.Expr) (string, bool) {
	l, ok := e.(*ast.BasicLit)
	if !ok || l.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(l.Value)
	return s, err == nil
}

var durationUnits = map[string]string{"Second": "s", "Minute": "m", "Hour": "h", "Millisecond": "ms"}

// renderDefault prints a flag default the way a reader expects: literals as they are,
// `N*time.Unit` as a duration, and os.Getenv("X") as the variable that supplies it.
func renderDefault(e ast.Expr) (string, error) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if s, ok := strLit(e); ok {
			if s == "" {
				return "", nil
			}
			return "`" + s + "`", nil
		}
		return e.Value, nil
	case *ast.Ident:
		if e.Name == "true" || e.Name == "false" {
			return e.Name, nil
		}
	case *ast.BinaryExpr:
		n, ok1 := e.X.(*ast.BasicLit)
		sel, ok2 := e.Y.(*ast.SelectorExpr)
		if e.Op == token.MUL && ok1 && ok2 {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" && durationUnits[sel.Sel.Name] != "" {
				return n.Value + durationUnits[sel.Sel.Name], nil
			}
		}
	case *ast.CallExpr:
		if sel, ok := e.Fun.(*ast.SelectorExpr); ok && len(e.Args) == 1 {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "os" && sel.Sel.Name == "Getenv" {
				if s, ok := strLit(e.Args[0]); ok {
					return "from `" + s + "`", nil
				}
			}
		}
	}
	return "", fmt.Errorf("cannot render the default value, extend docgen/flags.go")
}

// repoRoot finds the directory holding go.mod, walking up from dir.
func repoRoot(dir string) (string, error) {
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}
