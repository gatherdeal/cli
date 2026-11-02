# Contributing to the GatherDeal CLI

Thank you for helping. Bug reports and suggestions are welcome as
[GitHub issues](https://github.com/gatherdeal/cli/issues); pull requests are
welcome too. Only maintainers merge pull requests and cut releases.

Never include a real token (`gd_dat_…`), credential file or customer data in
an issue, pull request, test or log.

## Build, test and lint

You need Go 1.27.3 or newer and
[golangci-lint](https://golangci-lint.run/) v2.15.0. Node.js 24 is needed
only for the release tooling tests.

```sh
go build ./...
go vet ./...
go test -race ./...
golangci-lint run ./...
go mod tidy          # must leave go.mod and go.sum unchanged
node --test scripts/release.test.mjs
```

Pull requests run the same checks in CI: build, vet and tests on Linux, macOS
and Windows, a cross-compile of every release target, `go mod tidy` and
golangci-lint. Every check must pass before a maintainer merges.

Tests run offline against in-process fake servers; none of them needs a
GatherDeal account or network access. The release tooling tests do not
publish or contact GitHub or npm.

## Generated files

- **Goldens.** The JSON envelope and command catalog goldens under
  `internal/cli/testdata/` are rewritten with
  `go test ./internal/cli -update`. Review the diff before committing it.
- **Agent skill.** The skill is generated from `AgentContext` (guidance for
  every app), `appNotes` (guidance for one app), `Commands()` and
  `groupSummaries`, all in `internal/cli`; there is no checked-in copy. To
  inspect it, run
  `go run ./cmd/gatherdeal skills install --dir <explicit-output-directory>`,
  which writes `gatherdeal/SKILL.md` (general guidance, app index and core
  commands) and one `gatherdeal/references/<app>.md` per app (its notes and
  resource index). `gatherdeal agent-context` and
  `gatherdeal agent-context <app>` print the same texts. The indexes list
  resources and verbs only; per-command details stay in
  `gatherdeal commands <app> <resource>`. The skill tests verify these files
  against the shared sources.
- **Sigstore trusted root.** `go run ./cmd/releasecheck trusted-root` fails
  when the trusted root built into the CLI (`internal/update/trusted_root.json`)
  is stale; refresh it with `go run ./cmd/releasecheck trusted-root -write`.

## API contracts

`contracts/` holds the public contract of every API the CLI calls:
`manifest.json` (operations, permissions, and the expansions and edge filters
a delegated access may use) and one OpenAPI document per service version.
`internal/generated/<service>/<version>/` is the client generated from it,
standard library only. Both are exported by GatherDeal's contract generator,
and `contracts/testdata/problem-conformance/` holds the decode fixtures that
ship with them. Never edit these files by hand; maintainers update them from
the owning services. `contracts/contracts.go`, which embeds them, and its
tests are hand-written.

The commands derive their app, command path and declared flags from the
manifest, including the `--with-*` flags of `crm contacts list` and
`crm contacts get`. The tests of `internal/httpx` and `internal/crm` run every
conformance fixture. `go test ./internal/contractcheck` checks that each
generated package's `BundleDigest` matches the committed contract, that every
manifest operation has exactly one command, that command flags stay within the
manifest, and that generated code imports only the standard library.

See [Adding a service binding](docs/multi-service.md) for how commands bind
to the contract, service discovery, shared pagination and write policies.

## Compatibility rules

- Persisted and published formats are versioned: the profile store
  (`profiles.json`), credential files, discovery documents and the signed
  `release.json`. Unknown schema versions, unknown restriction versions and
  unsupported API versions are refused, never guessed. Any new persisted field
  raises the schema version so an older binary cannot silently discard it.
- `release.json` fields may be added, never removed or redefined.
- The release identity (this repository and `.github/workflows/release.yml`)
  is pinned in `internal/update`. Moving the repository or renaming the
  workflow needs a transition release that trusts both identities.
- Commands have no aliases. Unrecognized commands and non-problem error
  responses are rejected, so they never acquire compatibility meaning.
- Minimum CLI versions (`CLIENT_UPGRADE_REQUIRED`) and the update
  upgrade/downgrade checks protect older installs.

## Packaging

`npm/` holds the npm packaging: a launcher package (`@gatherdeal/cli`) plus
one package per platform, with no install scripts. The launcher sets
`GATHERDEAL_LAUNCHER=npm` for the binary, which `gatherdeal update` uses to
confirm an npm install. GoReleaser (`.goreleaser.yaml`) builds the archives.

## Releases

Maintainers release by pushing a `v<version>` tag. That runs
`.github/workflows/release.yml` in a protected environment that a maintainer
approves before anything is signed or published:

1. GoReleaser builds the archives and `checksums.txt`.
2. `releasecheck manifest` writes `release.json`; cosign signs it and
   `checksums.txt` keylessly with Sigstore, and every archive gets GitHub
   build provenance.
3. `releasecheck` verifies every archive and binary and runs the candidate;
   a smoke matrix (Linux, macOS and Windows on x64 and arm64) verifies the
   same assets and runs `version`, `commands` and `agent-context`. The
   signed assets are kept as the run's `release-assets` artifact.
4. The publish job creates a GitHub draft from those verified assets, then
   publishes the npm platform packages in parallel, with npm provenance.
   Every package must install, pass `npm audit signatures` and match this
   workflow run; the job waits (up to about 10 minutes per package) for the
   registry to serve a newly published version. The launcher is published
   last, only after every platform package verifies.
5. Only after every npm package verifies does the GitHub release become
   public (`latest` for stable releases only).

Every step is safe to re-run in the original tag-push run: existing GitHub
assets and npm versions must match the original bytes and are never replaced.
A mismatch fails at once; a version the registry still does not serve when
the wait runs out fails with a message that a re-run is safe. To recover from
a partial release, re-run the failed jobs of that original run. Never move a tag, delete an npm version, overwrite release assets or
start a replacement run; if the original assets cannot be recovered, abandon
that version and release a new tag.
