package main

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// keyset answers "which secret goes with this key id" for the signature check. It knows the
// bootstrap key (id and secret from the environment, when set) and, optionally, a JSON file
// {"id":"secret", ...}. The file is re-read when it changes, so a studio can rotate the key the
// gateway signs with: list both keys, switch the gateway, then drop the old one, with no restart.
type keyset struct {
	bootID     string
	bootSecret []byte
	file       string

	mu    sync.Mutex
	mtime time.Time
	size  int64
	keys  map[string][]byte
}

func newKeyset(bootID, bootSecret, file string) *keyset {
	k := &keyset{bootID: bootID, bootSecret: []byte(bootSecret), file: file}
	k.reload()
	return k
}

// reload re-reads the file when its time or size changed. A file that cannot be read or parsed
// keeps the keys already loaded, so a half-written file never opens or closes the door by itself.
func (k *keyset) reload() {
	if k.file == "" {
		return
	}
	st, err := os.Stat(k.file)
	if err != nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.keys != nil && st.ModTime().Equal(k.mtime) && st.Size() == k.size {
		return
	}
	b, err := os.ReadFile(k.file)
	if err != nil {
		return
	}
	var raw map[string]string
	if json.Unmarshal(b, &raw) != nil {
		return
	}
	keys := make(map[string][]byte, len(raw))
	for id, sec := range raw {
		if id != "" && sec != "" {
			keys[id] = []byte(sec)
		}
	}
	k.keys, k.mtime, k.size = keys, st.ModTime(), st.Size()
}

// secretFor is the lookup agentsig.Verify wants. It returns nil for an unknown key.
func (k *keyset) secretFor(id string) []byte {
	k.reload()
	k.mu.Lock()
	defer k.mu.Unlock()
	if s, ok := k.keys[id]; ok {
		return s
	}
	if id == k.bootID && len(k.bootSecret) > 0 {
		return k.bootSecret
	}
	return nil
}
