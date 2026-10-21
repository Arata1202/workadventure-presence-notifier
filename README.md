# workadventure-presence-notifier

[![CI](https://github.com/Arata1202/workadventure-presence-notifier/actions/workflows/ci.yml/badge.svg)](https://github.com/Arata1202/workadventure-presence-notifier/actions/workflows/ci.yml)
[![GitHub release](https://img.shields.io/github/v/release/Arata1202/workadventure-presence-notifier)](https://github.com/Arata1202/workadventure-presence-notifier/releases/latest)
[![License](https://img.shields.io/github/license/Arata1202/workadventure-presence-notifier)](LICENSE)

Maintains one current WorkAdventure presence message in a configured destination. Version 0.2 includes a Slack destination.

Version 0.2 uses a Slack bot, replaces the message after debounced presence changes, and removes it when nobody is present.

## Behavior

- Subscribes to WorkAdventure room presence streams and reacts to joins and leaves
- Uses best-effort initial synchronization and suppresses reconnect replay bursts
- Deduplicates multiple connections and rooms by WorkAdventure UUID
- Coalesces changes for five seconds by default
- Posts the new Slack status before deleting the old one
- Deletes the status without a replacement when no users are present
- Persists message state atomically so failed cleanup is retried after restart
- Refreshes the known status every 30 minutes and recreates it if it was deleted externally
- Escapes display names while preserving trusted mrkdwn in the configured template
- Disables Slack link and media unfurls
- Does not persist or log display names, room URLs, IP addresses, tokens, or Slack response bodies

The process intentionally supports one configured destination and one service instance per state file.

## Architecture

![WorkAdventure presence notification architecture](docs/architecture/workadventure-presence-notifier.png)

The notifier reads `getRooms` and `adminRoom` from WorkAdventure over internal, unencrypted gRPC. Keep `back:50051` on a trusted private network. After a synchronized presence change, the platform-neutral coordinator sends the snapshot to a platform-neutral status reconciler. The configured destination adapter owns rendering, message references, recovery lookup, and API errors. The included Slack adapter implements those operations through `chat.postMessage`, `chat.update`, `chat.delete`, and `conversations.history`. Adding another built-in adapter does not change the presence provider, coordinator, or reconciler. The editable diagram source is available in [Excalidraw format](docs/architecture/workadventure-presence-notifier.excalidraw).

For a non-empty update, the service atomically records a random operation ID before any Slack call, marks the operation attempted, and attaches only that operation ID as Slack message metadata. If the post result is ambiguous, the next reconciliation searches channel history from five minutes before the local attempt time to tolerate clock skew. A no-match result cannot trigger a replacement during the 30-second recovery grace period. The newest matching message is unconditionally updated to the current rendered text and becomes current; duplicate matches and the previous current message become stale, and stale messages are deleted. A crash or failed deletion leaves enough non-sensitive state for recovery or cleanup to be retried. On a SIGINT or SIGTERM shutdown, deletion starts only after the coordinator has stopped and is attempted within `status.shutdown_timeout`; state is cleared only after successful deletion or Slack `message_not_found`. When rendered presence is unchanged, periodic reconciliation updates the known current message in place; if it was deleted externally, the missing reference is cleared before the normal replacement flow posts a new status. A missed presence change still uses the usual post-new-then-delete-old flow.

## Upgrading from v0.1

Version 0.2 is not an in-place configuration upgrade. Replace the incoming webhook with a Slack bot that can post message metadata, remove the old `notification` and `empty_message` keys, set `notifiers.slack.channel_id`, provide `SLACK_BOT_TOKEN`, and add the persistent state volume. Messages previously posted by the v0.1 webhook cannot be identified by v0.2 and must be removed manually.

## Slack app setup

Create or select a Slack app and add these bot token scopes:

- `chat:write`
- `channels:history` when the target is a public channel
- `groups:history` when the target is a private channel

The notifier uses the metadata event type `workadventure_presence_status` with one string payload property named `operation_id`. Slack allows unregistered custom metadata, but registering the schema under the app manifest's top-level `events` section is recommended for validation and discoverability. The notifier treats metadata-related response warnings as errors. See Slack's [message metadata guide](https://docs.slack.dev/messaging/message-metadata/) and [app manifest reference](https://docs.slack.dev/reference/app-manifest/).

Install the app, invite the bot to the target channel, and obtain the channel ID from Slack channel details. The app must remain a member of the channel so metadata recovery through `conversations.history` can work. A missing history scope, rejected metadata, or lost membership intentionally blocks replacement posts while preserving the known current status and pending recovery state.

Provide the bot token only through the environment:

~~~sh
export SLACK_BOT_TOKEN="xoxb-..."
~~~

Never put a token in YAML or commit it.

## Quick start

~~~sh
cp config.example.yaml config.yaml
# Set notifiers.slack.channel_id in config.yaml.
export SLACK_BOT_TOKEN="xoxb-..."
export WORKADVENTURE_NETWORK="workadventure_default"

docker compose -f compose.example.yaml run --rm --build \
  workadventure-presence-notifier \
  --config /etc/workadventure-presence-notifier/config.yaml \
  --once
~~~

The --once mode gives WorkAdventure readiness and Slack reconciliation separate deadlines, allows at least five minutes for a complete multi-call Slack recovery, performs one reconciliation, and exits. It does not delete the resulting status on exit. Start daemon mode after validation:

~~~sh
docker compose -f compose.example.yaml up -d --build
~~~

For a released version, pin the published image instead of rebuilding a checkout:

~~~sh
docker pull ghcr.io/arata1202/workadventure-presence-notifier:0.2.0
~~~

Replace the Compose service's `build` section with `image: ghcr.io/arata1202/workadventure-presence-notifier:0.2.0`, or use that image in the Docker command below. Avoid floating tags when reproducible deployment matters.

The Compose example mounts a named volume at /var/lib/workadventure-presence-notifier. Keep it across routine VM and container restarts. Losing it can leave an old Slack status that the service cannot identify. Its 25-second `stop_grace_period` exceeds the example's worst-case 10-second coordinator stop wait plus its 10-second Slack cleanup window, leaving Docker time to deliver SIGTERM before SIGKILL. Increase the grace period if you increase `status.shutdown_timeout`.

## Configuration

Durations use Go syntax such as 5s, 30m, and 1h. Unknown YAML keys are rejected.

~~~yaml
workadventure:
  address: "back:50051"
  room_discovery_interval: "30s"
  reconnect_interval: "5s"
  initial_sync_timeout: "2s"
  request_timeout: "10s"

status:
  debounce: "5s"
  reconciliation_interval: "30m"
  shutdown_timeout: "10s"
  state_file: "/var/lib/workadventure-presence-notifier/state.json"

notifiers:
  slack:
    request_timeout: "10s"
    channel_id: "C0123456789"
    message: "Currently online ({{ .Count }}): {{ .Users }}"
    user_separator: ", "
~~~

`notifiers.slack.request_timeout` applies to each HTTP attempt. A complete Slack operation may take longer because non-post methods use up to three attempts and bounded retry waits; `--once` budgets for all attempts and waits separately from WorkAdventure readiness.

### Template values

- .Count: number of unique users
- .Users: escaped display names joined with user_separator
- .UserNames: sorted list of escaped display names

The administrator-authored template is trusted and may contain static Slack links and mentions. Only WorkAdventure-supplied names are escaped. Do not let untrusted users edit the template.

### Persistent state

The state parent directory is created with private permissions when possible. State is written as a mode-0600 temporary file, synced, closed, atomically renamed, and followed by a directory sync.

The version 1 file uses a destination-neutral schema containing a destination ID, opaque current and stale message references, a pending operation UUID, and a non-sensitive RFC 3339 attempt time when recovery is needed. Rendered-content hashes are process-local and are not persisted. The file contains no display names, rendered text, room data, or tokens. The design assumes a single running instance. Do not share one state file between replicas or destinations.

Unknown fields, additional YAML documents, malformed identifiers, impossible invariants, and unsupported versions stop startup instead of discarding cleanup information.

Changing `notifiers.slack.channel_id` while tracked state exists also stops startup. To move channels safely, stop the service gracefully while the old bot and channel are still accessible, confirm that the old status was deleted, then change the channel ID and restart. If the old destination is no longer accessible, remove the orphaned Slack message manually before resetting the state file.

## Docker

The container runs as UID/GID 65532:65532 and prepares the default state directory for that user. For a host bind mount, create a directory writable by that UID/GID.

~~~sh
docker run --rm \
  --network workadventure_default \
  -e SLACK_BOT_TOKEN \
  -v "$PWD/config.yaml:/etc/workadventure-presence-notifier/config.yaml:ro" \
  -v workadventure-presence-notifier-state:/var/lib/workadventure-presence-notifier \
  workadventure-presence-notifier:dev \
  --config /etc/workadventure-presence-notifier/config.yaml
~~~

## Compatibility

The tested baseline is WorkAdventure v1.33.0, with end-to-end coverage also completed against v1.34.2. Later releases may work while the required internal RPCs remain compatible. The minimal compatibility protocol excludes IP address fields. One WorkAdventure back service is supported.

## Failure behavior

- WorkAdventure discovery and room subscriptions reconnect automatically. While a synchronized snapshot is unavailable, the last Slack status remains visible; an acquisition failure is never interpreted as zero users.
- Initial synchronization is best-effort: expected user counts can complete replay early, while `initial_sync_timeout` is the fallback when the protocol cannot prove replay completeness. Rooms discovered with zero expected users wait for that timeout so racing joins are included.
- Ambiguous `chat.postMessage` outcomes (network failure, HTTP 5xx, `fatal_error`/`internal_error`, decode failure, or a missing timestamp) are not automatically retried. HTTP 429 may be retried because Slack explicitly rejected that attempt; `Retry-After` is honored up to 30 seconds.
- Other Slack methods use bounded retries. Slack responses are limited to 1 MiB, and history is requested in pages of 50 messages. A failed or ambiguous post leaves the previous status current and retains only the operation ID and attempt timestamp. Recovery must successfully search message metadata in channel history and wait through the 30-second visibility grace period before posting again.
- If history lookup fails because of scope, membership, network, or API errors, the service keeps the valid previous status and pending metadata without guessing or reposting.
- `chat.update` `message_not_found` triggers another history lookup and, only when no matching message remains, a fresh post attempt.
- A failed old-message deletion remains recorded and is retried. While stale cleanup is blocked, later presence changes update the current message in place instead of creating unbounded replacement posts; cleanup continues for other stale references.
- message_not_found is successful idempotent deletion.
- Reconciliation failures retry after 30 seconds with capped exponential backoff up to `status.reconciliation_interval`; a successful reconciliation or new presence change resets the backoff.
- An intentional shutdown exits with an error if status deletion cannot complete; persisted state remains available for a later cleanup retry.
- A malformed or incompatible state file stops startup instead of losing cleanup information.

## Development

Requirements are Go 1.27.3 or later and Docker.

~~~sh
test -z "$(gofmt -l .)"
go vet ./...
go test -race ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
docker build --build-arg VERSION=ci -t workadventure-presence-notifier:ci .
test "$(docker run --rm workadventure-presence-notifier:ci --version)" = "ci"
~~~

Run make generate after changing proto/workadventure.proto. See [CONTRIBUTING.md](CONTRIBUTING.md), [RELEASING.md](RELEASING.md), and [SECURITY.md](SECURITY.md).

## License

This project is licensed under the MIT License. See [LICENSE](LICENSE).
