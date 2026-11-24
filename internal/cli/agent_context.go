package cli

import (
	"context"
	"fmt"
	"strings"
)

// AgentContext is the guidance that holds for every app. `gatherdeal
// agent-context` and SKILL.md print it followed by the app index and the core
// commands; see agentContext.
const AgentContext = `GatherDeal CLI: notes for AI agents

What it does
- Each invocation operates in one organization; use named profiles (--profile)
  for separate organizations. It reads and manages GatherDeal business data
  on behalf of the person who approved the login, within the approved outlets.
- Every command (except agent-context, skills show and help) prints exactly one JSON envelope on stdout:
  {"schema_version":1,"status":"success"|"error","data":…,"meta":…,"error":…,
   "exit_code":N,"hint":…}. Read stdout; stderr is for humans.
- App API commands use gatherdeal <app> <resource> <verb> (for example crm contacts list).

Finding commands
Look up commands one layer at a time instead of reading the whole catalog:
1. App: pick it from the app index at the end of this text.
2. Resource: read the app's notes and resource index (references/<app>.md
   in the skill, or ` + "`gatherdeal agent-context <app>`" + `). Follow the
   app's notes as well as these rules.
3. Command: ` + "`gatherdeal commands <app> <resource>`" + ` (for example
   ` + "`gatherdeal commands crm contacts`" + `) prints the exact arguments,
   flags, record_fields and read_only of each verb, plus the exit codes.
   When filter_fields is present, use its allowed --filter operators for the
   listed root fields, including rules inside nested boolean groups.
4. Run it.
- When the app is unknown, ` + "`gatherdeal commands --search TEXT`" + ` lists the
  commands whose path, summary or resource summary contains TEXT
  (case-insensitive). It combines with a prefix, and no match is an empty list.
- An unknown prefix exits 2 and its message lists the valid next segments.
- ` + "`gatherdeal commands`" + ` without a prefix prints the whole catalog; prefer
  a prefix.

Shaping output (saves tokens)
- --jq EXPR (every JSON command) runs a jq expression on data, not on the
  envelope, and prints only its results, one per line: strings raw, other
  values as compact JSON. Example: crm contacts list --limit 50 --jq '.[].id'.
- --fields a,b,c (commands with record_fields) keeps only those top-level fields
  of each record and keeps the envelope; record_fields in
  ` + "`gatherdeal commands <app> <resource>`" + ` lists the names. Unknown names exit 2. With --jq,
  fields apply first.
- Errors are never filtered: on failure you get the normal error envelope
  and exit code, so always check the exit code first. A bad --jq expression
  or --fields name exits 2 before any request.
- CLI_JQ_FAILED (exit 2): the command succeeded but the expression failed
  on its data; data holds the unfiltered result. Fix the expression and do
  not repeat a write.

Paging (paginated record lists)
- data is an array of records; meta.page_info has page_size, has_next_page,
  has_prev_page and, when present, next_cursor, prev_cursor, page,
  total_items, total_pages. Continue with --after <meta.page_info.next_cursor>;
  stop when has_next_page is false, never on a short or empty page.
- --limit N returns at most N records (1-10000), following cursors across
  pages; --all walks every page (with --limit, at most N; without, at most
  the hard cap of 10000). Prefer --limit.
  It is a live traversal, not a snapshot: records changed during the walk
  may be missed or seen out of order. Not with --page or --total.
  Requests shrink to the remaining allowance. When has_next_page is true,
  resume with --after <meta.page_info.next_cursor> and the same filters/sort.
  If the server overfills a page and the cap cuts it, next_cursor is omitted
  to avoid skipping unseen records; restart instead of guessing a cursor.
  Walks allow N + 8 requests (10008 for --all without --limit), then fail
  with CLI_PROTOCOL_ERROR and no records, even if empty-page cursors advance.
- --jq output has no meta, so when you must know whether more records exist
  (meta.page_info.has_next_page), use --fields, which keeps the envelope.

Writes
- Commands that change data need confirmation: configuration writes take
  --yes and sends take --send. --input takes a typed body as inline JSON,
  a file path, or - for stdin.
- --dry-run validates and previews without dispatch. Keyed creates report
  the exact idempotency key in meta.write; keep it with the result.
  For an interrupted write with a generated key, recover it from
  write-<invocation_id>.json in the CLI config directory using the ID on stderr.
  CLI_WRITE_OUTCOME_UNKNOWN (exit 8) means reconcile before repeating a write.
  A retryable error never authorizes replay.

Logging in
1. Run ` + "`gatherdeal auth login --no-wait`" + `. It exits 9 and the envelope's
   error.details holds verification_uri, user_code and approval_host.
2. Show the person the approval page URL and the code exactly as given, and
   ask them to approve it themselves in their browser. Never approve it for
   them and never ask for their password. The page suggests read-only
   access: if the task changes data, such as WhatsApp agents, ask them to
   choose "Read and write"; if it sends messages, to turn on "Allow sending
   messages".
3. After they confirm, run ` + "`gatherdeal auth login --continue`" + `.
4. Run ` + "`gatherdeal auth status`" + ` and tell them which organization,
   outlets and permissions the access covers.

Rules
- Keep the same --profile and --outlet across pagination, retries, login
  initiation/--continue/status, and multi-step work. Never retry a denial
  or not-found in another organization or profile.
- Use auth profiles list to inspect local bindings and cached grant freshness
  offline. auth status checks current access with the server.
- A successful login pins the profile to organization and principal IDs.
  A mismatch fails with CLI_PROFILE_ORGANIZATION_MISMATCH or
  CLI_PROFILE_PRINCIPAL_MISMATCH (exit 2); the old credential stays intact.
  To deliberately retarget, auth logout --profile NAME, then log in again.
- A successful auth logout reports data.logged_in=false for the local profile.
  data.revoked and data.credential_deleted report the actions taken.
- meta.context.binding is the cached profile identity, not effective scope.
  meta.context.request.outlet_ids is the requested selection; [] means the
  authorized set. An agent-create preview with an unresolved outlet code
  reports meta.context.request.outlet_code. Missing binding means unknown
  (including GATHERDEAL_TOKEN).
  No context means no authenticated context was established.
- Only run gatherdeal skills install when the person asks for it, or to
  refresh an installed skill as below.
- Skill freshness: an installed skill records the CLI version it was made
  for in its metadata (cli_version). If ` + "`gatherdeal version`" + ` reports a
  different version (data.version), reinstall the skill before using its
  command paths (for example, crm contact-tags list). Run gatherdeal skills
  install with the same --target and --scope (or --dir) it was installed
  with, plus --force.
- With --target, resolved paths must stay inside the resolved project/home base.
  Explicit --dir follows ancestor symlinks.
- On CLI_SKILL_CONFLICT from any other install, show the person the paths
  and only use --force if they agree.
- Never pass --server or --accept-server-risk unless the person typed that
  exact flag and server themselves. The default server is the right one.
- Never print, log or paste a token into chat, files or commits. The CLI only
  ever shows a hint such as gd_dat_…abcd; do not try to read the credential
  store or the GATHERDEAL_TOKEN variable.
- Do not choose --credential-store file on your own; if the keyring is
  unavailable (exit 9), ask the person first.
- Never run ` + "`gatherdeal update`" + ` (other than ` + "`gatherdeal update --check`" + `) unless the
  person asked you to. All update forms are read-only (no disk cache), and
  there is no --yes flag. It never installs anything by itself: it returns
  the command (error.details.action.argv) for the person to run. Show it;
  do not run it unless they ask.
- Never run ` + "`gatherdeal uninstall --yes`" + ` (with or without --local-only)
  unless the person explicitly asked to remove the CLI. Run
  ` + "`gatherdeal uninstall`" + ` first and show them the plan; it changes nothing.
- On CLIENT_UPGRADE_REQUIRED (exit 9) the server refuses this CLI version:
  tell the person and show the hint (` + "`gatherdeal update`" + `).
- Exit 3 means log in again (expired, unused for 30 days, or revoked).
  Exit 4 means the access does not allow it; do not retry. Exit 5 means not
  found or outside the approved outlets.
- error.retryable means the failure condition is transient; it never means
  repeating a command that changes state is safe.
`

