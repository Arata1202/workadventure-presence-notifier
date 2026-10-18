# workadventure-presence-notifier

A small service that periodically sends the current WorkAdventure user list to external notification services.

The first notifier is Slack Incoming Webhooks. The internal notifier boundary is intentionally small so additional destinations can be contributed without changing the WorkAdventure presence provider.

## Status

This project is under active development and has not published its first release yet.

## Features

- Discovers active WorkAdventure rooms through the internal gRPC API
- Maintains the current user list from room presence streams
- Deduplicates multiple connections by WorkAdventure UUID
- Sends scheduled Slack notifications
- Supports a one-shot mode for validation and cron-style execution
- Uses YAML for behavior and message templates
- Keeps the Slack webhook URL in an environment variable
- Does not retain or expose IP addresses

## Compatibility

The initial supported range is WorkAdventure `v1.33.0` and later. End-to-end container tests have been completed against WorkAdventure `v1.33.0` and `v1.34.2`.

The provider uses the `getRooms` and `adminRoom` RPCs shared by WorkAdventure `v1.33.x` and `v1.34.x`. The compatibility protocol intentionally defines only the fields required for presence notifications. The IP address field sent by WorkAdventure is not part of the generated Go type and is discarded while decoding.

The initial implementation supports one WorkAdventure `back` service. Multiple back instances are outside the initial scope.

## Configuration

Copy the example configuration:

```sh
cp config.example.yaml config.yaml
```

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

Run the container on the same Docker network as the WorkAdventure `back` service:

```sh
docker run --rm \
  --network workadventure_default \
  -e SLACK_WEBHOOK_URL \
  -v "$PWD/config.yaml:/etc/workadventure-presence-notifier/config.yaml:ro" \
  workadventure-presence-notifier:dev \
  --config /etc/workadventure-presence-notifier/config.yaml
```

The repository also includes `compose.example.yaml`. Set `WORKADVENTURE_NETWORK` when the WorkAdventure Docker network uses a different name:

```sh
WORKADVENTURE_NETWORK=workadventure_default \
  docker compose -f compose.example.yaml up -d --build
```

Tagged container images will be published after the first release.

## One-shot mode

One-shot mode waits for the initial WorkAdventure presence snapshot, sends one notification, and exits:

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

- Go 1.25 or later
- Docker, for container builds and protocol generation

Run tests:

```sh
go test ./...
```

Regenerate the minimal gRPC compatibility types:

```sh
make generate
```

Build the binary:

```sh
make build
```

## Initial scope

The first release does not include join or leave notifications, Slack Bot authentication, Block Kit messages, persistence, history, multiple WorkAdventure back instances, a management UI, health endpoints, or metrics.
