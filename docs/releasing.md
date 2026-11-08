# Releasing kern

A release is a `v*` tag on a commit of `main` that CI has passed. Pushing
the tag runs `.github/workflows/release.yml`, which checks the tag and the
changelog, builds the archives reproducibly and publishes them. The contract
is `specs/release-v0.1/SPEC.md`.

## Versions

Tags are `v` followed by a Semantic Versioning 2.0.0 version without build
metadata: `v0.1.0`, `v0.2.0-rc.1`. A tag with a `-` suffix is published as
a pre-release. What a version promises is in the README's "Versioning and
stability" section.

A binary reports its version with `kern version` (JSON) or
`kern --version`:
- a release archive's binary reports its tag, linked by `scripts/release.sh`;
- `go install github.com/dshills/kern/cmd/kern@v0.1.0` reports `v0.1.0`;
- `go build` in a checkout reports the tag on a tagged commit and a Go
  pseudo-version otherwise. The commit carries `-dirty` when the checkout has
  any change, untracked files included (Go's `vcs.modified`), so a local
  build in a tree with a stray file says `-dirty` even when the tracked
  files are clean.

## Steps

1. Move the `## [Unreleased]` entries into a new `## [vX.Y.Z] - YYYY-MM-DD`
   section of `CHANGELOG.md`, and add its link reference at the end. List
   any change to `spec/rules.lock` and any machine-API version added or
   dropped.
2. Check the section and the build:

   ```bash
   scripts/release-check.sh vX.Y.Z /tmp/notes.md
   ```

   ```bash
   scripts/check-reproducible.sh vX.Y.Z
   ```

   `scripts/release.sh vX.Y.Z` builds the archives into `dist/` (it needs
   GNU tar: `brew install gnu-tar` on macOS).
3. Commit, and push `main`.
4. Wait for the `ci` workflow to pass on that commit. The release workflow
   refuses a commit without a successful `ci` run on a push, so pushing the
   tag together with `main` fails.
5. Tag and push the tag:

   ```bash
   git tag -a vX.Y.Z -m "kern vX.Y.Z"
   ```

   ```bash
   git push origin vX.Y.Z
   ```

6. When the `release` workflow finishes, check the release page: four
   archives and `SHA256SUMS`, and the notes from the changelog. Download one
   archive, check it with `shasum -a 256 -c SHA256SUMS --ignore-missing`,
   and run `kern --version`.

## When the workflow fails

A failure before the "Create the draft release" step creates nothing: fix
the cause, delete and re-push the tag if the fix needs a new commit, or
re-run the workflow.

A failure in "Create the draft release" or "Publish the release" leaves the
remote state uncertain. Look at it with `gh release view vX.Y.Z`:

| What exists | What to do |
|---|---|
| no release | re-run the workflow |
| a draft | `gh release delete vX.Y.Z`, then re-run the workflow |
| a public release with four archives and `SHA256SUMS` | nothing |
| a public release missing any asset | `gh release delete vX.Y.Z` (the tag stays), then re-run the workflow |

Assets are never uploaded by hand: every archive and `SHA256SUMS` come from
one run of the workflow.
