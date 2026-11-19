# Use multiple GatherDeal services

Log in once, then use the same profile for CRM, Identity, and WhatsApp
commands. Your granted permissions determine which commands and outlets you
can access.

```sh
gatherdeal auth login --profile company-a
gatherdeal outlets list --profile company-a
gatherdeal crm contacts list --profile company-a
gatherdeal whatsapp agents list --profile company-a
```

For a profile on a custom server, pass `--server <url>` when you log in. The
server's discovery document must advertise each service you want to use, and
its service endpoints must be on the server's origin.

See [Profiles and request context](../README.md#profiles-and-request-context)
for profile options.
