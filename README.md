# workadventure-presence-notifier

[![CI](https://github.com/Arata1202/workadventure-presence-notifier/actions/workflows/ci.yml/badge.svg)](https://github.com/Arata1202/workadventure-presence-notifier/actions/workflows/ci.yml)
[![GitHub release](https://img.shields.io/github/v/release/Arata1202/workadventure-presence-notifier)](https://github.com/Arata1202/workadventure-presence-notifier/releases/latest)
[![License](https://img.shields.io/github/license/Arata1202/workadventure-presence-notifier)](LICENSE)

A small service that periodically sends the current WorkAdventure user list to external notification services.

The first notifier is Slack Incoming Webhooks. The internal notifier boundary is intentionally small so additional destinations can be contributed without changing the WorkAdventure presence provider.

## Architecture

![WorkAdventure presence notification architecture](docs/architecture/workadventure-presence-notifier.png)

The notifier reads presence data through WorkAdventure's internal gRPC API and sends scheduled summaries to Slack over HTTPS. Port `50051` remains inside the trusted Docker network. The editable source is available in [Excalidraw format](docs/architecture/workadventure-presence-notifier.excalidraw).

## Status

The latest release is [`v0.1.0`](https://github.com/Arata1202/workadventure-presence-notifier/releases/tag/v0.1.0). Multi-platform container images are available from `ghcr.io/arata1202/workadventure-presence-notifier` for Linux AMD64 and ARM64.

## Quick start

The notifier must be able to reach the WorkAdventure `back` service on its internal Docker network.

```sh
cp config.example.yaml config.yaml
export SLACK_WEBHOOK_URL="https://hooks.slack.com/services/..."
export WORKADVENTURE_NETWORK="workadventure_default"

docker compose -f compose.example.yaml run --rm --build \
  workadventure-presence-notifier \
  --config /etc/workadventure-presence-notifier/config.yaml \
  --once
```

The command sends one real Slack notification and exits. If it succeeds, start daemon mode:

```sh
docker compose -f compose.example.yaml up -d --build
```

Use `docker network ls` if the WorkAdventure network name is unknown. Never commit the Slack webhook URL.

## Features

- Discovers active WorkAdventure rooms through the internal gRPC API
- Maintains the current user list from room presence streams
- Deduplicates multiple connections by WorkAdventure UUID
- Sends scheduled Slack notifications
- Supports a one-shot mode for validation and cron-style execution
- Uses YAML for behavior and message templates
- Keeps the Slack webhook URL in an environment variable
- Does not retain or expose IP addresses

Example Slack message:

```text
Currently online (3): Alice, Bob, Charlie
```

## Compatibility

The minimum supported version is WorkAdventure `v1.33.0`. End-to-end container tests have been completed against WorkAdventure `v1.33.0` and `v1.34.2`. Later releases may work when the required internal RPCs remain compatible, but untested versions are not guaranteed.

The provider uses the `getRooms` and `adminRoom` RPCs shared by WorkAdventure `v1.33.x` and `v1.34.x`. The compatibility protocol intentionally defines only the fields required for presence notifications. The IP address field sent by WorkAdventure is not part of the generated Go type and is discarded while decoding.

The initial implementation supports one WorkAdventure `back` service. Multiple back instances are outside the initial scope.

## Configuration

Copy the example configuration:

```sh
cp config.example.yaml config.yaml
```

Configuration files use Go duration syntax such as `30s`, `5m`, and `1h`. Unknown YAML keys are rejected to catch configuration mistakes. The example file documents every currently supported setting.

`workadventure.address` must point to the internal gRPC endpoint of the WorkAdventure `back` service. The default is `back:50051`.

Set the Slack Incoming Webhook URL through the environment:

```sh
export SLACK_WEBHOOK_URL="https://hooks.slack.com/services/..."
```

Example configuration:

```yaml
workadventure:
  address: "back:50051"
  room_discovery_interval: "30s"
  reconnect_interval: "5s"
  initial_sync_timeout: "2s"
  request_timeout: "10s"

notification:
  interval: "30m"

notifiers:
  slack:
    message: "Currently online ({{ .Count }}): {{ .Users }}"
    empty_message: "No users are currently online."
    user_separator: ", "
```

### Template values

- `.Count`: number of unique users
- `.Users`: display names joined with `user_separator`
- `.UserNames`: sorted list of display names

User names are escaped before they are inserted into Slack text.

## Docker

Build the image:

```sh
docker build -t workadventure-presence-notifier:dev .
```

Run the container on the same Docker network as the WorkAdventure `back` service. The gRPC connection is unencrypted and is intended only for a trusted internal network; do not expose port `50051` publicly:

```sh
docker run --rm \
  --network workadventure_default \
  -e SLACK_WEBHOOK_URL \
  -v "$PWD/config.yaml:/etc/workadventure-presence-notifier/config.yaml:ro" \
  workadventure-presence-notifier:dev \
  --config /etc/workadventure-presence-notifier/config.yaml
```

The repository also includes `compose.example.yaml`. The container runs as the non-root UID/GID `65532:65532`, so the bind-mounted configuration file must be readable by that user. Set `WORKADVENTURE_NETWORK` when the WorkAdventure Docker network uses a different name:

```sh
WORKADVENTURE_NETWORK=workadventure_default \
  docker compose -f compose.example.yaml up -d --build
```

Release images are published to `ghcr.io/arata1202/workadventure-presence-notifier` for Linux AMD64 and ARM64.

After a release, pull a specific version instead of relying on `latest` for production deployments:

```sh
docker pull ghcr.io/arata1202/workadventure-presence-notifier:0.1.0
```

## One-shot mode

One-shot mode waits for the initial WorkAdventure presence snapshot, sends one real notification, and exits with a non-zero status if configuration, presence collection, or delivery fails:

```sh
docker run --rm \
  --network workadventure_default \
  -e SLACK_WEBHOOK_URL \
  -v "$PWD/config.yaml:/etc/workadventure-presence-notifier/config.yaml:ro" \
  workadventure-presence-notifier:dev \
  --config /etc/workadventure-presence-notifier/config.yaml \
  --once
```

Normal daemon mode does not send a startup notification. The first notification is sent after the configured interval.

## Failure behavior

- WorkAdventure discovery and room streams reconnect automatically.
- A failed presence snapshot is logged and skipped until the next interval.
- A failed Slack request is logged and retried at the next interval.
- User display names, room URLs, webhook URLs, and IP addresses are not written to normal logs.

## Development

Requirements:

- Go 1.27.2 or later
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

Regenerate the minimal gRPC compatibility types:

```sh
make generate
```

Build the binary:

```sh
make build
```

## v0.1.0 scope

`v0.1.0` does not include join or leave notifications, Slack Bot authentication, Block Kit messages, persistence, history, multiple WorkAdventure back instances, a management UI, health endpoints, or metrics.

## Contributing and security

Contributions are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for development and pull request guidelines. Maintainers should follow [RELEASING.md](RELEASING.md) when publishing a version.

Do not report vulnerabilities in public issues. Follow [SECURITY.md](SECURITY.md) for private reporting guidance.

## License

This project is licensed under the MIT License. See [LICENSE](LICENSE).
