package studiosig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// LoadKeys reads a key file: a JSON object mapping key id to secret, for example
// {"studio-2026-01":"...","studio-2026-07":"..."}. It must hold at least one key and no
// empty id or secret, so a typo cannot silently lock every studio out on reload.
func LoadKeys(path string) (map[string][]byte, error) {
	b, err := os.ReadFile(path) //nolint:gosec // path comes from the operator's own CLI flag
	if err != nil {
		return nil, err
	}
	var raw map[string]string
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("studiosig: key file %s: %w", path, err)
	}
	if len(raw) == 0 {
		return nil, errors.New("studiosig: key file has no keys")
	}
	keys := make(map[string][]byte, len(raw))
	for id, sec := range raw {
		if id == "" || sec == "" {
			return nil, errors.New("studiosig: key file has an empty key id or secret")
		}
		keys[id] = []byte(sec)
	}
	return keys, nil
}
