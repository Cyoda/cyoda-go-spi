# Contributing to cyoda-go-spi

cyoda-go-spi defines the stable storage-plugin contract that cyoda-go and third-party plugins build against. Contributions that strengthen the contract — clearer docs, better conformance test helpers, bug fixes — are welcome.

## Before you open a PR

- **Stability first.** Every exported symbol in this module is observable to every plugin that depends on it. We add to the surface reluctantly and remove from it rarely. See the stability policy below.
- **Zero-dependency discipline.** The SPI depends on stdlib plus `google/uuid`. New dependencies need strong justification — anything that takes a `testing` import goes in a sub-package (e.g., `spitest/`, Plan 5).
- **Documentation is load-bearing.** Because plugin authors can't read our code, they read the doc comments. Every exported symbol needs one.

## Development workflow

The development methodology (TDD, trunk-based development, verification gates) is documented in [cyoda-go/CONTRIBUTING.md](https://github.com/cyoda/cyoda-go/blob/main/CONTRIBUTING.md). Follow the same process here — this is a sibling repo with the same conventions, not a separate project.

Quick commands:

```bash
go test ./... -v           # unit tests
go vet ./...
go test -race ./...        # race detector
```

## Stability policy

cyoda-go-spi follows semver.

- **Patch** (`v0.2.1`): internal refactor, bug fix in a helper, documentation.
- **Minor** (`v0.3.0`): new method on an interface with a default implementation, new optional interface, new exported constants or types. Backwards-compatible for existing plugins.
- **Breaking** (`v1.0.0`, etc.): anything that existing plugins need to change to remain compliant. Requires a coordinated release — we notify known consumers (currently `cyoda-go/plugins/{memory,postgres}` and `cyoda-go-cassandra`) and bump them at the same time.

Pre-1.0 (where we are now), minor versions can contain breaking changes, but we avoid them — the set of consumers is small enough that coordinated bumps are cheap.

## Reporting vulnerabilities

See [SECURITY.md](SECURITY.md).

## Conduct

See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
