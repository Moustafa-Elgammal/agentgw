# Use the gateway from Claude Desktop

Claude Desktop launches local stdio MCP servers and cannot reach an `http://localhost`
connector. `gwbridge` closes that gap: it speaks MCP over stdio to the client and forwards
every tool list and tool call to the gateway's `/mcp` endpoint. It holds no policy, so limits,
scopes and audit stay in the gateway. Build it with `docker compose build bridge`
(image `agentgw-bridge:latest`), then add this to `claude_desktop_config.json` (use the
absolute docker path):

```json
{"mcpServers":{"tinycity":{"command":"/usr/local/bin/docker","args":["run","-i","--rm","agentgw-bridge:latest","--url","http://host.docker.internal:8081/mcp","--header","X-Dev-Player: desktop"]}}}
```

`--header "Name: value"` is repeatable and `--timeout` sets the upstream HTTP timeout. Sending
`X-Dev-Player` only works against a gateway started with `--dev-auth` and is for local testing
only. Real deployments pass a bearer token, for example `--header "Authorization: Bearer <token>"`.
If the gateway cannot be reached, or answers 401, the bridge exits or reports the problem on
stderr (stdout carries the protocol and is never used for logs).
