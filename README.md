# workadventure-presence-notifier

[![CI](https://github.com/Arata1202/workadventure-presence-notifier/actions/workflows/ci.yml/badge.svg)](https://github.com/Arata1202/workadventure-presence-notifier/actions/workflows/ci.yml)
[![GitHub release](https://img.shields.io/github/v/release/Arata1202/workadventure-presence-notifier)](https://github.com/Arata1202/workadventure-presence-notifier/releases/latest)
[![License](https://img.shields.io/github/license/Arata1202/workadventure-presence-notifier)](LICENSE)

A small service that maintains one event-driven Slack status for the people currently online in WorkAdventure.

## Architecture

![WorkAdventure presence status architecture](docs/architecture/workadventure-presence-notifier.png)

The service discovers rooms and consumes presence events through WorkAdventure's internal gRPC API. It deduplicates users globally by WorkAdventure UUID, waits five seconds after the last change by default, then posts a new Slack message before deleting the previous one. Port `50051` stays inside the trusted Docker network; Slack calls use HTTPS. The editable diagram source is available in [Excalidraw format](docs/architecture/workadventure-presence-notifier.excalidraw).

Version 0.2 supports one WorkAdventure `back` service, one configured destination, and one notifier process.

## Behavior

- Publishes a synchronized non-empty snapshot on startup
- Debounces presence changes for five seconds by default
- Posts a changed non-empty status before deleting the previous status
- Deletes all known current and stale statuses when nobody is online
- Deletes all known current and stale statuses on intentional daemon shutdown
- Retains the last valid status while WorkAdventure discovery or room reconnection is incomplete
- Persists the destination ID, current message timestamp, and up to three known stale timestamps for restart replacement and cleanup
- Leaves a successful `--once` result in Slack for practical live validation

Example status:

```text
Currently online (3): Alice, Bob, Charlie
```

## Slack app setup

Create a Slack app with the `chat:write` bot token scope, install it to the workspace, and invite the bot to the target channel. No event subscription, App-Level Token, channel-history scope, or Incoming Webhook is required.

Keep the bot token in the environment:

```sh
export SLACK_BOT_TOKEN="xoxb-..."
```

Never place the token in `config.yaml` or commit it.

## Quick start

The notifier must be able to reach the WorkAdventure `back` service on its internal Docker network.

```sh
cp config.example.yaml config.yaml
# Set destination.slack.channel_id in config.yaml.
export SLACK_BOT_TOKEN="xoxb-..."
export WORKADVENTURE_NETWORK="workadventure_default"

docker compose -f compose.example.yaml run --rm --build \
  workadventure-presence-notifier \
  --config /etc/workadventure-presence-notifier/config.yaml \
  --once
```

The command publishes one real status and exits without deleting it. If it succeeds, start daemon mode:

```sh
docker compose -f compose.example.yaml up -d --build
```

The named `notifier-state` volume preserves the destination ID and known message timestamps across restarts. The Compose example gives the process 20 seconds to finish its configured 10-second shutdown cleanup. Use `docker network ls` if the WorkAdventure network name is unknown.

## Configuration

Configuration files use Go duration syntax such as `30s`, `5m`, and `1h`. Unknown YAML keys are rejected.

```yaml
workadventure:
  address: "back:50051"
  room_discovery_interval: "30s"
  reconnect_interval: "5s"
  initial_sync_timeout: "2s"
  request_timeout: "10s"

status:
  debounce: "5s"
  shutdown_timeout: "10s"
  state_file: "/var/lib/workadventure-presence-notifier/state.json"

destination:
  slack:
    channel_id: "C0123456789"
    message: "Currently online ({{ .Count }}): {{ .Users }}"
    user_separator: ", "
    request_timeout: "10s"
```

Template values:

- `.Count`: total number of unique online users
- `.Users`: sorted, bounded, Slack-escaped display names joined with `user_separator`
- `.UserNames`: the bounded sorted display-name list

The service caps individual names, the rendered message, and Slack API response bodies. Requests have a configured timeout and bounded attempts. It sends plain text with Slack mrkdwn disabled, keeps `&`, `<`, and `>` escaped, and disables link and media unfurling.

## Compatibility

The minimum supported version is WorkAdventure `v1.33.0`. End-to-end container tests have been completed against WorkAdventure `v1.33.0` and `v1.34.2`. Later releases may work when the required internal RPCs remain compatible, but untested versions are not guaranteed.

The provider uses the `getRooms` and `adminRoom` RPCs. The compatibility protocol includes only the fields required for presence; WorkAdventure IP address fields are discarded while decoding.

## Failure behavior

- Failed room discovery leaves the last valid global snapshot unchanged.
- A reconnect builds a replacement room snapshot privately and publishes it only after synchronization.
- A failed Slack post leaves the previous status and persisted reference unchanged.
- A missing persisted Slack message is treated as recoverable during delete or replacement.
- Startup stops with a clear error if persisted references belong to a different configured destination.
- Failed deletion of a known stale message is retained and retried on startup, publish, empty-state cleanup, and shutdown. The service stops posting replacements when all three stale slots remain occupied.
- Slack network failures after a post may be ambiguous. The service logs the failure and does not scan channel history or claim exactly-once delivery.
- Normal logs do not include user names, room URLs, tokens, or IP addresses.

## One-shot mode

`--once` waits for a synchronized WorkAdventure snapshot, applies the same replacement or empty cleanup behavior, and exits. It intentionally does not delete a successfully published status on normal exit:

```sh
docker compose -f compose.example.yaml run --rm --build \
  workadventure-presence-notifier \
  --config /etc/workadventure-presence-notifier/config.yaml \
  --once
```

## Migrating from v0.1

Version 0.2 replaces Incoming Webhooks and interval summaries. See [the v0.2 migration guide](docs/migration-v0.2.md) for Slack scopes, environment variables, configuration, persistence, and operational changes.

## Development

Requirements:

- Go 1.27.3 or later
- Docker, for container builds and protocol generation

Run the same validation used by CI:

```sh
test -z "$(gofmt -l .)"
go vet ./...
go test -race ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
docker build --build-arg VERSION=ci -t workadventure-presence-notifier:ci .
test "$(docker run --rm workadventure-presence-notifier:ci --version)" = "ci"
```

Regenerate the minimal gRPC compatibility types with `make generate` after changing `proto/workadventure.proto`.

## Contributing and security

See [CONTRIBUTING.md](CONTRIBUTING.md) for contribution guidelines and [SECURITY.md](SECURITY.md) for private vulnerability reporting. Maintainers should follow [RELEASING.md](RELEASING.md) when publishing a version.

## License

This project is licensed under the MIT License. See [LICENSE](LICENSE).
