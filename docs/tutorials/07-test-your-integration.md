# Tutorial 7: Test your integration

Verified against agentgw v0.1.0 (see the [changelog](../../CHANGELOG.md)). About 15 minutes.
The script [`07-test-your-integration.sh`](07-test-your-integration.sh) runs every command below
against a throwaway Docker project, and CI runs that script.

Before real players or real agents touch your game, you want proof that the two halves of your
integration behave: the **gateway** serves the tools you described, and your **backend** accepts
only calls signed by the gateway. You will run black-box checks against both, break each on
purpose to see a failure, run the gateway's own conformance suite, and finish with a load smoke
test.

## Prerequisites

- Docker with Compose v2, and the repository checked out.
- Stop any other agentgw stack first. The tutorial uses the demo stack, so it needs
  `127.0.0.1:8080` and `127.0.0.1:9091`.
- Tutorial 3 helps: it is about the backend side of signing.

## What exists, and what it covers

There are three things, and they test different layers.

| What | Tests | Needs |
|---|---|---|
| **External gateway checks** (`TestExternalGateway`) | a gateway you run, through its MCP endpoint, against the manifest you give | a URL and an identity |
| **External backend checks** (`TestExternalBackend`) | your backend's handling of the gateway's signed requests | its URL, key id, secret and one GET path |
| **The gateway's own suite** (the rest of `test/conformance`) | the gateway **code**, in process, with a fake backend: rate limits, caps, reservations, audit hashes, kill switches, confirmations, OAuth | nothing; it is for people changing the gateway |

The in-process suite does not look at your deployment. The external checks do, but they are
**black box**: they can only see what an agent or a backend sees. They cannot tell, for example,
that a bad call never reached your backend or how your policy store is configured. They check
shape and behavior, not every rule. Treat them as a smoke test you can run in CI against staging.

What the gateway checks do:

1. The tool list matches the manifest: same names, descriptions and `readOnlyHint`. Sensitive
   tools are not listed.
2. Every input schema is a closed object with the manifest's required, `enum`, `min` and `max`.
3. An unknown tool is a tool error, not a transport failure.
4. For each tool, calls with an unknown argument and with a wrong type are denied with
   `invalid_arguments`. These are refused before the backend, so they are safe to run against a
   real game.
5. Results have the documented shape (`ok`, and `code` plus `message` on a denial).
6. Read tools that need no arguments run and succeed. That proves the gateway reaches your
   backend and your backend accepts its signature.
7. A request with no credentials gets `401`.

What the backend checks do, with a GET path you choose:

1. A correctly signed request is accepted (`2xx`).
2. These are each refused (`400`, `401` or `403`): no signature, the wrong secret, an old
   timestamp, a timestamp in the future, a signed query that was changed in transit, a player
   header that differs from the signed one, and no player header.

They do not test replays of one idempotency key, because what a backend must do there depends on
the action. See [tutorial 3](03-accept-signed-calls.md).

## 1. Start the demo

```bash
dc() { docker compose "$@"; }
dc up -d --build gateway
dc build --quiet dev
```

The demo gateway runs with `--dev-auth`, so the checks identify themselves with a player id. The
demo backend, `city-backend`, runs behind it.

## 2. Check the gateway from outside

```bash
dc run --rm -T \
  -e AGENTGW_CONFORMANCE_URL=http://gateway:8080/mcp \
  -e AGENTGW_CONFORMANCE_PLAYER=tutorial-7 \
  dev go test -count=1 -v -run 'TestExternalGateway$' ./test/conformance
```

```text
--- PASS: TestExternalGateway (0.07s)
    --- PASS: TestExternalGateway/tools_list_matches_the_manifest (0.02s)
    --- PASS: TestExternalGateway/input_schemas_carry_the_manifest_limits (0.00s)
    --- PASS: TestExternalGateway/unknown_tool_is_a_tool_error (0.00s)
    --- PASS: TestExternalGateway/bad_arguments_are_denied_with_invalid_arguments (0.03s)
    --- PASS: TestExternalGateway/results_have_the_documented_shape (0.00s)
    --- PASS: TestExternalGateway/read_tools_reach_the_backend_and_succeed (0.01s)
    --- PASS: TestExternalGateway/a_request_with_no_credentials_is_refused (0.00s)
PASS
```

