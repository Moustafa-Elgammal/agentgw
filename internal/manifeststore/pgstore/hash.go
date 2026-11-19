package pgstore

import (
	"crypto/sha256"
	"encoding/hex"
)

func hashHex(doc []byte) string {
	s := sha256.Sum256(doc)
	return hex.EncodeToString(s[:])
}
