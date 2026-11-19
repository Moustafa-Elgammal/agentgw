# Pawprint Pets manifest

`manifest.json` describes the toy pet shop API in `../pet-backend-go` and `../pet-backend-python`.
It is the manifest built in [tutorial 2](../../docs/tutorials/02-describe-your-actions.md), and the
tutorial's script fails if the two differ. Validate it with
`docker compose run --rm dev go run ./cmd/gwctl validate examples/pet-shop/manifest.json`.
