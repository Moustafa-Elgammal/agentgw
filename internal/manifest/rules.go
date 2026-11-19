package manifest

import "sort"

// NamePattern is the regular expression a tool name must match.
func NamePattern() string { return nameRe.String() }

// ParamTypes lists the allowed param types, sorted.
func ParamTypes() []string { return keys(validTypes) }

// HTTPMethods lists the allowed http.method values, sorted.
func HTTPMethods() []string { return keys(validMethod) }

// MaxYAMLBytes is the largest YAML document ParseYAML reads.
const MaxYAMLBytes = maxManifest

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
