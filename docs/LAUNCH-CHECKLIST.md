# Launch checklist for v0.1.0

For the maintainer. Do the steps in order. Everything marked "agent" was checked locally; the
GitHub steps can only be done by you, and the release workflow has never run, so the rc tag in
step 4 is the real test.

## 1. Before anything is tagged

```bash
docker compose run --rm dev make fmt vet test lint vuln
make deploy-check workflows-check release-check release-snapshot
make guides-check            # about 8 minutes, runs all tutorial scripts
```

All of these must pass on `main`. The author of every commit must be your noreply address:

```bash
git log --format='%ae%n%ce' | sort -u      # only your noreply, Dependabot's and GitHub's noreply
```

## 2. Apply the repository settings

Follow [REPO-SETTINGS.md](REPO-SETTINGS.md): About box, topics, branch protection, secret
scanning, private vulnerability reporting, labels, Actions policy. Required checks are the job
names `test`, `lint`, `govulncheck` and `signoff`. Check them against a real run of the workflows
on a pull request before you make them required, or a check that never reports blocks every merge.

## 3. Confirm the workflows are green

Open the Actions tab on `main` and check `ci`, `vuln`, `codeql`, `dco`, `pages` and
`docs-quality`. Dependabot bumped the actions, and none of the bumps has run until now, so
expect to fix something small. Merge Dependabot pull requests only when their checks pass.

## 4. Release candidate

```bash
git tag -a v0.1.0-rc.1 -m "v0.1.0-rc.1"
git push origin v0.1.0-rc.1
```

Watch the `release` workflow. Things that have never run: GoReleaser with SBOM and signing, the
multi-arch image build, cosign keyless signing of the images, build provenance. Known doubts:
the cosign flags in `.goreleaser.yaml` (newer cosign prefers a `--bundle` file), and whether the
syft installer leaves `syft` on the PATH. Then verify the result with the commands in
[RELEASING.md](RELEASING.md): `sha256sum -c`, `cosign verify-blob`, `cosign verify`,
`gh attestation verify`, and run `docker run --rm ghcr.io/moustafa-elgammal/agentgw:0.1.0-rc.1 --version`.

If it fails: delete the GitHub release and the tag (`git push origin :refs/tags/v0.1.0-rc.1`),
delete the package versions on GHCR, fix, and tag `rc.2`.

## 5. The real release

Set the date in `CHANGELOG.md` (replace "unreleased" in the 0.1.0 heading), commit it signed off,
push, then:

```bash
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

Verify again as in step 4. Images are immutable once pulled by others, so a bad release is not
undone by deleting it: publish `v0.1.1` and mark the bad release as a pre-release.

## 6. After the release

- Set the repository website and confirm Pages serves the site at your domain.
- Check the quickstart in the README on a machine that has never built the project.
- Enable Discussions categories (Q&A, Ideas, Show and tell) and pin two or three
  `good first issue` issues.
- Make the `tinycity-mock` repository public after the same audit, and update its README link.
- Have one person who has not seen the project follow the quickstart and the first tutorials, and
  fix what slows them down.
- Record a short terminal demo for the site's landing page (`scripts/demo.sh`, see `docs/DEMO.md`).
- Announce it, once, where MCP and game developers read.

## Not covered by any local check

- The release workflow itself, and every action version Dependabot bumped.
- The relay inside Unity (the C# client has been tested outside Unity only).
- A first-time reader following the guides without help.
