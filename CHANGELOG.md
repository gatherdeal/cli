# Changelog

## 0.6.0 (2026-11-26)

Use the new command names in scripts and saved prompts.

| Deprecated command | Replacement |
| --- | --- |
| `gatherdeal crm contact-tags assign` | `gatherdeal crm contact-tags add` |
| `gatherdeal whatsapp broadcasts recipients delete` | `gatherdeal whatsapp broadcasts recipients remove` |
| `gatherdeal whatsapp broadcast-suppressions delete` | `gatherdeal whatsapp broadcast-suppressions remove` |

The old commands still work and print a deprecation warning to stderr.
They will be removed in a future major release.
