#!/bin/sh
# Usage: docker compose run --rm site <command>
#   build           sync docs, build the static site into site/dist
#   dev             sync docs, start the dev server on 0.0.0.0:4321
#   check           sync docs, build, fail on warnings, validate links (internal) and output
#   check-external  check external links in site/dist (non-blocking when offline)
#   sync            only copy docs into site/src/content/docs
#   lock            (re)generate package-lock.json
#   shell           a shell in the container
set -eu
cd /repo/site

cmd="${1:-build}"
[ $# -gt 0 ] && shift

if [ "$cmd" = "lock" ]; then
  npm install --package-lock-only
  exit 0
fi
if [ "$cmd" = "shell" ]; then
  exec sh "$@"
fi

want="$(sha256sum package.json package-lock.json | sha256sum | cut -d' ' -f1)"
have="$(cat node_modules/.deps-hash 2>/dev/null || true)"
if [ "$want" != "$have" ]; then
  echo "site: installing dependencies from package-lock.json"
  npm ci
  echo "$want" > node_modules/.deps-hash
fi

case "$cmd" in
  build|dev|check|check-external|sync) exec npm run --silent "$cmd" -- "$@" ;;
  *) echo "unknown command: $cmd" >&2; exit 2 ;;
esac
