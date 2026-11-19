package agentsig

import (
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// vectorFile is the shared test vector every language example checks. It is also printed
// in docs/PROTOCOL.md. Set AGENTSIG_WRITE_VECTOR=1 to regenerate it.
const vectorFile = "../../examples/signing/vector.json"

type vector struct {
	KeyID        string `json:"key_id"`
	Secret       string `json:"secret"`
	Timestamp    int64  `json:"timestamp"`
	Method       string `json:"method"`
	Path         string `json:"path"`
	Player       string `json:"player"`
	Idempotency  string `json:"idempotency_key"`
	Body         string `json:"body"`
	BodySHA256   string `json:"body_sha256"`
	SignedString string `json:"signed_string"`
	Signature    string `json:"signature"`
}

func TestSharedVector(t *testing.T) {
	v := vector{
		KeyID: "dev-1", Secret: "dev-secret", Timestamp: 1767268800, Method: "POST",
		Path: "/api/v1/pets/feed?src=agent", Player: "player-7", Idempotency: "idem-0001",
		Body: `{"pet":"rex","food":"fish","amount":2}`,
	}
	req := httptest.NewRequest(v.Method, v.Path, strings.NewReader(v.Body))
	Sign(req, v.KeyID, []byte(v.Secret), v.Player, v.Idempotency, []byte(v.Body), time.Unix(v.Timestamp, 0))
	v.Signature = req.Header.Get(HeaderSignature)
	v.SignedString = canonical(v.Timestamp, v.KeyID, v.Method, v.Path, v.Player, v.Idempotency, []byte(v.Body))
	lines := strings.Split(v.SignedString, "\n")
	v.BodySHA256 = lines[len(lines)-1]
	if _, err := hex.DecodeString(v.BodySHA256); err != nil {
		t.Fatal(err)
	}
	want, _ := json.MarshalIndent(v, "", "  ") //nolint:gosec // a public test vector with a placeholder secret
	want = append(want, '\n')

	if os.Getenv("AGENTSIG_WRITE_VECTOR") == "1" {
		if err := os.WriteFile(vectorFile, want, 0o644); err != nil { //nolint:gosec // a public test fixture
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(vectorFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s is out of date; run AGENTSIG_WRITE_VECTOR=1 go test ./pkg/agentsig", vectorFile)
	}
}
