# Known consumers of cyoda-go-spi

This file lists projects that depend on `cyoda-go-spi` and have asked to
be notified before breaking changes ship. Inclusion is opt-in — open a
PR to add an entry. The notification etiquette for breaking-change PRs
is documented in [MAINTAINING.md](MAINTAINING.md#deprecation-policy).

## How to add your project

Open a PR adding an entry below in this format:

```
- **org/repo** — claims compliance with vX.Y.Z; contact @handle (issues / DM)
```

Maintainers will merge if the entry is well-formed.

## Current consumers

- **cyoda/cyoda-go** — in-tree storage plugins (memory, postgres, sqlite); claims compliance with v0.7.0+; contact @cyoda maintainers via repo issues.
- **cyoda/cyoda-go-cassandra** — Cassandra storage plugin; claims compliance with v0.6.0+; contact @cyoda maintainers via repo issues.
