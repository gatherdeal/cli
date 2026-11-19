# Adding a service binding

The only API inventory is `contracts/manifest.json`. A service's public
contract is exported into this repository by GatherDeal's contract
generator: it merges the service/version into the shared manifest and writes
`contracts/<service>/<version>/openapi.json` and
`internal/generated/<service>/<version>`. Commit those artifacts unchanged.
Do not hand-write API clients, invent operations, or fetch contracts at
runtime. The CRM, Identity and WhatsApp bundles are installed.

### Manifest v2 release pins

The bundles were regenerated from the pinned producer commits with
`GOWORK=off`. The CRM candidate uses utilities
`v0.0.0-20261119192914-fb05de4996af`. Identity and WhatsApp use
`v0.0.0-20261119035543-33746fdc2708`.

| Producer | Pinned commit | Public operations |
| --- | --- | ---: |
| CRM | `4128f9aa2cf1fe73681784a90b5f45781c9dd141` | 12 |
| Identity | `b7e955aa5161c83957b4b23b8639604b3bad0818` | 1 |
| WhatsApp | `201d745162307b09762dba466f585cbaefc83fa2` | 34 |

The CRM producer pin points to the local `chore/utilities-public-filter` branch
until the CRM revision is released.

All OpenAPI documents are embedded automatically. Contract checks enumerate
all bundles, verify disk/embedded/generated digests, service/version constants
and generated Client methods, and require one command per published operation.
Command bindings reference a public ID. `Command.Contract()` exposes typed
effects, permissions, outlet policy and idempotency for metadata and skills;
API `ReadOnly` is derived from the effect. Catalog and skills derive from the
installed commands. A top-level presentation is a `CommandPresentation{Path: ...}` on
that binding: `outlets list` belongs to `identity.outlets.list`.
The CLI renders underscores as hyphens in command path segments.
There is no second inventory and presentation never changes service ownership.
`outlets get` is a convenience lookup through the published list's ID/code
filters, not an additional public operation. WhatsApp agent creation resolves
outlet codes through the same scoped list during execution. Offline previews show the unresolved code.

