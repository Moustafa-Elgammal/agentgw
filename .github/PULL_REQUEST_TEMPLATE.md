## What and why

## Checklist

- [ ] Behavior changes have tests (table-driven where the package already is)
- [ ] Docs are updated (`docs/`, README) where behavior changed
- [ ] Compatibility changes (manifest format, signed request, policy semantics) have an entry in `docs/DECISIONS.md`
- [ ] A new dependency has a reason here and an entry in `docs/DECISIONS.md`
- [ ] Commits are signed off (`git commit -s`)
- [ ] No third-party or proprietary material (no real game, company or customer data)
- [ ] `make fmt vet test lint` pass in Docker
