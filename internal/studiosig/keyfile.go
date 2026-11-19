package studiosig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
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

// KeyPair is a single signing key: the id sent in X-AG-Key-Id and its secret.
type KeyPair struct {
	ID     string
	Secret []byte
}

// LoadKeyPair reads a file holding one key, {"id":"...","secret":"..."}. It is the key the
// gateway signs its backend calls with. An empty id or secret is an error, so a typo never
// replaces a working key.
func LoadKeyPair(path string) (KeyPair, error) {
	b, err := os.ReadFile(path) //nolint:gosec // path comes from the operator's own CLI flag
	if err != nil {
		return KeyPair{}, err
	}
	var raw struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return KeyPair{}, fmt.Errorf("studiosig: key file %s: %w", path, err)
	}
	if raw.ID == "" || raw.Secret == "" {
		return KeyPair{}, errors.New("studiosig: key file needs a non-empty id and secret")
	}
	return KeyPair{ID: raw.ID, Secret: []byte(raw.Secret)}, nil
}

// DiffIDs returns the key ids present in next but not in prev (added) and in prev but not in
// next (removed), each sorted. Secrets are never looked at, so the result is safe to log.
func DiffIDs(prev, next map[string]struct{}) (added, removed []string) {
	for id := range next {
		if _, ok := prev[id]; !ok {
			added = append(added, id)
		}
	}
	for id := range prev {
		if _, ok := next[id]; !ok {
			removed = append(removed, id)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}
