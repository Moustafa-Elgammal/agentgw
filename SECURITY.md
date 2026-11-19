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

| Version | Supported |
|---|---|
| Latest `0.x` release | Yes: security fixes and bug fixes |
| Older `0.x` releases | No: upgrade to the latest release |
| `main` | Fixes land here first, but it is not a release |

Until `1.0`, a fix may need a new minor release instead of a patch. Releases are signed, so verify
what you run: see [docs/RELEASING.md](docs/RELEASING.md).
