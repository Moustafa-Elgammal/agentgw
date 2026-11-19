# GitHub repository settings checklist

Settings live on GitHub, not in the code, so they are applied by hand (or with the commands below)
once, when the repository goes public. Do the pre-flight in [AUDIT-REPORT.md](AUDIT-REPORT.md) first.
The `gh` commands need the GitHub CLI signed in as the owner. Run them from any machine, not
inside the build containers.

## Before the first public push

1. The repository is **private** and empty (delete and recreate it, or force push the rewritten
   history, see the plan).
2. In your GitHub account settings: turn on **Keep my email addresses private** and **Block
   command line pushes that expose my email**.
3. Push `main` and the tags. Check `git log --format='%an <%ae>' | sort -u` shows only the noreply
   address.
4. Do not push `backup/*` branches.

## Visibility and metadata

The About box on the repository page. The description is under 350 characters and each topic
is lowercase and under 50.

```bash
gh repo edit Moustafa-Elgammal/agentgw \
  --description "Open-source gateway that lets a player's own AI agent act safely in a game: MCP tools from a manifest, policy, signed calls, audit." \
  --homepage "https://elgx.me/agentgw/" \
  --add-topic mcp --add-topic model-context-protocol --add-topic ai-agents \
  --add-topic game-development --add-topic gateway --add-topic oauth \
  --add-topic golang --add-topic rate-limiting --add-topic audit-log \
  --add-topic websocket --add-topic kubernetes --add-topic helm \
  --enable-discussions --enable-issues --enable-wiki=false
gh repo edit Moustafa-Elgammal/agentgw --visibility public --accept-visibility-change-consequences
```

A shorter description for a tighter card: "Let a player's AI agent play your game safely. MCP
gateway with policy, caps, confirmations, signed backend calls and audit."

The website is the docs site. If the custom domain goes away, use the default Pages URL
`https://moustafa-elgammal.github.io/agentgw/`.

## Branch protection on `main`

Require a pull request, the `test`, `lint`, `govulncheck` and `signoff` checks (job names), linear history, no force
push, no deletion, and do not allow admin bypass. Use a ruleset (Settings, Rules) or:

```bash
gh api -X PUT repos/Moustafa-Elgammal/agentgw/branches/main/protection --input - <<'JSON'
{
  "required_status_checks": {"strict": true, "contexts": ["test", "lint", "govulncheck", "signoff"]},
  "enforce_admins": true,
  "required_pull_request_reviews": {"required_approving_review_count": 0},
  "restrictions": null,
  "required_linear_history": true,
  "allow_force_pushes": false,
  "allow_deletions": false
}
JSON
```

Check the job names against `.github/workflows` first. A required check that never runs blocks
every merge.

## Security features

```bash
gh api -X PUT repos/Moustafa-Elgammal/agentgw/private-vulnerability-reporting
gh api -X PUT repos/Moustafa-Elgammal/agentgw/vulnerability-alerts
gh api -X PATCH repos/Moustafa-Elgammal/agentgw --input - <<'JSON'
{"security_and_analysis": {
  "secret_scanning": {"status": "enabled"},
  "secret_scanning_push_protection": {"status": "enabled"}}}
JSON
```

Dependabot version updates come from `.github/dependabot.yml`. CodeQL comes from the workflow.

## Labels

Create the labels in `.github/labels.yml` (for example with `gh label create`), including
`good first issue`, `help wanted`, `area/*` and `milestone/*`.

## Actions

Settings, Actions, General: allow GitHub-owned and verified creators, require approval for first
time contributors, default workflow permissions read only. Release signing uses the repository's
OIDC identity, so no signing secret is stored.

## The second repository

Repeat the pre-flight and a smaller version of this list for `tinycity-mock` (private until the
audit is clean, then public). The workspace root repository stays private.

```bash
gh repo edit Moustafa-Elgammal/tinycity-mock \
  --description "Fictional city-builder MCP server and REST mock, used as a test game for agentgw. Not a real game." \
  --add-topic mcp --add-topic mock-server --add-topic test-fixture \
  --add-topic python --add-topic ai-agents
```

## After M8

Enable Pages with the GitHub Actions source. The repository website is already set in the
metadata step above.
