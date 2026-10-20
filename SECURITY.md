# Security Policy

## Supported versions

Until the first stable release, security fixes are applied to the latest commit on the `main` branch.

| Version | Supported |
| --- | --- |
| Latest `main` | Yes |
| Older commits | No |

After the first stable release, the latest stable version will be supported unless a release note states otherwise.

## Reporting a vulnerability

Do not report security vulnerabilities in a public issue, pull request, or discussion.

Use the repository's **Security** tab and select **Report a vulnerability** when private vulnerability reporting is available. If that option is unavailable, contact the maintainer through a private channel before sharing technical details.

Include the following information when possible:

- A description of the vulnerability and its potential impact
- The affected version or commit
- Reproduction steps or a minimal proof of concept
- Relevant deployment details, such as the WorkAdventure version
- Suggested mitigations, if known

Do not include Slack access tokens, production configuration, IP addresses, room URLs, or real user presence data in the report.

The maintainer will review the report, coordinate remediation and disclosure, and publish a security advisory when appropriate. Response times are best effort; this project does not currently provide a formal security SLA.

## Security boundaries

This service connects directly to WorkAdventure's internal, unencrypted gRPC endpoint. Keep that endpoint on a trusted private network and do not expose it publicly.

Destination credentials are secrets. For the included Slack adapter, provide the bot token only through the `SLACK_BOT_TOKEN` environment variable, keep it out of configuration files and logs, and rotate it immediately if exposed. Grant only `chat:write` plus `channels:history` for a public target channel or `groups:history` for a private target channel. Keep the app in that channel; bounded history access is required to recover ambiguous posts without creating duplicates. Persistent state uses a strictly validated, destination-neutral schema containing a destination ID, opaque message references, operation IDs, and attempt times, but no display names, rendered text, or name-derived hashes. Unknown fields, invalid invariants, and unsupported versions stop startup.
