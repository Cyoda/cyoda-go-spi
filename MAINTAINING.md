# Maintaining cyoda-go-spi

This document is the canonical reference for releasing `cyoda-go-spi` and
governing changes to its public surface. It complements `CONTRIBUTING.md`
(which covers contributing changes) and `KNOWN_CONSUMERS.md` (which lists
projects that depend on this module's stability).

## Release process

`cyoda-go-spi` is a small interface module with no release artefacts other
than the Git tag itself. We do not use a `release/` branch — the repo is
small enough that direct-to-main is appropriate.

### When to tag

**Tag at end-of-milestone, not per-PR.** cyoda-go-spi and cyoda-go ship in
lock-step: cyoda-go-spi vX.Y.Z is cut when *every* SPI change targeted at
the corresponding cyoda-go vX.Y.Z milestone has been merged into
cyoda-go-spi `main`. Cutting earlier creates a tag that doesn't represent
a usable milestone release — and tag immutability means we can't fix it
after the fact without breaking the convention (see the v0.8.0 retraction
note in CHANGELOG.md for the one case where this happened).

If unsure whether more SPI changes are coming for the current milestone,
do not tag. cyoda-go pins to a pseudo-version against `main` while the
milestone is in flight; the pin bump to the published tag is the final
step.

### 1. Cut a release

From an up-to-date `main`, when the milestone is complete:

1. Make sure all merged-since-last-tag changes are reflected in `CHANGELOG.md`'s `[Unreleased]` section.
2. As a final preparation commit, rename `[Unreleased]` to `[X.Y.Z] - YYYY-MM-DD` and push to `main`.
3. Create the annotated, signed tag:
   ```bash
   git tag -s -a vX.Y.Z -m "Release vX.Y.Z"
   git push origin vX.Y.Z
   ```
4. Verify the tag is annotated + signed:
   ```bash
   git verify-tag vX.Y.Z
   ```
   Expected: signature verifies. (Lightweight tags will print `cannot verify a non-tag object of type commit`.)

### 2. Notify known consumers

Every entry in `KNOWN_CONSUMERS.md` should be notified of the new tag,
especially if the release contains breaking changes or deprecations.

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
- Breaking changes are permitted in **any** pre-1.0 release, minor or patch,
  iff:
  - The change is called out in `CHANGELOG.md` under `### Breaking` with
    explicit migration notes.
  - Where feasible, deprecated symbols carry `// Deprecated: <reason>`
    comments for at least one prior release before removal.
  - Each consumer listed in `KNOWN_CONSUMERS.md` has been notified
    before the breaking PR is merged. The notification is linked from
    the PR description.
- The version component is therefore not a breakage signal pre-1.0 — the
  `### Breaking` changelog section is. Consumers must read it on every bump
  rather than inferring safety from the number.

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
