# GatherDeal CLI

Read and manage your GatherDeal business data from the terminal or an AI agent.
This package launches the platform-specific `gatherdeal` binary.

## Quick start

### For people

```sh
npm install -g @gatherdeal/cli@latest --ignore-scripts
gatherdeal auth login
gatherdeal outlets list
gatherdeal crm contacts list --limit 10
```

`auth login` prints an approval page address and a code; open the page,
check the code and approve.

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

## Usage

```sh
gatherdeal auth status
gatherdeal crm contacts list --search ali
gatherdeal crm contacts get <contact-id> --with-emails --with-tags
```

App API commands use `gatherdeal <app> <resource> <verb>`. Authentication,
`commands`, `agent-context`, `skills`, `update`, and `version` stay top-level.

```sh
gatherdeal --help
gatherdeal crm --help
gatherdeal crm contacts --help
gatherdeal commands crm contacts
gatherdeal commands --search pairing
gatherdeal agent-context
```

`commands` returns JSON command metadata, including `app` for API commands.
A path prefix (`crm`, `crm contacts`) or `--search TEXT` narrows it; without
either it lists every command.
See the [repository README](https://github.com/gatherdeal/cli#readme) for
login options, pagination, output envelopes, and exit codes.

## Agent skill

Install only when the person asks (or to refresh it, see below), with an
explicit location:

```sh
gatherdeal skills install --dir /path/to/skills
gatherdeal skills install --target claude --scope project
gatherdeal skills install --target claude --scope user
gatherdeal skills install --target agents --scope project
gatherdeal skills install --target agents --scope user
gatherdeal skills show
```

`--dir PATH` installs to `PATH/gatherdeal/`. `--target claude` (Claude Code)
installs to `.claude/skills/gatherdeal/` and `--target agents` (Codex and
other agents that read the shared agent-skills directory) to
`.agents/skills/gatherdeal/`, under the current directory with
`--scope project` or the OS home directory with `--scope user`. Choose exactly
one of `--dir` or `--target`; `--scope` is required with `--target` and
rejected with `--dir`. There is no default location.

Installation is offline and writes only `SKILL.md` (general guidance, app
index and core commands, as `gatherdeal agent-context` prints them) and one
`references/<app>.md` per app (its notes and resource index, as
`gatherdeal agent-context <app>` prints them); exact flags come from
`gatherdeal commands <app> <resource>`. Identical files remain unchanged. Differing files
fail before any writes unless `--force` is given; symlinked skill files and
subdirectories are refused. With --target, resolved destinations must stay
inside the resolved project/home base. Explicit --dir follows ancestor symlinks.
Success returns a JSON envelope with the absolute `dir`, `cli_version`, and
per-file `path` and `action` (`created`, `updated`, or `unchanged`).
`skills show` prints the skill as plain text. The skill records the CLI
version it was made for (`cli_version`); after the CLI changes version,
re-run the install command with the same location and `--force`. The skill
tells agents to do this when `gatherdeal version` reports a different version.


## Updates

`gatherdeal update --check` checks for a release; `gatherdeal update` reports
installation instructions (exit 9 when an external install is needed).
All forms are read-only, with no disk cache or self-replacement and no
`--yes` flag. Use `--version <version>` to select an exact release.
