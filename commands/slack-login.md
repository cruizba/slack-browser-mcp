---
description: Sign in to a Slack workspace through a browser window
argument-hint: "[workspace]"
---
Use `login_workspace` from the `slack-browser` MCP server. If that server is not
connected, run `slack-browser login` in a terminal instead.

The user asked for (may be empty): $ARGUMENTS

1. Call `login_workspace`, passing the workspace as `workspace` when one was
   named. It opens Chrome and returns once the browser is running, or once the
   sign-in finished if the user was quick.
2. Say that the window is open and that SSO and 2FA are theirs to complete in it:
   nothing is stored until Slack accepts the session.
3. If the tool reports a new workspace, confirm its name, the user it signed in
   as, and whether it became the default. The first workspace stored always
   becomes the default.
4. If it is still waiting, wait for the user to say they are done and then call
   `auth_status`. Report the workspace it now lists; if it still does not appear,
   repeat the failure the tool returned verbatim instead of retrying on your own.
