# Security policy

agentgw sits between AI agents and game economies, so security reports matter.

## Reporting a vulnerability

Please report privately through GitHub's "Report a vulnerability" feature on this
repository (Security tab). Do not open a public issue for security problems.

Include what you found, how to reproduce it, and what you think the impact is.
You can expect an acknowledgement within a few days. This is a volunteer-run project,
so fixes are best effort, but confirmed issues get priority over features.

## Scope

In scope: the gateway, the policy engine, request signing (`pkg/agentsig`), the manifest
loader, and the example code when it demonstrates an insecure pattern.

Out of scope: the dev HTTP API (`internal/httpapi`) being reachable from a public network.
It trusts the `X-Dev-Player` header by design and is documented as loopback-only.

## Supported versions

Pre-1.0: only the latest commit on `main` is supported.