(The first run also prints `go: downloading ...` lines.) The test reads the manifest the gateway
serves from `examples/city/manifest.json`. Use a test player id. The bad-argument calls count
against that player's rate limit like any other attempt.

## 3. Check the backend from outside

```bash
dc run --rm -T \
  -e AGENTGW_CONFORMANCE_BACKEND_URL=http://city-backend:9090 \
  -e AGENTGW_CONFORMANCE_BACKEND_KEY_ID=dev-1 \
  -e AGENTGW_CONFORMANCE_BACKEND_SECRET=dev-secret \
  -e AGENTGW_CONFORMANCE_BACKEND_READ_PATH=/api/v1/city \
  dev go test -count=1 -v -run 'TestExternalBackend$' ./test/conformance
```

```text
--- PASS: TestExternalBackend (0.01s)
    --- PASS: TestExternalBackend/a_correctly_signed_request_is_accepted (0.00s)
    --- PASS: TestExternalBackend/a_request_with_no_signature_is_refused (0.00s)
    --- PASS: TestExternalBackend/a_request_signed_with_the_wrong_secret_is_refused (0.00s)
    --- PASS: TestExternalBackend/a_request_with_an_old_timestamp_is_refused (0.00s)
    --- PASS: TestExternalBackend/a_request_with_a_timestamp_far_in_the_future_is_refused (0.00s)
    --- PASS: TestExternalBackend/a_request_whose_signed_query_was_changed_in_transit_is_refused (0.00s)
    --- PASS: TestExternalBackend/a_request_for_a_different_player_than_the_one_signed_is_refused (0.00s)
    --- PASS: TestExternalBackend/a_request_with_no_player_header_is_refused (0.00s)
PASS
```

The key id and secret are what the gateway signs with (`--key-id` and `AGENTGW_SIGNING_SECRET`).
`dev-secret` is the demo placeholder. For your backend, give the path of any GET it serves that
needs no arguments.

## 4. See a failure: a wrong secret

A suite you have never seen fail proves little. Give the backend check a secret the backend does
not know:

```bash
dc run --rm -T \
  -e AGENTGW_CONFORMANCE_BACKEND_URL=http://city-backend:9090 \
  -e AGENTGW_CONFORMANCE_BACKEND_SECRET=a-secret-the-backend-does-not-know \
  -e AGENTGW_CONFORMANCE_BACKEND_READ_PATH=/api/v1/city \
  dev go test -count=1 -run 'TestExternalBackend$' ./test/conformance
```

```text
--- FAIL: TestExternalBackend (0.00s)
    --- FAIL: TestExternalBackend/a_correctly_signed_request_is_accepted (0.00s)
        external_test.go:...: the backend answered 401 to a valid request for /api/v1/city, want 2xx (wrong key, secret or path?)
FAIL
```

The command exits with status 1, so CI fails. The message points at the usual causes. The other
refusal checks still pass here, because a backend that refuses everything looks safe. That is why
the first check must pass.

## 5. See a failure: the wrong manifest

Tell the gateway check it should expect the two-tool manifest `examples/tutorials/city-lite.json`,
while the gateway serves four tools. The path is relative to `test/conformance`:

```bash
dc run --rm -T \
  -e AGENTGW_CONFORMANCE_URL=http://gateway:8080/mcp \
  -e AGENTGW_CONFORMANCE_PLAYER=tutorial-7b \
  -e AGENTGW_CONFORMANCE_MANIFEST=../../examples/tutorials/city-lite.json \
  dev go test -count=1 -run 'TestExternalGateway$' ./test/conformance
```

