#!/bin/sh
# Entry point of the site-audit container (image mcr.microsoft.com/playwright, browsers included).
#   docker compose run --rm site-audit          audit site/dist (build it first: make site-build)
#   docker compose run --rm site-audit lock     regenerate package-lock.json
set -eu
cd /repo/site/audit
if [ "${1:-}" = "lock" ]; then
  npm install --package-lock-only --no-audit --no-fund
  exit 0
fi
want="$(sha256sum package.json package-lock.json | sha256sum | cut -d' ' -f1)"
have="$(cat node_modules/.deps-hash 2>/dev/null || true)"
if [ "$want" != "$have" ]; then
  echo "site-audit: installing dependencies from package-lock.json"
  npm ci --no-audit --no-fund
  echo "$want" > node_modules/.deps-hash
fi
exec node audit.mjs "$@"
