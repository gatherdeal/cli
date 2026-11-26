# Changelog

## 0.6.1 (2026-11-26)

- `gatherdeal status`, and the account check that runs after
  `gatherdeal login`, now use the identity `/v1/access-context` endpoint.
  Upgrade before the old endpoint is retired: earlier versions will then
  fail to report sign-in status.

## 0.6.0 (2026-11-26)

Use the new command names in scripts and saved prompts.

| Deprecated command | Replacement |
| --- | --- |
| `gatherdeal crm contact-tags assign` | `gatherdeal crm contact-tags add` |
| `gatherdeal whatsapp broadcasts recipients delete` | `gatherdeal whatsapp broadcasts recipients remove` |
| `gatherdeal whatsapp broadcast-suppressions delete` | `gatherdeal whatsapp broadcast-suppressions remove` |

The old commands still work and print a deprecation warning to stderr.
They will be removed in a future major release.
