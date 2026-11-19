package relay

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// DefaultTicketTTL is how long a ticket stays valid.
const DefaultTicketTTL = 60 * time.Second

const maxTicket = 1024

// ErrInvalidTicket covers every reason a ticket is refused.
var ErrInvalidTicket = errors.New("relay: invalid ticket")

// Tickets mints and verifies short lived connect tickets. A ticket is stateless
// (HMAC signed), so any replica verifies it. It is not single use: it is valid until it
// expires, so keep the ttl short.
type Tickets struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

// NewTickets builds a Tickets. A zero ttl uses DefaultTicketTTL, a nil clock time.Now.
func NewTickets(secret []byte, ttl time.Duration, now func() time.Time) *Tickets {
	if ttl <= 0 {
		ttl = DefaultTicketTTL
	}
	if now == nil {
		now = time.Now
	}
	return &Tickets{secret: append([]byte(nil), secret...), ttl: ttl, now: now}
}

type ticketBody struct {
	T   string `json:"t"`
	P   string `json:"p"`
	Exp int64  `json:"exp"`
}

var b64 = base64.RawURLEncoding

func (t *Tickets) sign(payload string) string {
	m := hmac.New(sha256.New, t.secret)
	m.Write([]byte(payload))
	return b64.EncodeToString(m.Sum(nil))
}

// Mint returns a ticket for the player and when it expires.
func (t *Tickets) Mint(tenant, player string) (string, time.Time) {
	exp := t.now().Add(t.ttl)
	raw, _ := json.Marshal(ticketBody{T: tenant, P: player, Exp: exp.Unix()})
	payload := b64.EncodeToString(raw)
	return payload + "." + t.sign(payload), exp
}

// Verify checks a ticket and returns who it was minted for.
func (t *Tickets) Verify(ticket string) (tenant, player string, err error) {
	if len(ticket) == 0 || len(ticket) > maxTicket {
		return "", "", ErrInvalidTicket
	}
	payload, sig, ok := strings.Cut(ticket, ".")
	if !ok {
		return "", "", ErrInvalidTicket
	}
	got, err := b64.DecodeString(sig)
	if err != nil || !hmac.Equal(got, mustDecode(t.sign(payload))) {
		return "", "", ErrInvalidTicket
	}
	raw, err := b64.DecodeString(payload)
	if err != nil {
		return "", "", ErrInvalidTicket
	}
	var b ticketBody
	if json.Unmarshal(raw, &b) != nil || b.T == "" || b.P == "" || !t.now().Before(time.Unix(b.Exp, 0)) {
		return "", "", ErrInvalidTicket
	}
	return b.T, b.P, nil
}

func mustDecode(s string) []byte {
	b, _ := b64.DecodeString(s)
	return b
}
