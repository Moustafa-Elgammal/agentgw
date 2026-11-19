package relay

// Routes lists the routes Handler and MintHandler serve. docs/openapi/studio.yaml is checked
// against it.
var Routes = []string{
	"GET /v1/relay/connect",
	"POST /v1/studio/relay/tickets",
}
