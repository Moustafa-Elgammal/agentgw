# Tutorial 3: Make your backend accept signed calls

Verified against agentgw v0.1.0 (see the [changelog](../../CHANGELOG.md)). About 20 minutes.
The script [`03-accept-signed-calls.sh`](03-accept-signed-calls.sh) runs the four language
examples, checks that this page quotes them correctly, and replays a signed request against
the Go and Python pet shops. CI runs that script.

The gateway signs every call it makes to your backend. Your backend must check that signature
before it trusts anything in the request, most of all the player. You will build the check in
Go, Python, TypeScript or C# (pick yours), test it against a published vector, and watch it
refuse a forged request and an idempotent replay.

## Prerequisites

- Docker with Compose v2, and the repository cloned.
- [Tutorial 2](02-describe-your-actions.md) for the pet shop manifest.

## 1. What the gateway sends

Five headers on every request:

| Header | Meaning |
|---|---|
| `X-AG-Key-Id` | which shared secret signed it (so you can rotate) |
| `X-AG-Timestamp` | Unix seconds when it was signed |
| `X-AG-Player` | the player this call is for |
| `X-AG-Idempotency-Key` | unique per call. A repeat must give the same result |
| `X-AG-Signature` | hex HMAC-SHA256 over the request |

The signature covers a string of eight lines joined by `\n`: `v1`, the timestamp, the key id, the
upper-case method, the path **with its query, exactly as sent**, the player, the idempotency
key, and the hex SHA-256 of the body. The full rules are in the [protocol](../PROTOCOL.md).

## 2. Four rules for the verifier

1. **Verify before you trust.** Treat `X-AG-Player` as the player only after the signature
   checks out. The signature covers it, so nobody can swap it.
2. **Check the timestamp.** Refuse anything more than five minutes from your clock, in either
   direction. This is what stops an old captured request from working forever.
3. **Compare in constant time.** A plain `==` on two strings can leak, through timing, how many
   leading characters matched. Use the library's constant-time compare.
4. **Honor the idempotency key.** If the gateway retries a call, or someone replays a captured
   one inside the five minutes, run the action once and return the stored answer. Keep each
   answer for at least ten minutes, because a timestamp up to five minutes in the future is
   accepted. Look the key up only after the signature passed, and keep a failed (5xx) call out of
   the store so a retry can run.

When a request is refused, answer `401` and say nothing about why. Do not tell an attacker which
check failed.

## 3. The test vector

Every implementation must reproduce this request exactly. It is also in
`examples/signing/vector.json` and in the [protocol](../PROTOCOL.md):

```text
key id           dev-1
secret           dev-secret
timestamp        1767268800
method           POST
path and query   /api/v1/pets/feed?src=agent
player           player-7
idempotency key  idem-0001
body             {"pet":"rex","food":"fish","amount":2}
signature        a706e2b8923961ede4fc119424fe81125533df9ca469cdd5ec86663eefbba6de
```

Each example below has a small test that checks this vector, then refuses a tampered
signature, player, body and path, an unknown key, a missing header, and a timestamp six minutes
off in either direction, then proves that a repeated idempotency key runs the action once.

## 4. Pick your language

Each example is one file with no dependencies beyond the language's standard library (Go also
uses `pkg/agentsig` from this repository). Copy the file into your backend. The excerpts show
the heart of each; the files have the rest.

### Go

`examples/signing/go/sigverify.go`. `agentsig.Verify` checks the timestamp skew, the key, and
compares in constant time.

<!-- from: examples/signing/go/sigverify.go -->
```go
		player, err := agentsig.Verify(r, body, m.SecretFor, m.now(), agentsig.DefaultMaxSkew)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized) // say nothing about why
			return
		}
```

<!-- from: examples/signing/go/sigverify.go -->
```go
		key := r.Header.Get(agentsig.HeaderKeyID) + "/" + r.Header.Get(agentsig.HeaderIdempotency)
		entry, first := m.claim(key)
		if !first {
			<-entry.done // a concurrent first call is still running: wait for its answer
```

```bash
docker compose run --rm sig-go
```

### Python

`examples/signing/python/sigverify.py`. Standard library only.

<!-- from: examples/signing/python/sigverify.py -->
```python
        if abs(self.now() - ts) > MAX_SKEW:
            raise Rejected("timestamp outside the allowed skew")
        secret = self.secrets.get(h["x-ag-key-id"])
        if secret is None:
            raise Rejected("unknown key")
        want = sign(secret, ts, h["x-ag-key-id"], method, path, h["x-ag-player"], h["x-ag-idempotency-key"], body)
        # Constant time: a plain == would leak how many leading characters matched.
        if not hmac.compare_digest(want, h["x-ag-signature"]):
            raise Rejected("signature mismatch")
```

```bash
docker compose run --rm sig-python
```

### TypeScript

`examples/signing/typescript/sigverify.ts`. Node's standard library only. The test runs on Node
22 with its built-in type stripping, so there is no build step.

