# Migrating from v0.1 to v0.2

Version 0.2 replaces periodic Slack Incoming Webhook summaries with one event-driven Slack Bot status.

## Slack app

1. Create or select a Slack app and add the `chat:write` bot token scope.
2. Install the app to the workspace.
3. Invite the bot to the configured channel.
4. Set `SLACK_BOT_TOKEN` to the bot token. Remove `SLACK_WEBHOOK_URL`.
5. Add `destination.slack.channel_id` to `config.yaml`.

No history-reading scope is required. The service only posts and deletes its own messages.

## Configuration and storage

- Replace `notification.interval` with `status.debounce`, `status.shutdown_timeout`, and `status.state_file`.
- Remove the old `notifiers.slack.empty_message`. An empty office is represented by no status message.
- Mount persistent writable storage for `status.state_file`. The example Compose file uses the `notifier-state` volume.

The state file stores the destination ID, current message timestamp, and up to three known stale timestamps needed for replacement and cleanup. It is written atomically with private file permissions. Failed stale-message deletions remain recorded for retry, and new replacements stop while the stale list remains full. Changing the configured destination while references remain is rejected so the old status cannot be silently orphaned.

Slack status messages are sent as plain text with mrkdwn disabled. WorkAdventure display names still escape `&`, `<`, and `>` before insertion.

## Operational behavior

- Startup publishes a synchronized non-empty snapshot.
- Presence changes are debounced for five seconds by default.
- A changed non-empty snapshot is posted before the previous status is deleted.
- Empty presence and an intentional daemon shutdown retry deletion of all known current and stale statuses.

If a network connection fails after Slack may have accepted a post, the result can be ambiguous. The service logs the failure and keeps its last known reference; it does not scan channel history or claim exactly-once delivery.