// appNotes is the guidance specific to one app, keyed by its top-level
// command group. It heads the app's resource index (appReference).
var appNotes = map[string]string{
	"crm": `- gatherdeal crm contacts list [--search TEXT] [--page N | --after CURSOR]
  [--page-size N] [--tag NAME] returns contacts with the general paging rules.
- gatherdeal crm contact-tags list --search <name> --fields id,name finds tag IDs.
- gatherdeal crm contacts get <id> [--with-phones] [--with-emails]
  [--with-tags] [--with-attributes] adds the related records it names.
- gatherdeal crm contact-logs list --contact-id <id> lists notes for one contact.
- gatherdeal crm sales-pipelines list --with-stages finds pipeline and stage IDs.
  If CRM_SALES_PIPELINE_STAGE_LIMIT occurs, omit --with-stages to list pipelines.
- gatherdeal crm deals list [--pipeline-id UUID] [--stage-id UUID] [--search TEXT]
  narrows the deal list. Use gatherdeal crm deals get <id> for one deal.
- CRM writes require the matching CRM write approval (contacts or deals)
  during auth login and use --dry-run to preview or --yes to execute.
- contacts create, contact-tags assign, contact-logs create and deals create
  use a key from meta.write.idempotency_key. For a retry of the same operation,
  pass that key with --idempotency-key; IDEMPOTENCY_KEY_CONFLICT means the key
  was used for other input. Use a fresh key to reassign a tag after removal.
- gatherdeal crm deals create --outlet <id> --input <json> --yes creates a deal
  in that outlet from {"pipeline_id":"…","stage_id":"…"} plus optional type,
  priority and remark; CRM names it and returns data.id. Without --outlet, CRM
  uses the access's only deal-write outlet or fails with
  CRM_DEAL_OUTLET_REQUIRED.
- deals move-stage and deals update need the deal's current revision. Read
  data.revision with gatherdeal crm deals get <id> (--jq .revision prints it)
  and pass it unchanged, quotes included, as --if-match: for example
  --if-match '"r1"'. A successful move or update returns data.revision for the
  next write. PRECONDITION_FAILED (exit 6) means the deal changed since that
  read: read it again before deciding whether to repeat the change.
- deals update --input takes priority, type and remark: omit a member to keep
  it, null clears it. deals move-stage --input takes {"stage_id":"…"} from
  the deal's own pipeline.
`,
	"outlets": `- gatherdeal outlets list [--code CODE] [--id UUID] [--search TEXT] --limit N
  reads only authorized outlets, with the general paging rules.
- gatherdeal outlets get <id-or-code> resolves through the scoped list.
  Exit 5 means absent or out of scope; never retry in another profile.
`,
	"whatsapp": `- These agents serve the WhatsApp API. They do not appear as messaging inbox
  channels; messaging channels are a separate system.
- gatherdeal whatsapp chat-messages list --agent-id UUID --after-rev 0 --count 100
  polls the chat messages. Read data.messages and save data.after_rev for the
  next poll with the same agent and outlet. While data.has_more is true, poll
  again immediately. Use --jq to select members of this revision poll.
- Create only when asked: gatherdeal whatsapp agents create --outlet <id-or-code>
  [--phone <E.164>] --yes --no-wait. Phones need the leading +.
- agents create --outlet accepts a UUID or code. Dry runs show the unresolved
  outlet_code. Execution resolves the code and keeps the UUID selected during pairing.
  A not-found code stops creation. JSON input outlet_id must be a UUID and,
  when combined with --outlet, must identify the same outlet.
- Exit 9 with data.action means a person must pair. Show data.pairing_url,
  data.expires_at and, when present, data.pairing_code (XXXX-XXXX), with the
  "Link with phone number instead" instruction. Never request a new pairing
  code or repeat create while the person is typing one.
- Check once with gatherdeal whatsapp agents pairing <agent_id>: pending
  repeats instructions and exits 9; connected exits 0; expired/failed exits 6.
  Without --no-wait, create waits at most five minutes or until pairing expires.
- Pairing needs the passport origin from discovery and the organization code
  from the profile. Missing context stops creation; log in to establish it.
- agents update requires revision from the agent's update_at. agents delete
  unlinks the device and releases its quota slot asynchronously.
- messages create sends to one recipient. Message status describes runtime
  acceptance, not recipient delivery.
`,
}

