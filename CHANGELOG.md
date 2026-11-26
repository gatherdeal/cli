# Changelog

## Unreleased

- New batch commands write many CRM records from a JSON Lines or JSON array
  file: `crm contacts batch-create`, `crm contact-logs batch-create`,
  `crm contact-tags batch-create`, `crm contact-tags batch-add` and
  `crm contact-tags batch-remove`. Items are sent in atomic chunks of up to
  100, keyed `<key>.<n>`; every chunk is sent even when one fails, unless
  `--stop-on-error` is given. A chunk is resent unchanged, with the same
  key, after a network or server error and after a rate limit. The result
  reports each item as `succeeded`, `errored`, `uncommitted` or `unknown`.
  See "Batch writes" in the README.
- New `crm contact-tags create` creates an organization tag.
- Exit code 10 (`CLI_BATCH_INCOMPLETE`) now reports a batch command that
  committed some chunks and not others.

## 0.6.0 (2026-11-26)

Use the new command names in scripts and saved prompts.

| Deprecated command | Replacement |
| --- | --- |
| `gatherdeal crm contact-tags assign` | `gatherdeal crm contact-tags add` |
| `gatherdeal whatsapp broadcasts recipients delete` | `gatherdeal whatsapp broadcasts recipients remove` |
| `gatherdeal whatsapp broadcast-suppressions delete` | `gatherdeal whatsapp broadcast-suppressions remove` |

The old commands still work and print a deprecation warning to stderr.
They will be removed in a future major release.
