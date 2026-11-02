# Adding a service binding

The only API inventory is `contracts/manifest.json`. A service's public
contract is exported into this repository by GatherDeal's contract
generator: it merges the service/version into the shared manifest and writes
`contracts/<service>/<version>/openapi.json` and
`internal/generated/<service>/<version>`. Commit those artifacts unchanged.
Do not hand-write API clients, invent operations, or fetch contracts at
runtime. The CRM, identity, and WhatsApp bundles are installed.

All OpenAPI documents are embedded automatically. Contract checks enumerate
all bundles, verify disk/embedded/generated digests, service/version constants
and generated Client methods, and require one command per published operation.
Command bindings reference a public ID. `Command.Contract()` exposes typed
effects, permissions, outlet policy and idempotency for metadata and skills;
API `ReadOnly` is derived from the effect. Catalog and skills derive from the
installed commands. A top-level presentation is a `CommandPresentation{Path: ...}` on
that binding: `outlets list` belongs to `identity.outlets.list`.
There is no second inventory and presentation never changes service ownership.
`outlets get` is a convenience lookup through the published list's ID/code
filters, not an additional public operation. WhatsApp agent creation resolves
outlet codes through the same scoped list before preview or execution.

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
`ConfirmSend` supplies `--send`. The framework registers typed --dry-run,
confirmation, --idempotency-key and --input flags. Missing confirmation returns
exit 9 without dispatch. Dry runs require a separate Preview handler and never
invoke Run. Both handlers must validate concrete generated input and scope;
use `ReadBody[GeneratedInput]` for bounded inline, file or stdin (`--input -`) JSON.
Unknown members, null and trailing documents are rejected.

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
server's declared replay guarantee. WhatsApp configuration and message writes
use these policies.