// agentContext is the general guidance, the app index and the core
// commands: the output of `gatherdeal agent-context` and the body of SKILL.md.
func agentContext(root *commandNode) string {
	var b strings.Builder
	b.WriteString(AgentContext)
	b.WriteString("\nApps\nRead an app's notes and resource index before choosing one of its commands.\n")
	for _, app := range skillApps(root) {
		fmt.Fprintf(&b, "- %s: %s Notes and resources: %s or `gatherdeal agent-context %s`.\n", app.path[0], app.summary(), appReferencePath(app), app.path[0])
	}
	b.WriteString("\nCore commands\n")
	for _, name := range root.choices() {
		n := root.children[name]
		if n.app != "" {
			continue
		}
		if n.command != nil {
			fmt.Fprintf(&b, "- %s: %s\n", n.command.Name(), n.summary())
		} else {
			writeResourceIndex(&b, n)
		}
	}
	return b.String()
}

// appReference is one app's notes and resource index: the content of
// references/<app>.md and of `gatherdeal agent-context <app>`.
func appReference(app *commandNode) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# gatherdeal %s\n\n%s\n", app.path[0], app.summary())
	if notes := appNotes[app.path[0]]; notes != "" {
		fmt.Fprintf(&b, "\nNotes\n%s", notes)
	}
	first := app
	for first.children[first.choices()[0]].command == nil {
		first = first.children[first.choices()[0]]
	}
	group := strings.Join(first.path, " ")
	fmt.Fprintf(&b, `
Resources
Each line is a resource path, its summary and its verbs. A command is the
resource path followed by a verb, for example `+"`gatherdeal %s %s`"+`.
Before running a command, read its arguments, flags, record_fields,
filter_fields (allowed root filter operators) and read_only with `+"`gatherdeal commands <resource>`"+`, for example
`+"`gatherdeal commands %s`"+`.
`, group, first.choices()[0], group)
	writeResourceIndex(&b, app)
	return b.String()
}

// writeResourceIndex writes one line for n and each group below it that
// holds commands: its path, summary and verbs.
func writeResourceIndex(b *strings.Builder, n *commandNode) {
	var verbs, groups []string
	for _, name := range n.choices() {
		if n.children[name].command != nil {
			verbs = append(verbs, name)
		} else {
			groups = append(groups, name)
		}
	}
	if len(verbs) > 0 {
		fmt.Fprintf(b, "- %s: %s Verbs: %s.\n", strings.Join(n.path, " "), n.summary(), strings.Join(verbs, ", "))
	}
	for _, name := range groups {
		writeResourceIndex(b, n.children[name])
	}
}

func runAgentContext(_ context.Context, inv *Invocation) (any, error) {
	root := commandTree(Commands())
	if len(inv.Args) == 0 {
		return agentContext(root), nil
	}
	var names []string
	for _, app := range skillApps(root) {
		if app.path[0] == inv.Args[0] {
			return appReference(app), nil
		}
		names = append(names, app.path[0])
	}
	return nil, usageError("unknown app %q; valid apps: %s", inv.Args[0], strings.Join(names, ", "))
}
