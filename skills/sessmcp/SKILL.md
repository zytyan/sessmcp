---
name: sessmcp
description: Operate persistent SSH/Telnet sessions through sessmcp MCP tools, including device consoles, pagers and bounded waits.
---

# sessmcp MCP operating skill

Configure an MCP stdio server: executable `sessmcp`, args `["serve"]`.
Optional audit: `["serve", "--log", "/private/path/audit.jsonl"]`.
Audit redacts authentication fields, but commands/data may contain secrets.

1. `sess_connect` creates a process-local handle. Supply protocol and address (host:port).
   SSH requires user, password or key_file, plus known_hosts or host_key SHA256 fingerprint.
   Use insecure=true only when the user explicitly intends to skip verification.
2. For ordinary SSH servers ALWAYS prefer interactive=false (default) and `sess_exec`.
   SSH exit-status/exit-signal determines completion, with merged stdout/stderr. No sentinel markers.
   Each exec uses a new channel over the same connection: cwd/environment do not persist.
   Example: {"session":"HANDLE","command":"uname -a","wait":"30s"}.
3. For Telnet and SSH switch/router consoles, set interactive=true and use `sess_interact`.
   `data` is exact input: include newline yourself, e.g. "show version\r\n".
   Login to Telnet by matching "Username:"/"Password:" then sending one field at a time.
   Example: {"session":"HANDLE","data":"show version\r\n","waitfor":"(?m)switch[#>]\\s*$","timeout":"800ms","wait":"30s"}.
   For pagers match "--More--", send a single space, and wait for the next page/prompt.
4. Wait conditions are OR: SSH completion, waitfor Go RE2 regex on NEW output, fixed wait
   from invocation, or timeout since last OUTPUT refresh. Input does not refresh timeout.
   Default wait=5s, even with waitfor/timeout. Override wait to wait longer. Positive durations:
   100ms, 30s, 1min. A call is capped at 115s, even if output continues.
   Endpoint stdout/stderr silence for 110s disconnects with error. Do not use an infinite wait.
5. Results are incremental by default. If running=true on SSH exec, use sess_read to poll;
   do NOT execute the command again. Cancellation stops waiting, not the remote command.
   `sess_close` closes a blocked connection without waiting for its operation lock.
6. Inspect connected, error, reason, backpressure, more, truncated, retained_from, end and exit_code.
   On disconnect, create a NEW session and assess remote state before retrying side effects.
   On busy/backpressure, do not queue/replay input. Max 4096 input bytes; send one SMALL command
   per prompt/echo on serial/slow devices. An accepted write does not mean the device consumed it.
7. Default reads advance a shared per-session byte cursor. `all:true` reads retained history;
   `tail:100` gets last 100 retained lines. History is bounded to 1MiB; response cap is 64KiB.
   To retrieve the next page use sess_read with offset equal to the last result's end.
   Explicit all/tail/offset do not advance the default cursor. Truncation is reported, never hidden.
   Polling matches unread output (including bytes arriving between calls); input calls match only output received after invocation.
   Additional input is rejected while the previous write has produced no output; Ctrl-C/Ctrl-D/Escape remain available.
8. `sess_list` inspects connections; `sess_close` releases them. Maximum 32 sessions and 32
   concurrent tool calls. Remote calls are concurrent across sessions; same-session contention
   returns busy immediately. Ping/cancellation/listing remain available during waits.

The stdio transport uses bounded frames/queues and a 3s output-write deadline. If the MCP client
stops reading stdout, the server exits instead of hanging. Reconnect sessions after restart.