Every command needs a one-sentence `Summary` that says what it does in plain
language (for example "List CRM contacts in the outlets this access may
read."), and every command group needs an entry in `groupSummaries`
(`internal/cli/tree.go`). Tests enforce both. These summaries are the agent
skill's indexes: an app's group summary is its line in the `SKILL.md` app
index, and each resource's group summary, with its verbs, is a line in
`references/<app>.md`. The skill picks up a new app or resource without other
changes, and `gatherdeal commands <app> <resource>` serves its flags.
Guidance an agent needs for one app only (workflows, pairing, write caveats)
goes into that app's entry in `appNotes` (`internal/cli/agent_context.go`),
which heads `references/<app>.md` and `gatherdeal agent-context <app>`;
`AgentContext` holds only guidance that applies to every app.

## CRM v0.4.0 release gate

The producer's public compatibility check against the CLI `v0.3.0` contract
exits 1 for these 11 removed field/operator pairs on `crm.contact_logs.list`.

| Field | Removed operators | Retained operators |
| --- | --- | --- |
| `contact_id` | `NEQ`, `NOT_IN` | `EQ`, `IN` |
| `create_at` | `BETWEEN`, `EQ`, `GT`, `IN`, `LT`, `NEQ`, `NOT_IN` | `GTE`, `LTE` |
| `id` | `NEQ`, `NOT_IN` | `EQ`, `IN` |

A v0.4.0 release with this surface requires the CLI owner's explicit acceptance
of all 11 removals as a breaking release, a discovery `min_cli_version` of
`0.4.0`, and gated regeneration against the preserved `v0.3.0` contract with
the producer's `--public-major` flag. The branch carries the development candidate.

## Service discovery

Compiled production discovery derives paths from installed bundles and
advertises identity, CRM, and WhatsApp. Custom discovery (for `--server`
profiles) parses and validates every `services` entry, with same-origin paths
and no fallback to guessed URLs. A server must advertise a service in its
discovery document, on its own public origin, before that service's commands
can be used against it. Credentials are never sent cross-origin.

## Thin typed adapters

Use the origin-bound authenticated HTTP client with `httpx.ResponseDoer` to
add the outlet selector and retain correlation IDs/raw problems. Generated
problem structs convert explicitly to `httpx.Problem`; preserve the original
error as the cause for generated typed accessors. Generated status/protocol
errors project their typed fields into `httpx.StatusError`/`ProtocolError`.
The shared HTTP and auth packages do not depend on any generated package.

A list adapter maps its generated input/output into `paging.Walk[T]`'s fetch
callback. Set page=0 and total=none for traversal; use the manifest maximum
when page size is omitted. The walker reduces the requested size to remaining
allowance, caps every walk at `paging.MaxItems` (10000) items and limit+8
requests, rejects missing/repeated cursors, discards partial data on failures,
and retains a resumable cursor only at a complete page boundary. Overfilled
capped pages deliberately omit it. Use `pageFlags(operation, resource)` and
`validatePaging` for the --after/--page/--page-size/--total/--all/--limit
contract; `inv.walk()` says whether to walk and with which cap. Lists whose
API takes a search term (`q`) add the shared `flagSearch`. `--all` never
changes outlet scope. The CRM conformance and golden tests pin this
behavior.

Set `Command.Record` to a zero value of the generated record type on list and
get commands: it adds `--fields` and publishes `record_fields` in the catalog.
`--jq` is added to every JSON command automatically. Shaping happens once in
`Run` after the handler succeeds, so handlers never see it, and failures are
never shaped.

## Writes

Attach `WritePolicy` to the command: `ConfirmConfig` supplies `--yes`, and
`ConfirmSend` supplies `--send`. The write policy also supplies `--dry-run`
and adds `--idempotency-key` for keyed operations. Register `--input` only for
operations that accept a request body. Generated bindings declare body support
and required query flags. Help and the command catalog use the same flag
declarations as the parser.

Missing confirmation returns exit 9 without dispatch. Dry runs require a
separate Preview handler and never invoke Run. Both handlers must validate
concrete generated input and scope. Use `ReadBody[GeneratedInput]` for bounded
inline, file or stdin (`--input -`) JSON. Unknown members, null and trailing
documents are rejected.

Idempotency support is taken from the manifest. Keyed writes generate a random
key if omitted; `meta.write` reports the key and whether it was generated on
success, preview and failure. The binding must pass that exact key to the API.
Confirmation is mistake prevention, not authorization; previews do not reserve
resources. The binding is responsible for server scope/revision preconditions.

After dispatch may have begun, ambiguous transport/response failures must use
`output.UnknownWriteOutcome`: `CLI_WRITE_OUTCOME_UNKNOWN`, exit 8,
retryable=false, with the original cause retained. Cancellation does not erase
this classification. Definite server rejections use ordinary problem mapping.
There are no automatic retries. Even a transient failure or an idempotency
key is not evidence that replay is safe: reconcile the outcome and rely on the
server's declared replay guarantee. These policies apply to CRM writes and to
WhatsApp configuration and message writes.

## Tech debt

The recovery filename for a generated idempotency key can be lost on power
failure after server acceptance. Windows flushes the temporary file and then
uses an ordinary rename. Use a write-through rename before dispatch. On Unix,
first use with an environment token can create the config directory without
syncing its entry in its parent. Sync newly created parent directory entries
before dispatch. Verify both cases on their respective platforms.

Agent creation with an outlet code opens a WhatsApp client and then opens an
Identity client for the lookup. Each resolves the profile independently. A
concurrent profile rebind can therefore resolve the code under a different
credential from the one used for creation. Bind both service clients to the
same profile and credential when revisiting cross-service writes.

Some published success schemas make the result optional. For example,
`BroadcastOutput` permits a missing or null `broadcast`. The producer contract
would need to require that member for schema validation to distinguish a
missing broadcast from a valid receipt.
