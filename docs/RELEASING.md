# Releasing

## Version rules

Semantic versioning, starting at `v0.1.0`. While the major version is 0, a minor release may
change manifest or protocol details. Every such change has an entry in `DECISIONS.md` and a line
in `CHANGELOG.md`. Patch releases fix bugs only. A tag with a hyphen (`v0.2.0-rc.1`) is a
pre-release: it gets a GitHub pre-release and no `latest` image tag.

## Cut a release

1. Make sure `main` is green and `make fmt vet test lint vuln` pass in Docker.
2. Run `make deploy-check workflows-check release-check` and `make release-snapshot`
   (everything runs in containers; the snapshot publishes nothing and writes `dist/`).
3. Update `CHANGELOG.md` and commit it, signed off.
4. Tag and push: `git tag -s v0.1.0 -m "v0.1.0" && git push origin v0.1.0`.
5. Watch the `release` workflow. When it ends, open the release page and check the files below.

## What the workflow does

The `release` workflow runs on a tag matching `v*`.

1. **test**: `go vet` and `go test -race ./...` with real Redis and Postgres.
2. **publish**, only if the tests pass:
   - GoReleaser builds `gateway`, `gwctl` and `gwbridge` for linux, darwin and windows on
     amd64 and arm64, archives them, writes `checksums.txt`, writes an SPDX SBOM for each archive
     (syft), and signs `checksums.txt` keyless with cosign (`checksums.txt.sig` and
     `checksums.txt.pem`). It creates the GitHub release with the git log as notes.
   - A build provenance attestation is created for the files listed in `checksums.txt`.
   - Multi-arch (linux/amd64 and linux/arm64) images are built from the `gateway` and `bridge`
     Dockerfile targets and pushed to `ghcr.io/moustafa-elgammal/agentgw` and
     `ghcr.io/moustafa-elgammal/agentgw-bridge`, tagged with the version (without the `v`) and
     `latest` (not for pre-releases).
   - Each image is signed by digest with cosign (keyless), gets a provenance attestation pushed to
     the registry, and an SPDX SBOM attached to the release as `agentgw-image.spdx.json` and
     `agentgw-bridge-image.spdx.json`.

Only this job has write permissions. Third party actions are pinned by commit SHA.

## Verify a download

Replace `v0.1.0` and the file name with yours. Signatures are keyless, so you check who signed:
this repository's `release` workflow, run for a `v*` tag.

```bash
ID='^https://github.com/Moustafa-Elgammal/agentgw/\.github/workflows/release\.yml@refs/tags/v.*$'
ISSUER=https://token.actions.githubusercontent.com
```

**Binaries.** Download the archive, `checksums.txt`, `checksums.txt.sig` and `checksums.txt.pem`.

```bash
cosign verify-blob checksums.txt \
  --signature checksums.txt.sig --certificate checksums.txt.pem \
  --certificate-identity-regexp "$ID" --certificate-oidc-issuer "$ISSUER"
sha256sum --ignore-missing -c checksums.txt        # on macOS: shasum -a 256 --ignore-missing -c checksums.txt
gh attestation verify agentgw_0.1.0_linux_amd64.tar.gz --repo Moustafa-Elgammal/agentgw
```

**Images.**

```bash
cosign verify ghcr.io/moustafa-elgammal/agentgw:0.1.0 \
  --certificate-identity-regexp "$ID" --certificate-oidc-issuer "$ISSUER"
gh attestation verify oci://ghcr.io/moustafa-elgammal/agentgw:0.1.0 --repo Moustafa-Elgammal/agentgw
```

The same commands work for `agentgw-bridge`. For production, pin an image by digest
(`ghcr.io/moustafa-elgammal/agentgw@sha256:...`).

**SBOMs.** Each archive has a `<archive>.spdx.json` file on the release page, and each image has
`agentgw-image.spdx.json` or `agentgw-bridge-image.spdx.json`. They are plain SPDX JSON.

**Version.** `gateway --version`, `gwctl version` and `gwbridge --version` print the release.
