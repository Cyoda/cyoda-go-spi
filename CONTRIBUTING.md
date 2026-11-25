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

Pre-1.0, a breaking change ships in a minor, and additive changes ship in a minor or a patch. See [MAINTAINING.md](MAINTAINING.md#deprecation-policy).

## Reporting vulnerabilities

See [SECURITY.md](SECURITY.md).

## Conduct

See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
