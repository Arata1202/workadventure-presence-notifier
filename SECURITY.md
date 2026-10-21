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

Do not include Slack bot tokens, production configuration, IP addresses, room URLs, or real user presence data in the report.

The maintainer will review the report, coordinate remediation and disclosure, and publish a security advisory when appropriate. Response times are best effort; this project does not currently provide a formal security SLA.

## Security boundaries

This service connects directly to WorkAdventure's internal, unencrypted gRPC endpoint. Keep that endpoint on a trusted private network and do not expose it publicly.

Slack bot tokens are secrets. Provide them only through the `SLACK_BOT_TOKEN` environment variable, keep them out of configuration files and logs, and rotate them immediately if they are exposed.
