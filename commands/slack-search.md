---
description: Full-text search across the archived Slack conversations
argument-hint: "<query> [channel] [--workspace <ref>]"
---
Search the local Slack archive with `search_messages` from the `slack-browser`
MCP server. If that server is not connected, run
`slack-browser archive search <query> [--channel <name>] [--workspace <ref>]`.

What the user asked for: $ARGUMENTS

The first token is the query, the rest are filters. Call `search_messages` with
the query and any `channel` or `workspace` filter, `limit` 20 unless the user
asked for another number (cap 100).

Report:
1. The archive age line the tool returns, so a stale result is visible.
2. One bullet per hit: workspace (only when unfiltered), `#channel`, date,
   author, and the matching line in context.
3. A one-line answer to the question behind the query when there is one.

The index only covers what `sync` has fetched. If the hits look thin, say so and
suggest `/slack-sync`, or offer `include_deleted` when the user expects a message
that is no longer there.
