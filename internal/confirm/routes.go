package confirm

// Routes lists the routes StudioHandler serves. docs/openapi/studio.yaml is checked against it.
var Routes = []string{
	"GET /v1/studio/confirmations",
	"POST /v1/studio/confirmations/{id}/approve",
	"POST /v1/studio/confirmations/{id}/deny",
}
