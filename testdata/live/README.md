# Recorded Slack responses

One file per Web API method, shaped `{"status": <http status>, "body": <the JSON
body verbatim>}`. `internal/fakeapi` serves them; `cmd/fakeslack` puts that on a
port for the differential harness.

They are captures from a real workspace (CodeURJC, 2026-11-02), not invented
shapes. That is the point: a fake written from memory tests the memory. The
formatting traps live in the data — `search.messages` returns `total` as an
integer and `score` as a float, `usergroups.list` answers `ok: true` with an
empty array, `files.info` carries a `content` preview nobody asked for.

## What was changed when recording

- Every Slack identifier (`T`, `U`, `C`, `D`, `F`) was replaced with a stable
  stand-in (`T000000001`, `U000000002`, …), including the ones embedded in
  permalinks and in object keys such as `file.shares.private.<channel>`.
- Personal fields (`real_name`, `display_name`, `title`, `email`) became
  `user`.
- Long lists were cut to 30 entries and `response_metadata.next_cursor` blanked,
  so the files stay reviewable. Shapes are untouched.
- Any absolute URL pointing at `slack.com` or `slack-files.com` became
  `{{BASE}}`, which `fakeapi` rewrites to its own address. Without this a test
  would reach the real file host by accident.

Message text is kept as recorded: it is the fixture for rendering and for the
forum envelopes, and rewriting it would test the rewriter.

## Adding a method

Capture the response, apply the four rules above, and drop the file in. Only
`POST /api/<method>` is served, and only for the methods the client actually
calls; an unrecorded method answers `ok: false` with `unknown_method` and logs a
warning, so a missing fixture shows up instead of hiding.
