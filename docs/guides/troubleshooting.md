# Troubleshooting

Find the code the agent saw, then follow symptom, cause, check and fix. The meaning and the advice to the agent are in the generated [result codes](../reference/errors.md) table. This page adds what a studio can check.

Codes are the `code` field of a failed result. Over MCP the same code arrives with `isError` set. Every attempt is audited with its code, so the activity feed shows what happened.

## Result codes

Order follows a call through the pipeline. Codes marked dev only come from the dev HTTP API used with `--dev-auth`.

### `unknown_tool`

- **Symptom:** Agent says the tool does not exist.
- **Likely cause:** The name is not in the active manifest: a typo, a tool removed by a newer version, or a rollback.
- **Check:** `GET /v1/studio/manifest` shows the active version. Compare with the tool name the agent used.
- **Fix:** Have the agent list tools again. If the tool should exist, upload a manifest that has it. It is not charged to the rate limit.

### `scope_insufficient`

- **Symptom:** An action tool is refused for one player but works for others.
- **Likely cause:** The player linked the agent as coach (read only).
- **Check:** The player's grants: `GET /v1/studio/grants` shows the scope.
- **Fix:** The player links again as operator. Do not retry. The scope check is not charged to the rate limit.

### `switched_off`

- **Symptom:** Every call to a tool (or player) is refused with a message not to retry.
- **Likely cause:** A kill switch matches the tool, player or tenant.
- **Check:** Dev only: `GET /v1/admin/switches`. In production the cause is a switch set in the policy store.
- **Fix:** Turn the switch off. Remember a switch blocks every tier it matches, reads included.

### `tier_disabled`

- **Symptom:** A tool in the `sensitive` tier is refused.
- **Likely cause:** Sensitive is disabled in this version.
- **Check:** The tool's `tier` in the manifest.
- **Fix:** Do not retry. Move the tool to another tier only if the risk allows it, or leave it out. Sensitive tools are never listed to agents.

### `rate_limited`

- **Symptom:** Agent is told to slow down.
- **Likely cause:** The player used up 120 reads or 30 actions this minute. Every attempt counts, valid or not.
- **Check:** The activity feed for the player, and `gateway_calls_total{outcome="rate_limited"}`.
- **Fix:** Wait a minute. If real players hit it, make tools coarser. If an agent loops, look at its plan. The numbers are built in and not a flag.

### `invalid_arguments`

- **Symptom:** The call is refused before it reaches your game.
- **Likely cause:** A missing, wrongly typed, out of range or too long argument, a value not in the `enum`, extra data after the JSON, or a path value that is empty, `.` or `..`.
- **Check:** The `message` names the argument. Compare with the tool's `params` in the manifest.
- **Fix:** Fix the arguments. If the manifest is too strict for real use, upload a looser one. Invalid attempts still count against the rate limit.

### `confirmation_required`

- **Symptom:** A tool with `confirm: true` does not run.
- **Likely cause:** No approval is on file for this exact call.
- **Check:** The result data carries `confirmation_id` and `expires_at`. In the demo, `GET /v1/confirmations`.
- **Fix:** The player approves in your game. Then the agent repeats the exact same call. An approval lasts 10 minutes and works once.

### `confirmation_denied`

- **Symptom:** The agent is told not to repeat a call.
- **Likely cause:** The player denied this exact call.
- **Check:** The activity feed shows the denial.
- **Fix:** Do not repeat. The agent should ask the player what they want. A new call with other arguments asks again.

### `confirmation_in_use`

- **Symptom:** A repeated call is refused while another runs.
- **Likely cause:** The approved call is already being executed.
- **Check:** Is the first call still running? Look for a slow backend.
- **Fix:** Wait for the first call to finish. If it fails, the approval returns to the player.

### `confirmation_unavailable`

