# Signature verifier examples

One small file per language that checks the gateway's signed calls: the signature, the
timestamp skew, a constant-time compare, and idempotency keys. Copy the file for your language
into your backend. The walkthrough is [tutorial 3](../../docs/tutorials/03-accept-signed-calls.md).

| Language | File | Test command (Docker only) |
|---|---|---|
| Go | `go/sigverify.go` (uses `pkg/agentsig`) | `docker compose run --rm sig-go` |
| Python | `python/sigverify.py` (standard library) | `docker compose run --rm sig-python` |
| TypeScript | `typescript/sigverify.ts` (Node 22, no build step) | `docker compose run --rm sig-typescript` |
| C# | `csharp/SigVerify.cs` (.NET 8, no NuGet packages) | `docker compose run --rm sig-csharp` |

Each test checks `vector.json`, the shared test vector (also printed in
[docs/PROTOCOL.md](../../docs/PROTOCOL.md)), then a tampered signature, player, body and path, an
unknown key, a missing header, a timestamp six minutes off, and a replayed idempotency key. A Go
test fails if `vector.json` drifts from `pkg/agentsig`; regenerate it with
`AGENTSIG_WRITE_VECTOR=1 go test ./pkg/agentsig`.