<!-- from: examples/signing/typescript/sigverify.ts -->
```typescript
    if (Math.abs(this.now() - ts) > MAX_SKEW) throw new Rejected("timestamp outside the allowed skew");
    const secret = this.secrets[h["x-ag-key-id"]];
    if (!secret) throw new Rejected("unknown key");
    const want = Buffer.from(sign(secret, ts, h["x-ag-key-id"], method, path, h["x-ag-player"], h["x-ag-idempotency-key"], body));
    const got = Buffer.from(h["x-ag-signature"]);
    // Constant time, and only on equal lengths (timingSafeEqual throws otherwise).
    if (want.length !== got.length || !timingSafeEqual(want, got)) throw new Rejected("signature mismatch");
```

```bash
docker compose run --rm sig-typescript
```

### C# (.NET)

`examples/signing/csharp/SigVerify.cs`. .NET standard library only. The test is a small console
program, so it needs no NuGet packages and runs offline. It was run with the .NET 8 SDK image.

<!-- from: examples/signing/csharp/SigVerify.cs -->
```csharp
        if (Math.Abs(clock() - ts) > MaxSkew) throw new Rejected("timestamp outside the allowed skew");
        if (!secrets.TryGetValue(h["x-ag-key-id"], out var secret)) throw new Rejected("unknown key");
        var want = Encoding.ASCII.GetBytes(Sign(secret, ts, h["x-ag-key-id"], method, path, h["x-ag-player"], h["x-ag-idempotency-key"], body));
        var got = Encoding.ASCII.GetBytes(h["x-ag-signature"]);
        // Constant time. FixedTimeEquals returns false for different lengths.
        if (!CryptographicOperations.FixedTimeEquals(want, got)) throw new Rejected("signature mismatch");
```

```bash
docker compose run --rm sig-csharp
```

All four print passing tests. If you use another language, the signed string and the four
rules above are all you need, and the vector tells you when you got it right.

## 5. Put it in a real backend

`examples/pet-backend-go` and `examples/pet-backend-python` are the pet shop from tutorial 2,
each with the verifier in front of every route. Run one behind a gateway:

```bash
docker compose -f examples/pet-backend-go/compose.yml up --build -d
```

The gateway is on `127.0.0.1:8090` and the backend on `127.0.0.1:9190` (loopback only, so you
can poke it directly). A tool call works end to end because the gateway signs it:

```bash
curl -s -X POST -H 'X-Dev-Player: p1' \
  -d '{"arguments":{"pet":"rex","food":"fish","amount":2}}' \
  http://127.0.0.1:8090/v1/tools/feed_pet/call
```

```json
{"ok":true,"data":{"coins":96,"hunger":60,"pet":"rex"}}
```

Now go around the gateway. An unsigned request to the backend is refused, and so is one that
only pretends to be a player:

```bash
curl -s -o /dev/null -w '%{http_code}\n' -X POST -d '{"pet":"rex","food":"fish","amount":9}' \
  http://127.0.0.1:9190/api/v1/pets/feed
curl -s -o /dev/null -w '%{http_code}\n' -X POST -H 'X-AG-Player: p1' -d '{"pet":"rex","food":"fish","amount":9}' \
  http://127.0.0.1:9190/api/v1/pets/feed
```

```text
401
401
```

### Replay a signed request

The helper `docs/tutorials/sign-request.py` signs a request with the Python verifier's `sign`
function, so you can play the gateway. It runs in a container, so you need no Python on your
machine. Sign one feed request and send it twice:

```bash
body='{"pet":"rex","food":"fish","amount":1}'
headers=$(docker run --rm -v "$PWD/examples/signing/python:/sig:ro" -v "$PWD/docs/tutorials:/t:ro" \
  python:3.13-slim python /t/sign-request.py POST /api/v1/pets/feed p1 replay-1 "$body")
args=(); while IFS= read -r h; do args+=(-H "$h"); done <<< "$headers"

curl -si -X POST "${args[@]}" -d "$body" http://127.0.0.1:9190/api/v1/pets/feed
curl -si -X POST "${args[@]}" -d "$body" http://127.0.0.1:9190/api/v1/pets/feed
```

Both answers carry the same body. The second adds `X-Idempotent-Replay: true`, and the coins
dropped once, not twice. Change one character of `$body` after signing and you get `401`: the
signature covers the body.

The idempotency key `replay-1` is fixed, so on a second run of this block the first call is already
a replay. Change `replay-1` to a new key to see the first call run for real.

Clean up with `docker compose -f examples/pet-backend-go/compose.yml down`. The Python
backend works the same way with `examples/pet-backend-python/compose.yml`.

## What you learned

- Verify the signature and the timestamp before you trust the player, compare in constant time,
  and answer `401` with no detail.
- Store the answer per idempotency key for at least ten minutes and return it on repeats.
- A published test vector lets you check any implementation, in any language.

## Next

[Tutorial 4: link a player](04-link-a-player.md).
