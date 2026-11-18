Do the M1 spike described in CLAUDE.md and docs/ROADMAP.md. Run everything through Docker.

1. Add `github.com/modelcontextprotocol/go-sdk` at the latest released version (use `docker compose run --rm dev go get ...`) and note the version.
2. In a throwaway `internal/mcpserver/spike_test.go`, prove two things with the official client:
   a. tools can be registered from a raw JSON input schema taken from a manifest
   b. a different tool set can be served per request (per-request server factory)
3. If either does not work as the docs assume, stop and write what you found into docs/DECISIONS.md.
4. Run `docker compose run --rm dev make test`. Report the SDK version and what you learned.
