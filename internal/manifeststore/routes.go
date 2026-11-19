package manifeststore

// Routes lists the routes Handler serves. docs/openapi/studio.yaml is checked against it.
var Routes = []string{
	"PUT /v1/studio/manifest",
	"GET /v1/studio/manifest",
	"GET /v1/studio/manifest/versions",
	"POST /v1/studio/manifest/versions/{id}/activate",
}