- **Symptom:** Calls to confirm tools fail, other tools work.
- **Likely cause:** Postgres is down, or approvals are not configured (no `--postgres-url` and no confirmation store).
- **Check:** `/readyz` shows a failing `postgres` check. The gateway logs the store error.
- **Fix:** Restore Postgres. Nothing was changed by the refused call.

### `cooldown`

- **Symptom:** A tool is refused for a short time.
- **Likely cause:** The tool was used successfully less than `cooldown_seconds` ago.
- **Check:** The `message` says how many seconds remain.
- **Fix:** Wait. A call that failed does not start a cooldown. Lower `cooldown_seconds` if it is too long.

### `daily_call_cap`

- **Symptom:** A tool is refused until tomorrow.
- **Likely cause:** The tool's `per_day_calls` is used up. Days are UTC days.
- **Check:** The tool's `caps` in the manifest and the player's activity.
- **Fix:** Wait for the next UTC day, or raise `per_day_calls`. Calls that failed do not count.

### `daily_amount_cap`

- **Symptom:** A sale or purchase is refused.
- **Likely cause:** The call would take the day's total over `per_day_amount`.
- **Check:** The `message` says how many units are left.
- **Fix:** Retry with a smaller amount, wait for the next UTC day, or raise the cap.

### `policy_unavailable`

- **Symptom:** Every call fails, reads included.
- **Likely cause:** The policy store (Redis) is unreachable. Calls fail closed.
- **Check:** `/readyz` fails the `redis` check. Check the Redis address and network.
- **Fix:** Restore Redis. Nothing was changed. If Redis lost its data, counters start fresh.

### `invalid_principal`

- **Symptom:** Every call for some player fails, nothing reaches the backend.
- **Likely cause:** The player id or the call id is not a safe header value (not printable ASCII, or longer than 128 bytes).
- **Check:** The player id your system gives the gateway, for example in the grant.
- **Fix:** Use short printable ASCII player ids. This is a studio configuration problem, not something the agent can fix.

### `backend_rejected`

- **Symptom:** The game says no, with its own text.
- **Likely cause:** Your backend answered 4xx. The gateway passes on the first 500 characters.
- **Check:** Read the `message`. Check your backend log for the same request.
- **Fix:** Fix the call, or the rule in your backend. If you see 401, check the signature, the clock (five minute skew) and the key id.

### `backend_unavailable`

- **Symptom:** Calls to the game fail, and nothing changed.
- **Likely cause:** The backend could not be reached, answered 5xx, or the URL is on a private address while `--backend-allow-private` is off. Redirects are not followed, so a 3xx is also a failure.
- **Check:** Is the backend up? `curl` it from the gateway's network. Check `--backend-url`.
- **Fix:** Fix the backend or its route. Set `--backend-allow-private` only if the backend is yours and on a private network. The cap and the approval are released.

### `outcome_unknown`

- **Symptom:** The agent is told the call may or may not have happened.
- **Likely cause:** The call timed out (10 second connector timeout) or the agent cancelled after the request may have been sent.
- **Check:** Your backend log and the audit event show whether it was applied.
- **Fix:** The agent must read the game state before repeating. Make the backend faster, or give the agent a read tool for the effect.

### `client_offline`

- **Symptom:** A relay tool says the game client is offline.
- **Likely cause:** The player has no open socket on any replica, or the relay is off.
- **Check:** The startup log `relay` shows `enabled=true`. `relay_connections` shows sockets per replica.
- **Fix:** The player opens the game. Make sure the client got a ticket and connects with it. All replicas must share one Redis and one `AGENTGW_RELAY_SECRET`.

### `client_error`

- **Symptom:** A relay tool failed and the message is from the client.
- **Likely cause:** The game client sent a failed result with no code.
- **Check:** The `message` is the client's text. Check the client log.
- **Fix:** Fix the client, and have it send a `code` with failures.

### `bad_backend_data`

- **Symptom:** A call succeeded in the game but the agent sees an error.
- **Likely cause:** The backend answered with data the gateway could not read.
- **Check:** Request the backend URL yourself and look at the response and its `Content-Type`.
- **Fix:** Return valid JSON, within 1 MiB.

