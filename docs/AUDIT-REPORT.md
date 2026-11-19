# Pre-publication audit (M7, step 7.1)

Scope: the `agentgw` repository, the `tinycity-mock` repository and the workspace root
repository, working trees and full history (`--all`). Everything ran in containers. Nothing was
pushed or posted.

## Results

| Check | Tool | Result |
|---|---|---|
| Secret scan, full history | gitleaks v8.30.1 (`ghcr.io/gitleaks/gitleaks`) | No leaks. agentgw 51 commits, tinycity-mock 7, root 24 |
| Author identity | `git log --all --format='%an <%ae> %cn <%ce>'` | Every commit in all three repos uses the GitHub `noreply` address |
| Personal address and local paths | `git log --all -p` and `git grep` for `yahoo`, `/Users/`, the old local part | No hits in history, messages or trees |
| Employer, real game or company names | Regex sweep of trees, history and messages | No names. The only matches are the independence statements themselves ("no code, data or names from any employer") |
| Dependency licenses, Go | google/go-licenses v1.6.0 | All Apache-2.0, MIT, BSD or ISC. See `docs/THIRD-PARTY-LICENSES.md` |
| Dependency licenses, Python (tinycity-mock) | `importlib.metadata` after `pip install -r requirements.txt` in `python:3.13-slim` | 34 packages, all MIT, BSD, Apache-2.0, ISC-style, PSF-2.0 or MIT-0. Nothing copyleft |
| Vulnerabilities | govulncheck on go1.26.11 | No vulnerabilities found |
| Lint and format | golangci-lint | 0 issues |

## Findings and actions

1. **Tooling leftovers.** `agentgw/.claude/commands/check.md` is a committed slash command that
   only runs the Docker checks. It is harmless and kept; remove it if you do not want Claude Code
   files in the public tree. `agentgw/CLAUDE.md` is a one line pointer to `AGENTS.md`.
2. **tinycity-mock `.claude/settings.json`** was committed (a local Claude Code permission list).
   Deleted from the working tree to match the decision made for agentgw. The deletion is not
   committed. `tinycity-mock/CLAUDE.md` is kept as its contributor guide.
3. **tinycity-mock community files were missing.** Added `NOTICE`, `CONTRIBUTING.md` (DCO),
   `CODE_OF_CONDUCT.md`, `SECURITY.md` and an "Independence and license" section in the README.
   `LICENSE` was already Apache-2.0. Not committed.
4. **Placeholders that stay.** `dev-secret` (default `AGENTGW_SIGNING_SECRET`), `agentgw-dev` (CI
   and compose Postgres password) and the example keys in docs are development placeholders. They
   are documented as dev only in `AGENTS.md`, the README and the runbook.
5. **Large or binary files.** None. The largest tracked file is 28 KB. `.idea/` is ignored and not
   tracked in either repository.
6. **Old history.** The backup mirrors made before the rewrite still hold the previous author
   address. They live outside the repositories and must not be published.

## Residual risks

- The GitHub remotes still hold the pre-rewrite history until it is force pushed or the
  repositories are recreated. Old commit hashes stay reachable on GitHub, forks and clones for a
  while after a force push, so recreating the repositories before going public is cleaner.
- gitleaks finds known secret shapes. It cannot prove that no sensitive prose exists. The
  independence sweep is a pattern search for names the auditor can guess, so a final read by the
  maintainer is still worthwhile.
- Go licenses are reported for the packages the binaries build from, not for test-only or
  tooling dependencies in the `dev` image.

## Repeat the audit

```bash
# secrets (run per repository root: agentgw, tinycity-mock, workspace root)
docker run --rm -v "$PWD:/repo" ghcr.io/gitleaks/gitleaks:latest detect --source /repo --log-opts=--all --redact

# identity and personal data
git log --all --format='%an <%ae> | %cn <%ce>' | sort | uniq -c
git log --all -p | grep -n -i -E 'yahoo|/Users/'
git ls-files | xargs grep -n -I -i -E 'yahoo|/Users/'

# licenses, vulnerabilities, lint
docker compose run --rm licenses          # rewrites docs/THIRD-PARTY-LICENSES.md, fails on a disallowed license
docker compose run --rm dev make vuln lint

# python dependency licenses (tinycity-mock)
docker run --rm -v "$PWD/requirements.txt:/r.txt:ro" python:3.13-slim sh -c \
  'pip install -q -r /r.txt && python -c "import importlib.metadata as m; [print(d.metadata[\"Name\"], d.version, d.metadata.get(\"License-Expression\") or d.metadata.get(\"License\")) for d in m.distributions()]"'
```
