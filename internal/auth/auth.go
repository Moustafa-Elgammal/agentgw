// Package auth holds the OAuth state of the gateway: registered clients,
// authorization codes, grants and tokens. See docs/DECISIONS.md, Auth (M3).
//
// Tokens are opaque and only their SHA-256 is stored, so a leaked database does
// not leak usable tokens and revoking a grant takes effect on the next lookup.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

// Token lifetimes and limits.
const (
	CodeTTL    = 60 * time.Second
	AccessTTL  = 15 * time.Minute
	RefreshTTL = 30 * 24 * time.Hour
)

// Scope is what a grant allows. It is core.Scope, shared with the gateway pipeline.
type Scope = core.Scope

// The two scopes.
const (
	ScopeCoach    = core.ScopeCoach
	ScopeOperator = core.ScopeOperator
)

// Client is a registered OAuth client. All clients are public (no secret).
type Client struct {
	ID           string
	Name         string
	RedirectURIs []string
	CreatedAt    time.Time
}

// Grant is a player's consent for one client to use one resource at one scope.
type Grant struct {
	ID        string
	Tenant    string
	Player    string
	ClientID  string
	Resource  string // audience: the MCP URL of the game
	Scope     Scope
	CreatedAt time.Time
}

// Code is a pending authorization code. Hash is the SHA-256 of the code handed to the client.
type Code struct {
	Hash        string
	ClientID    string
	RedirectURI string
	Challenge   string // PKCE S256 challenge
	Resource    string
	Scope       Scope
	Tenant      string
	Player      string
	Expires     time.Time
}

// Errors returned by stores.
var (
	// ErrNotFound covers unknown, expired, used, revoked and not-yours. One error on purpose.
	ErrNotFound = errors.New("not found")
	// ErrReuse means a rotated refresh token was presented again. The grant is revoked.
	ErrReuse = errors.New("refresh token reuse detected, grant revoked")
)

// Store keeps OAuth state. Every method must be safe for concurrent use.
type Store interface {
	RegisterClient(ctx context.Context, c Client) error
	// Client returns ErrNotFound for an unknown id.
	Client(ctx context.Context, id string) (Client, error)

	PutCode(ctx context.Context, c Code) error
	// TakeCode returns the code and marks it used. A second take, an unknown hash and
	// an expired code are all ErrNotFound. Exactly one concurrent caller wins.
	TakeCode(ctx context.Context, hash string) (Code, error)

	// UpsertGrant returns the active grant for (tenant, player, client, resource),
	// creating it if needed. An existing one keeps its ID and takes the new scope.
	UpsertGrant(ctx context.Context, g Grant) (Grant, error)
	// Grants lists the player's active grants, oldest first.
	Grants(ctx context.Context, tenant, player string) ([]Grant, error)
	// RevokeGrant revokes one of the player's grants and every token of it.
	// Another player's grant, an unknown id and an already revoked grant are ErrNotFound.
	RevokeGrant(ctx context.Context, tenant, player, grantID string) error

	PutAccess(ctx context.Context, hash, grantID string, expires time.Time) error
	// Access returns the grant behind an access token. Unknown, expired and revoked are ErrNotFound.
	Access(ctx context.Context, hash string) (Grant, error)
	PutRefresh(ctx context.Context, hash, grantID string, expires time.Time) error
	// RotateRefresh retires oldHash and stores newHash for the same grant. Unknown, expired
	// or revoked is ErrNotFound. A token that was already rotated is ErrReuse and revokes the
	// grant. Exactly one concurrent caller can rotate a token.
	RotateRefresh(ctx context.Context, oldHash, newHash string, newExpires time.Time) (Grant, error)
}

// NewToken returns a random token and its storage hash.
func NewToken() (plain, hash string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	plain = base64.RawURLEncoding.EncodeToString(b)
	return plain, HashToken(plain)
}

// HashToken is the form in which tokens and codes are stored.
func HashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// NewID returns a random identifier for clients and grants.
func NewID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