```text
--- FAIL: TestExternalGateway (0.02s)
    --- FAIL: TestExternalGateway/tools_list_matches_the_manifest (0.00s)
        external_test.go:...: unexpected tool build_structure (a sensitive tool, or one the manifest does not have)
            unexpected tool sell_resource (a sensitive tool, or one the manifest does not have)
FAIL
```

This is how you catch a deploy that serves a different manifest than the one in your repository.

## 6. Point it at your own game

Set the same variables for your deployment. Run these from the repository checkout, which holds
the tests.

| Variable | Meaning |
|---|---|
| `AGENTGW_CONFORMANCE_URL` | the MCP endpoint, for example `https://agentgw.example.com/mcp` |
| `AGENTGW_CONFORMANCE_PLAYER` | a test player id, sent as `X-Dev-Player`. Only for a gateway started with `--dev-auth`, so for local or CI stacks |
| `AGENTGW_CONFORMANCE_TOKEN` | a bearer token from a real link (tutorial 4), for a gateway in OAuth mode. Use it instead of the player |
| `AGENTGW_CONFORMANCE_MANIFEST` | the manifest the gateway serves, as a path relative to `test/conformance` |
| `AGENTGW_CONFORMANCE_SCOPE` | `coach` if the token is a coach grant, so only read tools are expected |
| `AGENTGW_CONFORMANCE_BACKEND_URL`, `_KEY_ID`, `_SECRET`, `_READ_PATH` | the backend checks, as above |

With OAuth you can get a token for a test player the way tutorial 4 does. A token from a real
production player is not a good choice, because the checks call tools for that player.

A CI job for a staging environment is then one `docker run` or one `go test` line per target.
Without the variables the external tests skip, so `go test ./...` stays green on a laptop.

## 7. The gateway's own suite

If you change the gateway itself, run the in-process suite. It needs no variables:

```bash
dc run --rm -T dev go test -race -count=1 ./test/conformance
```

```text
ok  	github.com/Moustafa-Elgammal/agentgw/test/conformance	2.320s
```

It drives the example manifest with the official MCP client against a fake backend that verifies
signatures, and checks rules such as: invalid attempts count against the rate limit, a failed
backend call releases its reservation and starts no cooldown, the daily cap holds under
concurrency, players are isolated, audit stores hashes, kill switches work, a confirmed tool needs
the player, and the OAuth flow, refresh rotation and revocation behave. The external checks are
also run against this in-process gateway, so the checks themselves are tested.

## 8. A load smoke test

`test/load` drives a running gateway (with `--dev-auth`) and exits 1 if a threshold is missed:

```bash
dc run --rm -T load --url http://gateway:8080 --duration 15s --rate 100 \
  --max-error-rate 0.01 --max-p99 500ms
```

```text
calls      1499 in 15.001s (99.9/s)
ok         1260
errors     0 (0.00%)
latency    p50 1.927625ms  p95 5.584458ms  p99 11.21825ms  max 62.127417ms
denials    239
  backend_rejected         239
PASS
```

`errors` counts transport failures and 5xx answers. `denials` are normal answers with a code. In
the demo they are `backend_rejected`, because the demo backend has only so much food to sell. A
longer soak is in the [runbook](../RUNBOOK.md#load-and-soak). Run load tests against staging, not
against a live game: they make real calls.

## Clean up

```bash
dc down -v
```

## What you learned

- Black-box checks exist for the gateway and for your backend. They need only URLs, an identity,
  and for the backend a key and a path.
- A check you have seen fail is worth more than one you have only seen pass. Break each on
  purpose, once.
- The in-process suite tests gateway code and is for contributors. The external checks test your
  deployment, but only what an agent or backend can see.
- A load smoke test belongs in CI for staging, with thresholds.

## Next

You have onboarded a toy game end to end. Continue with the [runbook](../RUNBOOK.md) for
operating it, the [reference](../reference/manifest.md) for every field, and
[CONTRIBUTING](../../CONTRIBUTING.md) if you want to improve the gateway.
