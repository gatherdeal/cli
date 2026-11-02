# gatherdeal CLI

`gatherdeal` lets you, or an AI agent working for you, read and manage your
GatherDeal business data from a terminal. It reads outlets and CRM contacts
and manages WhatsApp API agents and messages, limited to the outlets and
access you approve. Each invocation operates
in one organization; use named profiles (`--profile`) for separate organizations.

You log in by approving a code on the GatherDeal approval page in your
browser. The CLI never sees your password.

## Quick start

### For people

```sh
npm install -g @gatherdeal/cli@latest --ignore-scripts
gatherdeal auth login
gatherdeal outlets list
gatherdeal crm contacts list --limit 10
```

`auth login` prints an approval page address and a code; open the page,
check the code and approve. Other ways to install are under
[Install](#install).

### For AI agents

Paste this prompt into your agent (Claude Code, Codex or another agent):

```text
Install the GatherDeal CLI and its agent skill for me, then help me log in.

1. Install the CLI: npm install -g @gatherdeal/cli@latest --ignore-scripts
   (without npm, follow https://github.com/gatherdeal/cli#install).
2. Check it: gatherdeal version
3. Install the agent skill for yourself, for all my projects:
   - Claude Code: gatherdeal skills install --target claude --scope user
   - Codex or another agent that reads ~/.agents/skills:
     gatherdeal skills install --target agents --scope user
4. Read the usage rules: gatherdeal agent-context
5. Log me in: run gatherdeal auth login --no-wait and show me the approval
   link and code exactly as printed. I approve it myself in my browser; never
   approve it for me and never ask for my password. When I say it is done,
   run gatherdeal auth login --continue, then gatherdeal auth status, and tell
   me which organization, outlets and permissions I granted.
```

## Install

**npm** (Linux, macOS and Windows on x64 and arm64). To pin an exact
version, install it with lifecycle scripts disabled (the packages have none):

```sh
npm install -g @gatherdeal/cli@<version> --ignore-scripts
```

**Release archive.** Download the archive for your platform from the
[releases page](https://github.com/gatherdeal/cli/releases) and put the
`gatherdeal` binary in the documented location:

- Linux and macOS: `~/.local/bin/gatherdeal` (make sure `~/.local/bin` is on
  your `PATH`);
- Windows: `%LOCALAPPDATA%\Programs\gatherdeal\gatherdeal.exe`.

**From source** (Go 1.27.3 or newer):

```sh
go install github.com/gatherdeal/cli/cmd/gatherdeal@<version>
```

### Verify a release

Every release is built and signed by `.github/workflows/release.yml` on its
`v<version>` tag, with Sigstore keyless signing (no long-lived key). Each
release carries `release.json` (version, archive names, sizes and SHA-256 of
every archive and of the binary inside it) and `checksums.txt`, each with a
`.sigstore.json` bundle, plus GitHub build provenance for every archive. npm
packages are published with provenance from the same workflow.

```sh
gh attestation verify gatherdeal_<version>_linux_amd64.tar.gz --repo gatherdeal/cli
cosign verify-blob release.json --bundle release.json.sigstore.json \
  --certificate-identity "https://github.com/gatherdeal/cli/.github/workflows/release.yml@refs/tags/v<version>" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Update

The CLI never checks for updates on its own. Every form of `update` is
read-only: it writes no local or remote state, and TUF disk caching is disabled.
It only reports installation instructions; there is no `--yes` flag. Ask it:

```sh
gatherdeal update --check                   # is there a newer release? (read-only)
gatherdeal update                           # show how to install the latest release
gatherdeal update --version <version>       # an exact release (pre-releases too)
gatherdeal update --version <version> --allow-downgrade
```

Before it says anything about a release, the CLI verifies that release's
signed `release.json` against the pinned release identity above: the
signature must come from the release workflow at exactly the tag of the
version it describes. A release that fails verification is reported as
`CLI_UPDATE_VERIFY_FAILED` (exit 1) and nothing else.

How you update depends on how you installed; `data.install_method` and
`data.action` in the JSON output say which applies:

| Install | What `gatherdeal update` does |
|---|---|
| npm, global (`npm_global`) | Prints the exact `npm install -g …` command (`action.argv`); exit 9. The CLI never runs npm. |
| npm, project dependency (`npm_local`) | Prints the command and the project directory; exit 9. |
| npx (`npx`) | Nothing to do: `npx @gatherdeal/cli@<version>` runs that version. |
| `go install` (`go_install`) | Prints the `go install …@v<version>` command; exit 9. |
| Release archive in the documented location (`release_binary`) | Prints the verified download URL and SHA-256; exit 9. |
| Anything else (`unknown`, `unwritable`, `dev`) | Prints instructions; exit 9. It never uses `sudo` or changes permissions. |

Every install action's `instructions` end with a reminder to reinstall any
installed agent skill afterwards (see [Install an agent skill](#install-an-agent-skill)).

This version never replaces its own binary. If a server answers
`CLIENT_UPGRADE_REQUIRED` (exit 9), this version is too old for it: run
`gatherdeal update`.

## Log in

```sh
gatherdeal auth login
```

The CLI prints an approval page address and a code, for example:

```
To approve this login, open the approval page on PASSPORT.GATHERDEAL.AI:
    https://passport.gatherdeal.ai/device
and check that it shows this code:
    BCDF-GHJK
```

Open the page, sign in, check that the code matches, then choose the
organization, the outlets, what the access may do and how long it lasts (30,
90, 180 or 365 days, or "never"). The CLI suggests **Read only**. Choose
**Read and write** to create, change or delete WhatsApp agents, and turn on
**Allow sending messages** to send WhatsApp messages; without them those
commands are refused with exit 4. Every access also stops working after 30
days without use.
Only approve a code you started yourself; never approve a code someone sent
you.

Useful flags:

| Flag | Meaning |
|---|---|
| `--lifetime 30d\|90d\|180d\|365d\|never` | Suggest a lifetime; you choose the final one on the approval page. |
| `--device-name <label>` | Label shown on the approval page (default: this machine's name). |
| `--credential-store keyring\|file` | Where to keep the credential (see below). Default `keyring`. |
| `--profile <name>` | Keep several logins side by side. |
| `--no-wait` / `--continue` | Two-step login for AI agents: `--no-wait` prints the code in the JSON output and returns (exit 9); after approval, `--continue` stores the credential. |

Logging in again renews the access: the new access is stored and the old one
is revoked.

## Check and end the access

```sh
gatherdeal auth status   # organization, outlets, permissions, expiry, server
gatherdeal auth logout   # revoke on the server, then delete locally
```

`auth status` shows only a hint of the token (for example `gd_dat_…a1b2`),
never the token itself. You can also see and revoke accesses on the approval
site under Security › Sessions & devices.

## Read contacts

App API commands follow `gatherdeal <app> <resource> <verb>`. CRM contacts
live under `crm`; `auth`, `commands`, `agent-context`, `skills`, `update`, and `version`
stay top-level, as do `outlets list/get`. Use `gatherdeal --help`, `gatherdeal crm --help`, or
`gatherdeal crm contacts --help` to explore each level. The `commands` JSON
includes an `app` field for API commands, and `gatherdeal commands crm contacts`
limits it to one resource (see [Command catalog](#command-catalog)).

```sh
gatherdeal crm contacts list --search ali --page 1 --page-size 20
gatherdeal crm contacts list --search ali --after <next_cursor>
gatherdeal crm contacts list --tag VIP --limit 500
gatherdeal crm contacts list --tag VIP --all
gatherdeal crm contacts get <contact-id> --with-emails --with-tags
```

`crm contacts list` returns one page. `meta.page_info` carries the API's page
fields: `page_size`, `has_next_page`, `has_prev_page`, and when present
`next_cursor`, `prev_cursor`, `page`, `total_items` and `total_pages`. Get
the next page with `--page N` or, cheaper and past the page-number depth
limit, with `--after <next_cursor>` and otherwise unchanged flags. Stop when
`has_next_page` is `false`, not on a short or empty page. `--total exact`
or `--total none` asks for, or skips, the row count (by default it is
counted with page numbers and not with cursors).

`--all` walks forward through every page with cursors (100 contacts per
request unless `--page-size` is given; `--after` resumes a walk) and returns
all contacts in one `data` array. `--limit N` (1 to 10,000) returns at most N
contacts, following cursors across pages as needed; it walks on its own and
caps `--all` when both are given. Without `--limit`, `--all` stops at the
hard cap of 10,000 contacts. A walk that stops at its cap sets
`meta.page_info.has_next_page` to `true` if more exist. Each request uses
the smaller of the page size and remaining allowance. At a page boundary,
`meta.page_info.next_cursor` is the server's last next cursor; resume with
`--limit N --after <next_cursor>` (or `--all --after <next_cursor>`) and the
same filters and sort. Walks cannot be combined with `--page` or `--total`.
If the server ignores the requested size and the cap cuts a page, the
result contains only N contacts and omits next_cursor: the server cursor
would skip unseen rows. Such a result cannot be resumed safely; restart
the traversal instead of guessing a cursor.
A walk allows at most N + 8 page requests (10,008 for an uncapped `--all`).
This bounds empty pages even with advancing cursors; exceeding the budget
fails with `CLI_PROTOCOL_ERROR` (exit 1), returning no contacts. **The walk is a live
traversal, not a snapshot:** contacts created, changed or deleted while it
runs may be missed or returned out of order. If a page fails, the command
fails and returns no contacts.

`crm contacts list` and `crm contacts get` accept `--outlet <id>` to read from one of
the approved outlets, and `--with-phones`, `--with-emails`, `--with-tags` and
`--with-attributes` to include those details. A contact outside your
approved outlets is reported as not found.

## Read outlets

```sh
gatherdeal outlets list --all
gatherdeal outlets list --code HQ --with-owner --with-organization
gatherdeal outlets get HQ
gatherdeal outlets get <outlet-id>
```

`outlets list` reads the authorized outlets through the identity API's outlet
list. It supports the same bounded pagination as CRM, plus repeatable
`--id` and `--code` filters, `--name`, `--search`, `--is-master`, `--sort`, and
`--order`. `--is-master=false` selects non-master outlets.

`outlets get` resolves an exact UUID or code through that same scoped list.
An absent or out-of-scope outlet returns `CLI_OUTLET_NOT_FOUND` (exit 5).
Keep the same profile; never retry a denial or not-found in another organization.

## WhatsApp API

These commands manage WhatsApp API agents. They do not create messaging inbox
channels; those are a separate system. Registered agents consume paid quota
slots. Deleting an agent unlinks its device and releases
its slot asynchronously.

```sh
gatherdeal whatsapp agents list --all
gatherdeal whatsapp agents get <agent-id>
gatherdeal whatsapp agents create --outlet <outlet-id-or-code> --phone +60123456789 --yes --no-wait
gatherdeal whatsapp agents pairing <agent-id>
gatherdeal whatsapp agents update <agent-id> --input update.json --yes
gatherdeal whatsapp agents delete <agent-id> --yes
gatherdeal whatsapp messages list --agent <agent-id> --limit 100
gatherdeal whatsapp messages get <message-id>
gatherdeal whatsapp messages create --input message.json --send
```

Create returns pairing instructions under `data.action`, the agent ID,
`expires_at`, `pairing_url` and an optional `pairing_code` formatted XXXX-XXXX.
With `--no-wait` it returns immediately, exit 9 while pending. Otherwise it
waits at most five minutes or until expiry. `agents pairing` checks once:
pending → 9, connected → 0, expired/failed → 6. A bounded wait that ends while
still pending returns the same instructions and exit 9. It never requests a
replacement code. The QR code refreshes on the pairing page.

The page URL uses the passport origin from discovery and the organization code
from the profile: `/whatsapp/pairing/<agent_id>?org=<code>`. Missing context
stops creation before dispatch; log in to establish the profile. Environment
tokens have no cached profile binding.

An update body includes the agent's `update_at` as `revision`, and optional
`mode` (`resident` or `on_demand`) and `outlet_id` (UUID or null). For example:

```json
{"revision":"2026-10-29T10:00:00Z","mode":"on_demand"}
```

A message body names one agent and recipient; creating it sends the message:

```json
{"agent_id":"55555555-5555-5555-5555-555555555555","to":"+60123456789","type":"text","text":{"body":"Hello"}}
```

Message status describes runtime acceptance, not recipient delivery.
`--input` accepts inline JSON (starting with `{` or `[` after whitespace), a file path, or `-` for stdin. Writes accept `--dry-run` for a validated
preview without dispatch. Configuration writes require `--yes`; message
creation requires `--send`. Agent and message creation accept
`--idempotency-key`; when omitted the CLI generates one and reports it in
`meta.write`. Preserve that key. `CLI_WRITE_OUTCOME_UNKNOWN` (exit 8) means the
write may have succeeded: reconcile before retrying. There are no automatic
write retries.

Lists use the same bounded cursor pagination as CRM, including `--all`,
`--limit`, and `--after`. WhatsApp lists have no server-side search. Keep the same profile and outlet selection.

Agent creation accepts an outlet UUID or code with `--outlet`. Codes are
resolved through the authorized outlets list before preview or execution;
the resolved UUID is used for the request body, outlet selection and pairing
polls. A missing or out-of-scope code stops creation with exit 5. JSON input
`outlet_id` remains a UUID; when combined with `--outlet`, both must resolve to
the same outlet.
Custom discovery must advertise identity and WhatsApp; the CLI never guesses
missing paths.

## Output

Every command except `agent-context`, `skills show`, and `--help` prints exactly one JSON
document on stdout, for success and failure alike:

```json
{"schema_version":1,"status":"success","data":[…],
 "meta":{"server":"https://api.gatherdeal.ai","profile":"default","correlation_id":"…",
         "page_info":{"page_size":20,"has_next_page":true,"has_prev_page":false,
                      "next_cursor":"…","page":1,"total_items":42,"total_pages":3},
         "credential_expires_at":"2027-01-22T10:00:00Z"},
 "exit_code":0}
```

```json
{"schema_version":1,"status":"error","data":null,"meta":{…},
 "error":{"code":"CRM_CONTACT_NOT_FOUND","message":"Contact not found.",
          "details":{"contact_id":"…"},"http_status":404,"correlation_id":"…","retryable":false,
          "problem":{"type":"https://docs.gatherdeal.ai/errors/CRM_CONTACT_NOT_FOUND",
                     "title":"Contact not found.","status":404,"code":"CRM_CONTACT_NOT_FOUND",
                     "retryable":false,"correlation_id":"…","details":{"contact_id":"…"}}},
 "exit_code":5,"hint":"The contact does not exist or is outside the outlets this access may read."}
```

Server failures arrive as RFC 9457 problem documents
(`application/problem+json`). `error.problem` is the document as received
(credentials redacted). `error.code`, `error.details` and `error.retryable`
come from it, and `error.message` and `error.detail` are its `title` and
`detail`, printed as the server wrote them, with control characters and
terminal escape sequences removed. Branch on `error.code` and read
`error.details`; never parse `message` or `detail`. A failure without a
problem document (a gateway page, an empty body) has the code
`CLI_UPSTREAM_ERROR` and no `problem`.

`meta.credential_expires_at` is `null` for an access that never expires.
Within 7 days of expiry, `meta.credential_expiry_warning` is `true` and a
renewal reminder is printed on stderr. Human-readable messages and login
instructions go to stderr only.

### Shaping the output

Every command that prints a JSON envelope takes `--jq <expr>`; list and get
commands also take `--fields a,b,c`. Both apply only to a successful result.

```sh
gatherdeal crm contacts list --search ali --fields id,name
gatherdeal crm contacts list --limit 50 --jq '.[].id'
gatherdeal outlets get HQ --jq '{id, name}'
gatherdeal whatsapp messages list --agent <agent-id> --fields id,status --jq '.[] | select(.status == "error") | .id'
```

- `--fields` keeps only the named top-level members of each record: the
  `data` object of a get, or each element of the `data` array of a list. The
  envelope (`meta`, `page_info` and so on) stays. A record that lacks a field
  (empty values are omitted) leaves it absent. An unknown name is a usage
  error (exit 2) that lists the available names; `record_fields` in
  `gatherdeal commands` lists them too.
- `--jq` evaluates a [jq](https://jqlang.org/manual/) expression, with an
  embedded implementation (gojq), against `data`, not the envelope. Its
  results replace the envelope: one per line, strings raw (like `jq -r`;
  terminal control characters are escaped, line feeds and tabs kept) and
  other values as compact JSON. The expression cannot read the environment.
- With both, `--fields` applies first, then `--jq`.
- Failures are never filtered: a command that fails prints its normal error
  envelope and exit code, whatever `--jq` or `--fields` say. Check the exit
  code before reading `--jq` output.
- An invalid `--jq` expression or `--fields` list is a usage error (exit 2,
  `CLI_USAGE`) before any request is sent.
- If the expression fails on a successful result (for example `.name` on an
  array), the command exits 2 with `CLI_JQ_FAILED`; that error envelope's
  `data` holds the unfiltered result and `meta` is complete, because the
  command itself, including any write, succeeded. Fix the expression; never
  repeat a write to re-filter it.

### Language

Server error text (`error.message`, `error.detail`) comes in English, Malay,
Simplified or Traditional Chinese. Every command that talks to the server
takes `--lang <tag>` (`en`, `ms`, `zh-Hans`, `zh-Hant`, a regional tag such
as `ms-MY` or `zh-TW`, or an Accept-Language list such as `fr, ms;q=0.9`).
Without it the CLI uses the first of `LC_ALL`, `LC_MESSAGES` and `LANG` that
is set (`ms_MY.UTF-8` means Malay; `C` and `POSIX` mean English), and English
otherwise. Error codes, details and the rest of the output never change with
the language.

`gatherdeal agent-context` prints short instructions for AI agents, and
`gatherdeal agent-context <app>` prints one app's notes and resource index.
`gatherdeal version` works offline.

### Command catalog

`gatherdeal commands` prints every command, flag, exit code and environment
variable as JSON. Narrow it instead of reading the whole catalog:

```sh
gatherdeal commands crm                       # every command of an app
gatherdeal commands crm contacts              # one resource
gatherdeal commands whatsapp agents create    # one command
gatherdeal commands --search pairing          # by text, across apps
gatherdeal commands whatsapp --search send    # by text, within an app
```

A path prefix matches whole segments. An unknown prefix exits 2, and its
message lists the valid next segments. `--search` matches the command path,
the command summary and the summary of the resource that holds the command,
ignoring case; no match is a success with an empty `commands` array. The
filtered output keeps the same envelope and schema, including the complete
`exit_codes` and `environment` sections.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Unclassified failure or protocol violation, including any HTTP redirect |
| 2 | Invalid command, flag or input; `CLI_SKILL_PATH_INVALID` for a symlink or unexpected kind at a skill path (`error.details.path`: absolute path); `CLI_JQ_FAILED` when a `--jq` expression fails on a successful result (`data` holds the unfiltered result) |
| 3 | Not logged in, or the access expired, went unused for 30 days or was revoked: log in again |
| 4 | Not allowed by this access (permission, outlet, scope); retrying does not help |
| 5 | Not found, or outside the approved outlets |
| 6 | Conflict or other business error; `CLI_SKILL_CONFLICT` for differing skill files without `--force` (`error.details.paths`: all relative conflicting file paths; `error.details.dir`: absolute install directory). Re-run with `--force` to overwrite only if the person agrees. |
| 7 | Rate limited; try again later |
| 8 | Network, TLS or timeout failure, or a server error |
| 9 | A person must act first: approve the login, confirm a server, choose a credential store, pair WhatsApp, confirm a write, or install an update |
| 10 | Partial completion (reserved) |
| 130 | Interrupted |

For a server failure, the exit code derives from the HTTP status of the
problem: 400 → 2, 401 → 3, 403 → 4, 404 → 5, 409, 412, 422 and other 4xx →
6, 429 → 7, 5xx → 8. One code overrides its status: `CLIENT_UPGRADE_REQUIRED`
(this CLI version is too old) → 9, with the hint to run `gatherdeal update`.
`retryable` means the failure condition is transient; it never means that
repeating a command that changes state is safe. For a server problem it
is the server's own verdict (rate limiting and temporary
unavailability); without a problem document only 502, 503 and 504 are
retryable. A response that breaks the problem contract (for example a
problem with a success status) is a protocol violation, exit 1.

## For AI agents

Run `gatherdeal agent-context` and follow it, and run
`gatherdeal agent-context <app>` before using an app. In short: start the login with
`gatherdeal auth login --no-wait`, show the person the approval page and code
exactly as printed, and run `gatherdeal auth login --continue` after they
approve. Never pass `--server` or `--accept-server-risk` unless the person
typed them, and never paste a token into a chat. Never run `gatherdeal
update` unless the person asked; it only reports the command to run, which
you show rather than execute. To set everything up for a person, use the
[Quick start prompt](#for-ai-agents).

For WhatsApp, use `whatsapp agents create --outlet <id-or-code> --yes --no-wait`.
Add `--phone +60123456789` for phone-number pairing. Show the person
`data.pairing_url`, `data.expires_at` and `data.pairing_code` (when present),
with the **Link with phone number instead** instruction. Phones require the
leading `+`. Never request a new code or repeat create while they type it.
Afterward use `whatsapp agents pairing <agent_id>` to check once.

### Install an agent skill

Choose an explicit location; there is no default:

```sh
gatherdeal skills install --dir /path/to/skills
gatherdeal skills install --target claude --scope project
gatherdeal skills install --target claude --scope user
gatherdeal skills install --target agents --scope project
gatherdeal skills install --target agents --scope user
gatherdeal skills show
```

`--dir PATH` writes to `PATH/gatherdeal/`. The targets write to a directory
relative to the current directory (`--scope project`) or to the OS home
directory (`--scope user`):

| Target | Project scope | User scope | Read by |
|---|---|---|---|
| `claude` | `./.claude/skills/gatherdeal/` | `$HOME/.claude/skills/gatherdeal/` | Claude Code |
| `agents` | `./.agents/skills/gatherdeal/` | `$HOME/.agents/skills/gatherdeal/` | Codex and other agents that read the shared agent-skills directory |

Use exactly one of `--dir` or `--target`. `--scope project|user` is required
with `--target` and cannot be used with `--dir`. Target destinations resolve
ancestor symlinks and must stay inside the resolved project/home directory,
otherwise they fail with `CLI_SKILL_PATH_INVALID`. A symlinked base is allowed.
Explicit `--dir` follows ancestor symlinks because you chose that location.

Installation writes only `SKILL.md` and one `references/<app>.md` per app
(for example `references/crm.md`). It makes
no network requests. Identical files are unchanged; differing files cause
an error before any files are written unless `--force` is supplied. It
refuses symlinked skill directories and target files. New directories use
0755 and files use 0644, with each file replaced using a temporary file and
rename. Other files in the directory are left alone.

The success envelope contains the absolute `dir`, `cli_version`, and a
`files` array with each relative `path` and `action` (`created`, `updated`,
or `unchanged`). `skills show` prints the same `SKILL.md` as plain text.

The skill is layered so an agent reads a short index at each level.
`SKILL.md` holds the guidance that applies to every app, an app index (each
app, what it does and its reference file) and the core commands; it is the
same text `gatherdeal agent-context` prints. Each `references/<app>.md` holds
the app's notes and its resources with a summary and their verbs, the same
text `gatherdeal agent-context <app>` prints. Exact arguments, flags and
record fields come from `gatherdeal commands <app> <resource>` at the moment
they are needed. All of it is generated from the command registry.

The skill records the CLI version it was made for in its metadata
(`cli_version`). After the CLI changes version, re-run the same install
command (same `--target` and `--scope`, or `--dir`) with `--force` to refresh
the skill; the skill tells agents to do this when `gatherdeal version` reports
a different version. Otherwise agents run `gatherdeal skills install` only
when the person asks for it.

## Credentials and security

- **Default store: the OS keyring** (macOS Keychain, Windows Credential
  Manager, Linux Secret Service), service `gatherdeal-cli`. The CLI checks the
  keyring works *before* starting the login. If it does not, the CLI stops
  and tells you how to choose the file store; it never falls back silently.
- **File store** (`--credential-store file`): one file per login under
  `$XDG_CONFIG_HOME/gatherdeal/credentials/` (default
  `~/.config/gatherdeal/credentials/`) or `%APPDATA%\gatherdeal\credentials\`
  on Windows. The directory is `0700` and files are `0600`, owned by you
  (on Windows: owned by and accessible to your account only; in an elevated
  session the Administrators group also counts as the owner). The CLI
  refuses broader permissions, other owners, symlinks, and any location
  inside a git work tree. **`0600` does not protect against other programs
  running as your own user**; prefer the keyring where one is available.
- Profile settings (no secrets) live in `profiles.json` next to the
  credentials directory, mode `0600`. Each configuration directory keeps its
  own credentials, even when several (different `XDG_CONFIG_HOME`) share one
  OS keyring. `auth logout` also cancels a login of that profile that is
  still waiting for approval.
- A credential is bound to the server it was issued by and is only ever sent
  to that server's origin. The CLI never follows HTTP redirects and talks
  HTTPS only (plain HTTP only to `localhost`).
- The CLI makes no network calls except the ones the command you ran needs.
  There is no telemetry and no automatic update check. `gatherdeal update`
  contacts only `github.com` (and GitHub's release-asset hosts) for release
  metadata and the Sigstore TUF repository for the current trusted root
  (falling back to the copy built into the CLI); it sends no credential to
  either.

### Profiles and request context

```sh
gatherdeal auth login --profile company-a
gatherdeal auth login --profile company-b
gatherdeal auth profiles list
gatherdeal crm contacts list --profile company-a --outlet <outlet-id>
```

The first successful login pins the profile to immutable organization and
principal IDs. Codes and names are display metadata. Renewal must match both
IDs, including login started with `--no-wait` and resumed with `--continue`.
A mismatch returns `CLI_PROFILE_ORGANIZATION_MISMATCH` or
`CLI_PROFILE_PRINCIPAL_MISMATCH` (exit 2), with typed `profile`, `expected_id`
and `actual_id` details. The old credential remains committed; the rejected
new grant is sent through the late-token revocation and cleanup path.
Login requires a successful status response identifying the principal and
matching the issued grant. To deliberately retarget, run
`gatherdeal auth logout --profile company-a`, then log in to that profile again.

`auth profiles list` reads only local metadata: profile, server/issuer,
`binding` (organization ID/code/name and principal ID), and `cached_grant`
(including outlet IDs). `cached_at` and `cache_age_seconds` describe the
login-time summary; missing values mean unknown. This is not a live grant
check; use `auth status --profile NAME` for that.

Authenticated results include typed `meta.context`: `profile`, `server`,
`issuer`, cached `binding.organization` (`id`, optional `code`/`name`) and
`binding.principal_id`, plus `request.outlet_ids`. The array is always present
when context exists: `[]` requests the authorized set, while `[ID]` requests
that outlet. It is **requested selection**, never server-confirmed effective
scope. Missing `binding` means unknown; environment tokens never inherit the
stored profile's binding. Missing context means no authenticated context was
established. Cached authorized outlet IDs appear in the grant summary, not
in the request selection.

Keep the same profile and outlet selection across pagination, retries,
login continuation and multi-step work. Never retry a denial or not-found
in another organization or profile.

The profile store uses schema 1. Every binding requires both organization
and principal IDs. Unsupported schemas are refused for both reads and writes
with `CLI_PROFILE_SCHEMA` (exit 9); use a CLI supporting that schema and do
not delete credentials.

### `GATHERDEAL_TOKEN`

For CI and other unattended use you can set `GATHERDEAL_TOKEN` to a
delegated-access token (`gd_dat_…`). It is used only for the built-in
production profile, never stored, never printed, and cannot be combined with
`--server` or `--profile`. An invalid value is an error; the CLI does not
fall back to a stored credential.

### Non-default servers

`--server <url>` connects to a GatherDeal server other than
`https://api.gatherdeal.ai`. The CLI then prints a warning and asks you to
confirm; without a terminal it requires `--accept-server-risk <exact origin>`.
All commands with `--server`/`--profile` accept `--accept-server-risk`; it only
has an effect during `auth login`.
Only do this for a server you chose yourself: a malicious server can show a
fake approval page. Credentials from such a server are kept in a separate
profile and are never sent anywhere else.

The built-in production profile talks to `https://api.gatherdeal.ai` and
expects the OIDC issuer `https://api.gatherdeal.com/oidc`. For every
`--server` profile the issuer must be exactly `<server>/oidc`, and the server
and every endpoint of its discovery document must be on the `--server` origin.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for building, testing, the API
contract bundle and the release process.

## License

Copyright 2026 Enlliance Management Sdn Bhd. Licensed under the
[Apache License, Version 2.0](LICENSE); see [NOTICE](NOTICE).
