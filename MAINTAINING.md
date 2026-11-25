# Maintaining cyoda-go-spi

This document is the canonical reference for releasing `cyoda-go-spi` and
governing changes to its public surface. It complements `CONTRIBUTING.md`
(which covers contributing changes) and `KNOWN_CONSUMERS.md` (which lists
projects that depend on this module's stability).

## Release process

`cyoda-go-spi` is a small interface module with no release artefacts other
than the Git tag and its GitHub Release. A release is cut from one of two
branches:

| Release | Cut from | Branch created from |
|---|---|---|
| Any release, when `main` holds only this release's changes | `main` | — |
| A patch, when `main` already holds work for the next minor | `release/vX.Y.Z` | the previous tag |

CI runs on `main` and `release/**`. This file covers the SPI's part of a
release. The coordinated flow with cyoda-go (pins, plugin tags, binary,
chart) is in cyoda-go's `MAINTAINING.md`, sections "Release at a glance" and
"Patch release while the next minor is open".

### When to tag

**Tag at end-of-milestone, not per-PR.** cyoda-go-spi vX.Y.Z is cut when
*every* SPI change that the cyoda-go milestone needs has been merged into the
release's branch. The SPI picks its version number on its own axis (see
[Deprecation policy](#deprecation-policy)); it need not equal the cyoda-go
version. Cutting earlier creates a tag that doesn't represent a usable
milestone release — and tag immutability means we can't fix it after the
fact without breaking the convention (see the v0.8.0 retraction note in
CHANGELOG.md for the one case where this happened).

If unsure whether more SPI changes are coming for the current milestone,
do not tag. cyoda-go pins a pseudo-version of a commit on the release's
branch while the milestone is in flight; the pin bump to the published tag
is the final step.

### 1. Cut a release

For a patch from a release branch, first create the branch at the previous
tag. Fix PRs target it, and each fix is forward-ported to `main` by
cherry-pick in its own PR:

```bash
# the patch and the previous tag
V=v0.9.1
PREV=v0.9.0
git fetch origin --tags
git branch "release/$V" "$PREV"
git push origin "release/$V"
```

When the milestone is complete, on the release's branch (`main` or
`release/vX.Y.Z`):

1. Make sure all merged-since-last-tag changes are reflected in `CHANGELOG.md`'s `[Unreleased]` section.
2. Apply the pending Dependabot bumps. Dependabot targets `main`; on a
   release branch, apply them by hand. Re-read each PR's current versions
   first: Dependabot moves its open PRs to newer releases.
3. As a final preparation commit, rename `[Unreleased]` to `[X.Y.Z] - YYYY-MM-DD`
   and push. Wait for CI to pass on that commit.
4. A maintainer with the signing key creates the annotated, signed tag at
   that commit:
   ```bash
   git tag -s -a vX.Y.Z -m "Release vX.Y.Z" <sha>
   git push origin vX.Y.Z
   ```
5. Verify the tag is annotated + signed:
   ```bash
   git cat-file -p vX.Y.Z | grep -q "BEGIN SSH SIGNATURE" && echo signed
   ```
   Expected: `signed`. A lightweight tag has no tag object and prints nothing.
   `git verify-tag vX.Y.Z` also checks the signature, but needs the
   allowed-signers file configured.
6. Create the GitHub Release, only after the tag exists (`gh release create`
   on a missing tag creates a lightweight, unsigned one):
   ```bash
   gh release create vX.Y.Z --repo cyoda/cyoda-go-spi --verify-tag \
     --generate-notes --notes-start-tag <previous tag> --latest
   ```
   `--notes-start-tag` makes the notes cover exactly the changes since the
   previous release. `--latest` puts the Latest mark on this release.
7. Check that the Go proxy and the checksum database serve the tag; the
   first command prints the version, the second its hashes:
   ```bash
   GOWORK=off go list -m github.com/cyoda/cyoda-go-spi@vX.Y.Z
   curl -fsS https://sum.golang.org/lookup/github.com/cyoda/cyoda-go-spi@vX.Y.Z
   ```

### 2. Notify known consumers

After the check in step 7 passes, notify every entry in
`KNOWN_CONSUMERS.md`, in dependency order: `cyoda/cyoda-go` first, then
`cyoda/cyoda-go-cassandra`, which also waits for the cyoda-go tags. Breaking
changes and deprecations are announced earlier as well: before the breaking
PR merges (see [Deprecation policy](#deprecation-policy)).

### 3. Merge a release branch back into `main`

After a release from `release/vX.Y.Z`, merge that branch into `main` with a
merge commit, never a squash or rebase merge:

```bash
git fetch origin
git switch -c "merge/vX.Y.Z" origin/main
git merge --no-ff origin/release/vX.Y.Z
```

Resolve the conflicts. In `CHANGELOG.md`, the forward-ported entries move from
`[Unreleased]` into the `[X.Y.Z]` section that the merge brings. Push, open a
PR into `main`, and merge it with
`gh api -X PUT repos/cyoda/cyoda-go-spi/pulls/<n>/merge -f merge_method=merge`.
Then delete `release/vX.Y.Z`.

Why a merge commit: Go derives a pseudo-version from the highest tag that is
an ancestor of the commit. Until `vX.Y.Z` is an ancestor of `main`, the
pseudo-versions of `main` are `vX.Y.Z-0.…` and sort below the tag, so a consumer
whose module graph also requires `vX.Y.Z` selects the tag and loses `main`'s
newer surface.

## Versioning

`cyoda-go-spi` follows Go module versioning rules. **Tags are immutable.**
sum.golang.org caches the SHA of every tag it serves, so tags cannot be
moved or reused. Each new release uses a strictly greater version than the
previous tag.

## Deprecation policy

This is a pre-1.0 module. The following rules govern how the public surface
evolves:

**Pre-1.0 (current era):**

- Both minor and patch releases may add to the public surface. New methods,
  types, or options can ship in either.
- A breaking change ships in a **minor** release, never in a patch. A change
  is breaking when it breaks an interface, or when it adds a conformance
  (`spitest`) case that an existing implementor fails. It is permitted iff:
  - The change is called out in `CHANGELOG.md` under `### Breaking` with
    explicit migration notes.
  - Where feasible, deprecated symbols carry `// Deprecated: <reason>`
    comments for at least one prior release before removal.
  - Each consumer listed in `KNOWN_CONSUMERS.md` has been notified
    before the breaking PR is merged. The notification is linked from
    the PR description.
- The minor component is therefore the breakage signal pre-1.0, and the
  `### Breaking` changelog section says what broke. `v0.8.3` and `v0.8.4`
  shipped breaking changes as patches, before this rule.
- `v0.9.1` is a breaking patch, as an exception: it backs out the
  rollback-only mark that `v0.9.0` added to `TransactionCoordinator` (a bug
  fix) and adds the conformance cases its `### Breaking` section lists.

**Post-1.0:**

- Standard semver applies.
- Breaking changes require a major version bump.
- Deprecated symbols carry `// Deprecated:` comments for at least one
  full minor release before removal.

## Fixing forward

Tags `v0.1.0` through `v0.7.0` are **lightweight** (commit-pointer) tags
by design — they are immutable per Go module checksum stability and we
do not retroactively modify them. Beginning with **v0.7.1**, all tags
are annotated and signed.

The new regime is forward-only: existing history stays as-is, new
releases follow the new rules. Reviewers and consumers should treat
this as a clean line drawn at v0.7.1, not a defect in the historical
tags.