### `no_player`

- **Symptom:** Dev API call fails with no player.
- **Likely cause:** The dev HTTP API needs the `X-Dev-Player` header.
- **Check:** The request headers.
- **Fix:** Set `X-Dev-Player`. Dev only.

### `too_large`

- **Symptom:** Dev API call is refused for size.
- **Likely cause:** The request body is over the limit.
- **Check:** The body size.
- **Fix:** Send a smaller body. Dev only.

### `bad_request`

- **Symptom:** Dev API call is refused as malformed.
- **Likely cause:** The body is not the JSON the dev API expects.
- **Check:** The body against `{"arguments":{...}}`.
- **Fix:** Fix the JSON. Dev only.

### `not_configured`

- **Symptom:** A dev API route says the feature is off.
- **Likely cause:** Confirmations or kill switches are not enabled in this run.
- **Check:** The flags the gateway was started with.
- **Fix:** Enable the feature, or do not call the route. Dev only.

### `not_found`

- **Symptom:** A dev API confirmation lookup fails.
- **Likely cause:** No such confirmation for this player, or it expired (10 minutes).
- **Check:** `GET /v1/confirmations` for that player.
- **Fix:** List again and use a current id. For the studio endpoints the HTTP error is described below.

### `wrong_state`

- **Symptom:** A dev API confirmation move is refused.
- **Likely cause:** The confirmation can no longer make that move, for example approving a denied one.
- **Check:** `GET /v1/confirmations` and its state.
- **Fix:** List again. Approve and deny are idempotent when the state already matches.

### `forbidden`

- **Symptom:** A dev API admin route is refused.
- **Likely cause:** The `X-Dev-Admin` header is missing.
- **Check:** The request headers.
- **Fix:** Set `X-Dev-Admin: true`. Dev only, loopback only.

## HTTP errors of the studio, admin and relay endpoints

These come back as `{"error":"<code>"}` with an HTTP status to your backend, not to the agent.

### `invalid_client` (401)

- **Likely cause:** a wrong key id or secret, a clock more than five minutes off, a missing header, a body that was changed after signing, or a repeated idempotency key.
- **Check:** the five `X-AG-*` headers, the signed string in [PROTOCOL.md](../PROTOCOL.md), and the clock.
- **Fix:** sign again with a fresh random idempotency key per request. The response is the same for every cause on purpose.

### `invalid_ticket` (401)

- **Likely cause:** the relay ticket expired (60 seconds), was signed with another relay secret, or is malformed.
- **Fix:** mint a new ticket just before connecting. All replicas must share `AGENTGW_RELAY_SECRET`.

### `invalid_request` (400 or 413)

- **Likely cause:** a query parameter or body is malformed, for example a bad `since` or `limit` on the activity feed, or a body too large on an OAuth endpoint.
- **Fix:** check the parameters against the [studio API](../reference/studio-api.md).

### `not_found` (404) and `wrong_state` (409)

- **Likely cause:** the grant, confirmation or manifest version does not exist, expired, or belongs to another player. A confirmation cannot make the move you asked for.
- **Fix:** list again and use a current id. Approve and deny are idempotent when the state already matches.

### `invalid_manifest` (422)

- **Likely cause:** the uploaded or stored manifest failed validation.
- **Check:** the `detail` field lists every problem. Run `gwctl validate` locally.
- **Fix:** correct the manifest. Nothing was changed.

### `too_large` (413)

- **Likely cause:** the body is over the limit, for example a manifest over 1 MiB.
- **Fix:** send less.

### `temporarily_unavailable` (503)

- **Likely cause:** a store failed, or the replay cache is full.
- **Fix:** retry with a new idempotency key. Nothing was changed.

## Still stuck

The [runbook](../RUNBOOK.md) has a symptom table for operators, and the [FAQ](faq.md) answers common questions. Report bugs on the project's GitHub issues page.
