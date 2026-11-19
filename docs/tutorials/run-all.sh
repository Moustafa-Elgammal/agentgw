#!/usr/bin/env bash
# Runs every tutorial script in order and reports all failures at the end.
#
#   docs/tutorials/run-all.sh            run every NN-*.sh
#   docs/tutorials/run-all.sh --only 03  run only tutorial 03
#
# Each tutorial script holds the commands of its guide. They run against the compose stack, so
# Docker with Compose v2 is the only requirement. Exit status is nonzero if any script failed.
set -u

dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$dir/../.." || exit 2

only=""
while [ $# -gt 0 ]; do
  case "$1" in
    --only)
      [ $# -ge 2 ] || { echo "run-all: --only needs a number like 03" >&2; exit 2; }
      only="$2"
      shift 2
      ;;
    -h | --help)
      sed -n '2,9p' "${BASH_SOURCE[0]}"
      exit 0
      ;;
    *)
      echo "run-all: unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

if [ -n "$only" ] && ! [[ "$only" =~ ^[0-9]{2}$ ]]; then
  echo "run-all: --only takes two digits, for example 03" >&2
  exit 2
fi

scripts=()
for f in "$dir"/[0-9][0-9]-*.sh; do
  [ -e "$f" ] || continue
  n="$(basename "$f")"
  if [ -n "$only" ] && [ "${n:0:2}" != "$only" ]; then
    continue
  fi
  # Tutorials 9 and 11 need a Kubernetes cluster, which most machines and the guides workflow do
  # not have. Run them with `make k8s-tutorial-check` (kind) or `make k8s-docker-desktop-check`.
  if [ -z "$only" ] && { [ "${n:0:2}" = "09" ] || [ "${n:0:2}" = "11" ]; }; then
    echo "run-all: skipping $n (needs a Kubernetes cluster: make k8s-tutorial-check or k8s-docker-desktop-check)"
    continue
  fi
  scripts+=("$f")
done

if [ "${#scripts[@]}" -eq 0 ]; then
  echo "run-all: no tutorial scripts found${only:+ for $only}" >&2
  [ -n "$only" ] && exit 2
  exit 0
fi

pass=()
fail=()
for f in "${scripts[@]}"; do
  n="$(basename "$f")"
  echo "=== $n"
  if bash "$f"; then
    pass+=("$n")
    echo "=== $n: ok"
  else
    fail+=("$n")
    echo "=== $n: FAILED"
  fi
done

echo
echo "Tutorial scripts: ${#pass[@]} passed, ${#fail[@]} failed"
for n in "${pass[@]}"; do echo "  ok      $n"; done
for n in "${fail[@]}"; do echo "  FAILED  $n"; done
[ "${#fail[@]}" -eq 0 ]
