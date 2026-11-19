package core

// CodeInfo documents one machine-readable result code. The reference page
// docs/reference/errors.md is generated from Codes, and a test fails when the source emits
// a code that is not listed here.
type CodeInfo struct {
	Code    string
	Source  string // which part of the gateway emits it
	Meaning string // what happened
	Action  string // what the agent (or its developer) should do
}

// Codes lists every result code the gateway returns to an agent, in the order the pipeline
// can produce them.
var Codes = []CodeInfo{
	{"unknown_tool", "pipeline", "The tool name is not in the active manifest.", "List the tools again and use one of those names."},
	{"scope_insufficient", "pipeline", "The player linked the agent as coach (read only) and the tool changes the game.", "Ask the player to grant operator access. Do not retry."},
	{"switched_off", "policy", "A kill switch is on for this tool, player or tenant.", "Do not retry until the game turns it back on."},
	{"tier_disabled", "policy", "The tool is in the sensitive tier, which is disabled.", "Do not retry. The studio has not enabled this tier."},
	{"rate_limited", "policy", "The player's per-minute budget for reads or actions is used up. Every attempt counts, valid or not.", "Wait a minute and retry with fewer calls."},
	{"invalid_arguments", "pipeline and connector", "An argument is missing, has the wrong type, is outside min or max, is not in the enum, is too long, or cannot be placed in the URL path.", "Read the message, fix the arguments and call again."},
	{"confirmation_required", "pipeline", "The tool needs the player's approval and none is on file. The result data carries confirmation_id and expires_at.", "Ask the player to approve in the game, then repeat the exact same call."},
	{"confirmation_denied", "pipeline", "The player denied this exact call.", "Do not repeat it. Ask the player what they want instead."},
	{"confirmation_in_use", "pipeline", "The approved call is already running.", "Wait for it to finish instead of repeating it."},
	{"confirmation_unavailable", "pipeline and dev API", "Approvals are not configured or the confirmation store is down. Nothing was changed.", "Try again later. If it persists, the studio must enable confirmations."},
	{"cooldown", "policy", "The tool was used successfully too recently.", "Wait the number of seconds in the message."},
	{"daily_call_cap", "policy", "The tool's per-day call count is used up.", "Do not retry until the next day."},
	{"daily_amount_cap", "policy", "The call would exceed the per-day amount for the tool.", "Retry with a smaller amount, or wait until the next day."},
	{"policy_unavailable", "pipeline and dev API", "The policy store failed. Action tiers fail closed.", "Try again later. Nothing was changed."},
	{"invalid_principal", "connector", "The player or call identifier is not a safe header value, so nothing was signed or sent.", "This is a gateway or studio configuration problem. Do not retry."},
	{"backend_rejected", "connector", "The game backend answered 4xx. The message is the backend's own text (at most 500 characters).", "Read the message, it says why. Change the call or stop."},
	{"backend_unavailable", "pipeline", "The game could not be reached or answered 5xx. Nothing was changed and the reservation was released.", "Try again later."},
	{"outcome_unknown", "pipeline", "The call was cut off (cancelled or timed out) after it may have reached the game. It may or may not have happened.", "Check the game state before repeating the call."},
	{"client_offline", "relay", "A relay tool was called but the player's game client has no open socket.", "Ask the player to open the game, then retry."},
	{"client_busy", "relay", "The player's game client already has its limit of calls in flight (see --relay-max-in-flight), or it answered that it is busy. The call was not sent or not run. The message may say when to retry.", "Wait a moment, then retry. Do not send many calls at once."},
	{"client_error", "relay", "The game client answered a relay call with a failure and no code.", "Read the message. Retry only if it makes sense."},
	{"bad_backend_data", "MCP server", "The game answered with data the gateway could not read.", "Try again later. Report it to the studio if it persists."},
	{"no_player", "dev API", "The dev HTTP API needs the X-Dev-Player header.", "Set the header. Dev only."},
	{"too_large", "dev API", "The request body is too large.", "Send a smaller body."},
	{"bad_request", "dev API", "The dev API body is not the expected JSON.", "Fix the request body."},
	{"not_configured", "dev API", "The feature (confirmations or kill switches) is not enabled in this run.", "Enable it or do not call it."},
	{"not_found", "dev API", "No such confirmation for this player, or it expired.", "List confirmations again."},
	{"wrong_state", "dev API", "The confirmation can no longer make that move.", "List confirmations again."},
	{"forbidden", "dev API", "The dev admin header is missing.", "Set X-Dev-Admin: true. Dev only."},
}

// LookupCode returns the documentation for a result code.
func LookupCode(code string) (CodeInfo, bool) {
	for _, c := range Codes {
		if c.Code == code {
			return c, true
		}
	}
	return CodeInfo{}, false
}

// HTTPError documents one `{"error": "..."}` code the studio, admin and relay HTTP endpoints
// return. These are HTTP status codes plus a short error string, a different layer from the
// result codes above.
type HTTPError struct {
	Code    string
	Status  string
	Meaning string
}

// HTTPErrors lists the error strings of the studio, admin and relay HTTP endpoints.
var HTTPErrors = []HTTPError{
	{"invalid_client", "401", "The signature is missing, wrong, stale, or the signed request was already used (replay)."},
	{"invalid_ticket", "401", "The relay ticket is missing, tampered with or expired."},
	{"invalid_request", "400 or 413", "A query parameter or body is malformed, or the body is too large (OAuth endpoints)."},
	{"invalid_manifest", "422", "The uploaded or stored manifest failed validation. The detail field lists every problem."},
	{"not_found", "404", "The grant, confirmation or manifest version does not exist, expired, or belongs to another player."},
	{"wrong_state", "409", "A confirmation cannot make that move, for example approving a denied one."},
	{"too_large", "413", "The body is over the endpoint's limit."},
	{"temporarily_unavailable", "503", "A store failed or the replay cache is full. Nothing was changed. Retry with a new idempotency key."},
}
