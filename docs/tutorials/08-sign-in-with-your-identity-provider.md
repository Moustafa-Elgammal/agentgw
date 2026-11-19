# Tutorial 8: Sign in with your identity provider

Verified against agentgw v0.1.0 (see the [changelog](../../CHANGELOG.md)). About 20 minutes.
The script [`08-sign-in-with-your-identity-provider.sh`](08-sign-in-with-your-identity-provider.sh)
runs every command below against a throwaway Docker project, and CI runs that script.

In [tutorial 4](04-link-a-player.md) your game's backend minted a **link ticket** and the player
typed it on the consent page. That works with any game, but it asks the player to copy a code.
If your game already has a login system that speaks **OpenID Connect** (OIDC), the player can
press one button instead and sign in with the account they already have.

You will run the gateway with OIDC sign-in turned on, use [Dex](https://dexidp.io) as a stand-in
for your own identity provider, and walk a login by hand with `curl`: from the consent page to the
identity provider's login form, back to the gateway, and on to a token for an agent.

## Prerequisites

- Docker with Compose v2, plus `curl` and `openssl` on your machine.
- The repository: `git clone https://github.com/Moustafa-Elgammal/agentgw && cd agentgw`
- Tutorial 4 helps, because it explains consent, scopes and grants.
- Stop any other agentgw stack first. This one uses `127.0.0.1:8080`, `127.0.0.1:9091` and
  `127.0.0.1:15556`. (The script uses other ports for the gateway, so it can run next to a stack
  you already have.)

## How the sign-in works

```text
agent          player's browser            gateway                 your identity provider
  |  open consent page  |                      |                              |
  |-------------------->|  GET /authorize ---->|                              |
  |                     |<--- page with "Sign in with your game account" ----|
  |                     |  press the button -->|                              |
  |                     |<-- redirect, with state, nonce and PKCE ----------->|
  |                     |  log in -------------------------------------------->|
  |                     |<-- redirect back to the gateway, with a code --------|
  |                     |  GET /authorize/oidc/callback --------------------->|
  |                     |                      | exchange the code, check the ID token
  |<-- the agent's own code, for the player the provider vouched for ---------|
```

The gateway never sees the player's password. It only learns **who the identity provider says the
player is**, as a claim inside a signed ID token. It checks that token itself:

- the **signature**, against the provider's published keys (they are fetched and cached, and a
  rotated key is picked up without a restart), using only asymmetric algorithms (RS256 and ES256 by
  default; `none` and the HMAC family are refused);
- the **issuer**, the **audience** (this client), the **expiry** and the **nonce** of this one sign-in;
- that the sign-in **session** is the one this browser started, and that it is used **once**.

Then it reads one claim as the player id. That id is what the grant is stored under, so it must be
the same id your game's backend uses for that player. The rest of the flow is the same as with a
link ticket: the player's choice of coach or operator, the grant, the token.

## 1. Start the identity provider and the gateway

```bash
docker compose -f docker-compose.yml -f examples/tutorials/compose.oidc.yml up -d --build dex gateway
curl -s http://127.0.0.1:9091/readyz
```

The gateway checks the identity provider **at start**. If the address is wrong, the provider is
down, or its discovery document names a different issuer, the gateway refuses to start and says
why, so a typo shows up at deploy time and not when the first player presses the button. (In this
setup it is allowed to restart until Dex is up.)

```bash
docker compose -f docker-compose.yml -f examples/tutorials/compose.oidc.yml logs gateway | grep oidc
```

```text
"msg":"oidc sign-in enabled","issuer":"http://127.0.0.1:15556/dex","client_id":"agentgw","player_claim":"name","algs":"RS256,ES256", ...
```

The secrets are never logged. The compose file (`examples/tutorials/compose.oidc.yml`) shows the
flags. Read it next to this table:

| Flag or variable | Meaning |
|---|---|
| `--oidc-issuer` | The identity provider's issuer URL. OIDC is on only when this is set. |
| `--oidc-client-id` | The client id the provider gave your gateway. |
| `AGENTGW_OIDC_CLIENT_SECRET` | The client secret. Environment only, never a flag. |
| `AGENTGW_OIDC_SESSION_SECRET` | Seals the sign-in cookie. At least 32 bytes of randomness. Replicas share it. |
| `--oidc-player-claim` | The claim that carries your player id. Default `sub`. |
| `--oidc-scopes` | Scopes to request. `openid` is always included. |
| `--oidc-algs` | Signature algorithms accepted. Default `RS256,ES256`. |
| `--oidc-allow-insecure-http` | Dev only: lets the issuer be plain `http` on a loopback address. |

