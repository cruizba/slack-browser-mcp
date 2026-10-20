---
description: List Slack workspaces and choose the default one
argument-hint: "[use <workspace> | clear]"
---
Use the `slack-browser` MCP server: `list_workspaces` and `set_default_workspace`.

The user asked for (may be empty): $ARGUMENTS

- **Nothing, or `list`** — call `list_workspaces` and show it as returned: the
  `*` marks the default, the line below says what made it the default, and the
  last line says whether a sign-in is running.
- **`use <workspace>`** — call `set_default_workspace` with that name, domain or
  team id. If it is ambiguous the tool lists the candidates: show them and ask,
  do not pick one. Confirm the new default it reports.
- **`clear`** — call `set_default_workspace` with `clear: true` and pass on the
  consequence it reports; with several workspaces every call needs one named.
- **Adding a workspace is not this command.** Point at `/slack-login`; this one
  never opens a browser.
