# Contributing

Thanks for helping. Small, focused pull requests are easiest to review.

## Ground rules

- **Sign off every commit** (Developer Certificate of Origin): `git commit -s`.
  By signing off you confirm you wrote the change or have the right to submit it
  under Apache-2.0. See <https://developercertificate.org>.
- **Do not contribute code, data, API details or documents that belong to someone else.**
  That includes anything from an employer or from a game you work on. If you are unsure,
  ask first or leave it out.
- Tests are required for behavior changes. `make test` must pass with the race detector.
- Run `make fmt vet` before pushing.
- Keep the dependency list short. A new third-party dependency needs a reason in the PR.
- Security issues: do not open a public issue. See [SECURITY.md](SECURITY.md).

## Larger changes

Changes to the manifest format, the signed request protocol or the policy semantics are
compatibility changes. Open an issue first and describe the problem before the solution.
Accepted proposals are recorded in `docs/DECISIONS.md`.

## Conduct

Be respectful and assume good faith. Harassment and personal attacks are not tolerated.
The maintainer may remove comments or contributors that break this rule.
