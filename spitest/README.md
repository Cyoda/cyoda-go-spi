# spitest — conformance test harness for storage plugins

`spitest` is a Go test harness that exercises the cyoda-go-spi contract
against a backend implementation. It is the canonical way for a storage
plugin to verify its compliance with a given SPI version.

## Using spitest in your plugin

In your plugin's tests, import the relevant `spitest` subpackages and
hand them a constructor that produces a fresh instance of your backend.
See `cyoda-platform/cyoda-go/plugins/memory` for an idiomatic example.

The harness covers the full SPI surface: entity persistence, audit,
async search, transactions, workflow plugin contracts, and key/value
extension hooks.

Groups that cover an *optional* interface — `Searcher`, for example —
skip themselves when your store does not implement it, by type
assertion. That is a conformant result, not a gap. Do not add a
`Harness.Skip` entry for such a group: `StoreFactoryConformance` treats
any `Skip` key that never matched as an error, so the entry would fail
your run.

## Conformance against latest SPI HEAD (recommended for plugin authors)

If you maintain a third-party storage plugin, add a nightly job to your
own CI that exercises `spitest` against the latest SPI `main`. This
catches contract regressions before SPI tags a release.

The snippet below is a ready-to-paste GitHub Actions workflow. Drop it
into your plugin repo at `.github/workflows/spi-head-conformance.yml`,
adjust `MY_PLUGIN_TEST_PATH` to the test directory in your repo that
exercises `spitest`, and you'll get a nightly check.

```yaml
name: SPI HEAD conformance

on:
  schedule:
    - cron: '0 6 * * *'  # daily 06:00 UTC
  workflow_dispatch:

permissions:
  contents: read

env:
  MY_PLUGIN_TEST_PATH: ./...    # adjust to your tests' import path

jobs:
  spi-head:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6.0.2
        with:
          path: plugin

      - uses: actions/checkout@v6.0.2
        with:
          repository: cyoda-platform/cyoda-go-spi
          ref: main
          path: spi-head

      - uses: actions/setup-go@v6
        with:
          go-version-file: plugin/go.mod

      - name: Replace SPI dep with HEAD
        working-directory: plugin
        run: |
          go mod edit -replace github.com/cyoda-platform/cyoda-go-spi=../spi-head
          go mod tidy

      - name: Run spitest against your backend
        working-directory: plugin
        run: go test -v $MY_PLUGIN_TEST_PATH

      - name: Restore go.mod (in case the job continues)
        working-directory: plugin
        run: |
          go mod edit -dropreplace github.com/cyoda-platform/cyoda-go-spi
          go mod tidy
```

If the nightly run goes red, please open an issue against
[cyoda-go-spi](https://github.com/Cyoda-platform/cyoda-go-spi/issues)
referencing the failing commit and your plugin's import path.

## Registering your plugin

If you'd like to be notified before SPI breaking changes ship, open a
PR adding your plugin to [`KNOWN_CONSUMERS.md`](../KNOWN_CONSUMERS.md).
The deprecation policy in [`MAINTAINING.md`](../MAINTAINING.md)
requires SPI maintainers to notify each registered consumer before a
breaking change merges.
