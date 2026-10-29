---
description: Move slack-browser to the latest release and rebuild it
argument-hint: "[--check | --to <tag> | --pre]"
---
Update the `slack-browser` checkout this session runs from. It is a CLI action,
not an MCP call: run it in the terminal.

What the user asked for (may be empty): $ARGUMENTS

1. Run `slack-browser update --check` and report the installed tag (or branch
   and commit) and the latest release exactly as printed.
2. If it says it is up to date, stop there.
3. `--check` in the arguments means stop after step 1. Otherwise run
   `slack-browser update`, adding `--to <tag>` or `--pre` when the user named
   them. If the checkout has local changes the command refuses: pass the reason
   on and do not commit, stash or rebase anything yourself.
4. Report the release it moved to and the version `slack-browser version` now
   prints.
5. The running MCP server keeps the old build: say that the agent has to
   restart, or reconnect the `slack` MCP server, before the new tools exist. The
   archive sync and forum timers run the new build on their next tick.

If you cannot run commands in this session, print the exact `slack-browser
update` line for the user to run in a terminal instead of guessing.
