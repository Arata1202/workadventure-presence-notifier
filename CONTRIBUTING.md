# Contributing

Contributions are welcome. Please keep changes focused, documented, and safe for self-hosted WorkAdventure installations.

## Before starting

- Search existing issues and pull requests before opening a new one.
- Open an issue before large changes, new providers, or compatibility changes.
- Report vulnerabilities privately according to [SECURITY.md](SECURITY.md).
- Use English for repository documentation, issues, pull requests, commit messages, configuration examples, and user-facing text.

## Development setup

Requirements:

- Go 1.25 or later
- Docker

Clone the repository, then run:

~~~sh
go mod download
go test ./...
~~~

The Slack webhook URL must remain outside configuration files:

~~~sh
export SLACK_WEBHOOK_URL="https://hooks.slack.com/services/..."
~~~

Never commit webhook URLs, tokens, production configuration, room URLs, IP addresses, or real user presence data.

## Making changes

- Run `gofmt` on Go files.
- Add or update tests for behavior changes.
- Keep provider and notifier implementations behind their existing interfaces.
- Preserve UUID-based user deduplication and avoid logging personal presence data.
- Keep WorkAdventure compatibility protocol definitions minimal. Do not add fields that are not required by the notifier, especially IP address fields.
- Regenerate protobuf code with `make generate` after editing `proto/workadventure.proto`.
- Update the README and example configuration when user-facing behavior changes.

## Validation

Run the same checks used by CI:

~~~sh
test -z "$(gofmt -l .)"
go vet ./...
go test -race ./...
docker build -t workadventure-presence-notifier:test .
~~~

For WorkAdventure protocol or provider changes, test against every version whose compatibility is claimed in the README.

## Commits and pull requests

Use Conventional Commits, for example:

- `feat: add a Discord notifier`
- `fix: reconnect room stream after cancellation`
- `docs: clarify Docker network setup`
- `test: cover duplicate WorkAdventure sessions`

Pull requests should:

- Explain the problem and the chosen solution
- Stay limited to one logical change
- Include tests and documentation where applicable
- Pass all CI checks
- Call out compatibility, privacy, or security implications
- Avoid unrelated formatting or refactoring

By submitting a contribution, you agree that it may be distributed under the project's MIT License.
