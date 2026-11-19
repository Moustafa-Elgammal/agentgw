package auth

// Routes lists the public routes Handler and StudioHandler serve (CORS preflight OPTIONS
// routes left out). docs/openapi/studio.yaml is checked against it.
var Routes = []string{
	"GET /.well-known/oauth-authorization-server",
	"POST /register",
	"GET /authorize",
	"POST /authorize",
	"POST /authorize/oidc",
	"GET /authorize/oidc/callback",
	"POST /token",
	"POST /v1/studio/link-tickets",
	"GET /v1/studio/grants",
	"DELETE /v1/studio/grants/{id}",
	"DELETE /v1/studio/grants",
}