At the identity provider you register the gateway as a client with the redirect URI
`<public-url>/authorize/oidc/callback`. In this tutorial that is already done in
`examples/tutorials/dex/config.yaml`.

## 2. An agent starts an authorization request

An agent registers and sends the player to the consent page, with PKCE. The `oauthclient` helper
from tutorial 4 does this for you; here you do it with `curl` so every step is visible:

```bash
GW=http://127.0.0.1:8080
RESOURCE=$(curl -s $GW/.well-known/oauth-protected-resource/mcp | sed 's/.*"resource":"\([^"]*\)".*/\1/')
CLIENT_ID=$(curl -s -X POST $GW/register -H 'Content-Type: application/json' \
  -d '{"client_name":"oidc tutorial","redirect_uris":["http://127.0.0.1/callback"]}' \
  | sed 's/.*"client_id":"\([^"]*\)".*/\1/')
VERIFIER=$(openssl rand -base64 48 | tr '+/' '-_' | tr -d '=\n' | cut -c1-64)
CHALLENGE=$(printf %s "$VERIFIER" | openssl dgst -sha256 -binary | openssl base64 -A | tr '+/' '-_' | tr -d '=')
RES_ENC=$(printf %s "$RESOURCE" | sed 's#:#%3A#g; s#/#%2F#g')
QS="client_id=$CLIENT_ID&redirect_uri=http%3A%2F%2F127.0.0.1%2Fcallback&response_type=code&code_challenge=$CHALLENGE&code_challenge_method=S256&state=tutorial&resource=$RES_ENC"
```

## 3. The consent page offers the sign-in

```bash
curl -s "$GW/authorize?$QS" | grep -o 'Sign in with your game account\|formaction="[^"]*"'
```

```text
Sign in with your game account
formaction="/authorize/oidc"
```

The button appears only when the gateway could reach the identity provider. If it cannot, the page
shows only the link code form, so a provider outage never locks players out of linking.

## 4. Press the button

The browser posts the consent form (here, with the coach scope chosen) to `/authorize/oidc`. The gateway
checks it exactly as it checks a link code request, starts a sign-in, and redirects to the provider:

```bash
JAR=$(mktemp)
curl -s -i -c "$JAR" -b "$JAR" -X POST $GW/authorize/oidc -d "$QS&scope_choice=coach" | grep -i '^HTTP\|^location\|^set-cookie' | cut -c1-120
```

```text
HTTP/1.1 303 See Other
Location: http://127.0.0.1:15556/dex/auth?client_id=agentgw&code_challenge=...&code_challenge_method=S256&nonce=...&state=...
Set-Cookie: agw_oidc=...
```

Three things protect this step. `state` ties the answer to this sign-in. `nonce` ties the ID token to
it. The PKCE challenge means a stolen code is useless. Everything the sign-in must remember lives in
the `agw_oidc` cookie, which is encrypted with `AGENTGW_OIDC_SESSION_SECRET`, sent only to the
callback path, and good for a few minutes.

## 5. Log in at the identity provider

Follow the redirects to the login form, then submit the demo account (`player@example.com`, password
`password`). A wrong password is refused by the provider and the gateway hears nothing:

```bash
LOC=$(curl -s -i -c "$JAR" -b "$JAR" -X POST $GW/authorize/oidc -d "$QS&scope_choice=coach" | tr -d '\r' | awk -F': ' 'tolower($1)=="location"{print $2}')
# follow Dex's redirects until its login page
while :; do
  R=$(curl -s -i -c "$JAR" -b "$JAR" "$LOC"); NEXT=$(echo "$R" | tr -d '\r' | awk -F': ' 'tolower($1)=="location"{print $2}')
  [ -n "$NEXT" ] || break
  case "$NEXT" in /*) LOC=http://127.0.0.1:15556$NEXT ;; *) LOC=$NEXT ;; esac
done
FORM=$(echo "$R" | grep -o 'action="[^"]*"' | head -n 1 | sed 's/action="//; s/"$//; s/&amp;/\&/g')

curl -s -i -c "$JAR" -b "$JAR" -X POST "http://127.0.0.1:15556$FORM" -d 'login=player@example.com&password=wrong' | head -n 1
```

```text
HTTP/1.1 401 Unauthorized
```

```bash
curl -s -i -c "$JAR" -b "$JAR" -X POST "http://127.0.0.1:15556$FORM" -d 'login=player@example.com&password=password' | grep -i '^HTTP\|^location' | cut -c1-110
```

