package switchapi

// Path is the one route. Handler spells the patterns out so the drift tests can find them.
// docs/openapi/studio.yaml is checked against Routes.
const Path = "/v1/studio/switches"

// Routes lists the routes Handler serves.
var Routes = []string{"GET " + Path, "PUT " + Path, "DELETE " + Path}
