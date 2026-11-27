# Contributing to the GatherDeal CLI

Install Go 1.27.3 or newer and golangci-lint v2.15.0. Run the release
tooling tests on Linux or macOS with Node.js 26 and OpenSSL.

## Build, test and lint

```sh
go build ./...
go vet ./...
go test -race ./...
golangci-lint run
node --test scripts/release.test.mjs
```

## Generated files

- Go API clients are generated from the published OpenAPI documents in
  `contracts/`. When updating an API version, update the published documents
  and their matching generated files together.
- Regenerate CLI test snapshots with `go test ./internal/cli -update` and
  review the diff.

## Pull requests

Describe the change and how you tested it. Include or update tests for
behavior changes and keep `go.mod` and `go.sum` tidy. Use sample data in
issues, pull requests and tests. Keep real credentials and customer data out
of submissions.