```text
HTTP/1.1 303 See Other
Location: http://127.0.0.1:8080/authorize/oidc/callback?code=...&state=...
```

A browser does all of this by itself. Dex sends the player straight back to the gateway.

## 6. The gateway finishes the sign-in

The player's browser follows that redirect to the gateway's callback, carrying the `agw_oidc` cookie. The
gateway exchanges the code with the provider, checks the ID token, reads the player claim, records the
grant and sends the browser back to the agent with the agent's own code:

```bash
CALLBACK=...   # the Location from the previous step
curl -s -i -c "$JAR" -b "$JAR" "$CALLBACK" | grep -i '^HTTP\|^location' | cut -c1-110
```

```text
HTTP/1.1 303 See Other
Location: http://127.0.0.1/callback?code=...&iss=http%3A%2F%2F127.0.0.1%3A8080&state=tutorial
```

Two things that must fail, do. The same callback again is refused, because a sign-in is single use. A
callback from a browser that never started a sign-in is refused too, with nothing redirected:

```text
HTTP/1.1 400 Bad Request
```

## 7. The agent gets its token, for the right player

The agent trades the code for tokens exactly as in tutorial 4, and calls the gateway:

```bash
CODE=...       # the code in the redirect above
TOKENS=$(curl -s -X POST $GW/token -d "grant_type=authorization_code&code=$CODE&client_id=$CLIENT_ID&redirect_uri=http%3A%2F%2F127.0.0.1%2Fcallback&code_verifier=$VERIFIER&resource=$RES_ENC")
```

The token carries the coach scope the player chose. Now ask the game's side who the grant belongs to,
with the signed studio endpoint from tutorial 4:

```bash
docker compose -f docker-compose.yml -f examples/tutorials/compose.oidc.yml run --rm -T dev go build -o bin/ ./examples/studiocall
docker compose -f docker-compose.yml -f examples/tutorials/compose.oidc.yml run --rm -T dev \
  bin/studiocall --url http://dex:8080 --player player GET /v1/studio/grants
```

```text
HTTP 200
{"grants":[{"id":"...","client_name":"oidc tutorial","scope":"coach","resource":"http://127.0.0.1:8080/mcp", ...}]}
```

The grant is under the player `player`, which is the `name` claim Dex put in the ID token. Another
player id (`alice`) sees nothing. The agent cannot choose or change that id: it comes from the
verified token.

If the player presses "deny" at the identity provider, or it reports an error, the agent is sent
back with `error=access_denied` and its own `state`, and no grant is made.

## Choosing the player claim

This is the one decision you must get right. The player claim must be:

- **Stable for a person for ever.** Do not use an email address or a display name. They change, and
  a reused address would hand one player's linked agents to another.
- **Equal to your game's own player id**, because the gateway signs it into every call to your
  backend, and your backend looks the player up by it.

The default is `sub`, the identity provider's stable subject. If your game uses another id, ask
your identity provider to put it in a claim (for example `game_id`) and set `--oidc-player-claim`.
The tutorial uses Dex's `name` only because the demo account has no better claim. The gateway
refuses ids that are empty, longer than 128 characters or that contain spaces or control
characters.

## In production

- Use an `https` issuer. The `--oidc-allow-insecure-http` flag exists only for a local provider.
- Register `<public-url>/authorize/oidc/callback` as the redirect URI at the provider.
- Keep the two secrets in your secret manager. With the Helm chart set `oidc.issuer`,
  `oidc.clientId` and `oidc.playerClaim`, and put `oidc-client-secret` and `oidc-session-secret` in
  the Secret named by `existingSecret`.
- Give every replica the same session secret, so a sign-in can start on one and finish on another.
- Link tickets keep working next to the button. Players can use either.

If sign-in fails for players, [troubleshooting](../guides/troubleshooting.md) and the
[runbook](../RUNBOOK.md) list the log lines to look for. The reasons are logged (an expired token, a
nonce that does not match, a player claim that is missing) but never shown to the player, who sees one
generic message.

## What you learned

- OIDC sign-in is optional, off by default, and sits next to link tickets.
- The gateway verifies the ID token itself, with asymmetric signatures only, and a sign-in is single use.
- The player id comes from one claim you choose, and it must be stable and equal your game's id.
- A wrong issuer or an unreachable provider stops the gateway at start.

## Next

[Tutorial 6: Go live](06-go-live.md) covers the rest of a production deployment, including key
rotation. For the design and the threats considered, read the
[OIDC design note](../design/oidc-consent.md) and the [security model](../SECURITY-MODEL.md).
