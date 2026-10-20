---
description: Refresh the local Slack archive now
argument-hint: "[workspace] [--full]"
---
Bring the local Slack archive up to date.

1. Call `archive_status` first. If it says a sync is already running, report that
   and stop.
2. Run `slack-browser sync --quiet` in the background (add `--workspace <ref>`
   when one was named, `--full` when asked to re-fetch the backfill window).
   What the user asked for (may be empty): $ARGUMENTS
3. When it finishes, call `archive_status` again and report the new age, channels
   synced, thread backlog and any per-workspace or per-channel errors.

Do not read or summarise messages from this command; it only refreshes the
archive. If the sync fails with `invalid_auth` or `token_revoked`, say that
`slack-browser login` is needed and stop.
