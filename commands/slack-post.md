---
description: Post a Slack message, with an explicit confirmation step
argument-hint: "<channel> <text> [--thread <ts>]"
---
Post through `post_message` from the `slack-browser` MCP server, or
`slack-browser send` if that server is not connected.

What the user asked for: $ARGUMENTS

The first token is the channel, the rest is the message; `--thread <ts>` is a
thread reply.

When a message is posted, `post_message` wraps the text in a block: a divider, a
bold `*Message Sent by AI <nickname>*` line, the text and a closing divider. With
no nickname configured the line names the client and the Slack account instead,
`Agent '<client>' of <account>`. The dry run shows the plain fallback of that
block, with dashes where the dividers will render. Treat it as expected output:
never strip it, and only suppress it if the user asks, by passing `assist: false`.

1. Call `post_message` **without** `confirm` first. It posts nothing and returns
   the exact message, resolved channel and workspace.
2. Show that back: workspace, channel, thread (or "new message"), and the text
   verbatim.
3. Only after the user answers that it should go out, call it again with
   `confirm: true` and report the returned `ts` and permalink if present.

Never add `confirm: true` on your own, never reword the message, and do not
retry a failed send without showing the error first. If the message contains
`@channel`, `@here` or a DM to someone who was not named in the request, ask
before sending.
