---
name: sessmcp
description: Operate persistent SSH/Telnet sessions through sessmcp, including device consoles, incremental reads, input receipts and retry deduplication.
---

# sessmcp MCP operating skill

Configure an MCP stdio server with executable `sessmcp`, args `["serve"]`.
Audit: `["serve", "--log", "/private/path/audit.jsonl"]`. Authentication fields are redacted;
commands, line and data may still contain secrets.

## Choose the mode before sending commands

- Ordinary SSH server: `sess_connect` with interactive=false (default), then `sess_exec`.
  Supply protocol, address (host:port), SSH user, password/key_file and known_hosts/host_key.
  Use insecure=true only with explicit user intent to skip host-key verification.
  Each exec uses a new channel over the persistent transport; cwd/environment do not persist.
- Switch/router CLI, including Huawei-style consoles: connect with interactive=true, then
  `sess_interact`. Do not probe by executing the same command first in exec and then in a shell.
- Telnet is always interactive. Login by matching Username/Password prompts and sending one field
  at a time; never guess or automatically replay the device's authentication flow.

## Send one logical command, then read its result

Prefer `line` without newline characters for a single console command:

```json
{"session":"HANDLE","line":"display version","input_id":"inspect-version-1","waitfor":"(?m)(<[^>\r\n]+>|\\[[^\r\n]*\\])\\s*$","wait":"30s"}
```

`line` appends the selected Enter exactly once: SSH defaults to CR, Telnet defaults to CRLF.
Set enter="cr"/"lf"/"crlf" only when the device requires it. Some consoles treat CR and LF as
separate Enters; CRLF can then trigger an empty-line replay. SSH devices should start with CR.
Do not add CR/LF to line; it rejects embedded line breaks. `line` and raw `data` are exclusive.
Use raw data for controls, tab completion, pagination (a single space), or deliberate byte sequences;
raw data is never normalized or given an extra newline. JSON escapes must represent real control
characters, e.g. data="diagnose\r"; do not send literal backslash-r.

Give every logical command a unique input_id and keep it if retrying that same input. Within a
session's last 128 distinct IDs, the server rejects changed input under an old ID and suppresses
retransmission of the same input, even after an uncertain send. input_id is optional and distinct
from JSON-RPC request id. Deduplication does not survive eviction, reconnect or process restart.
Without input_id, two identical calls remain two intentional sends; do not repeat on empty output.

Read the receipt before deciding what to do next:

| input_state | Meaning | Action |
|---|---|---|
| sent | Transport accepted input; device completion is not implied | Read output, then sess_read if needed |
| not_sent | Busy/backpressure rejected this input before sending | Read prior operation; do not queue more input |
| unknown | Send failed/timed out and may have partially executed | Inspect remote state before another input |
| duplicate | Existing input_id was recognized; no new input was sent | Inspect output; poll sess_read |
| none | This call sent no input (read/list/connect) | input_seq refers to the latest session input |

input_seq identifies the send attempt; input_bytes is its intended byte count, not a confirmed
remote-execution count. When next_action="sess_read", call sess_read without command/line/data.
**An empty output or reason=wait/timeout is never evidence that a command was not executed.**
For SSH exec, running=true means the original command is still active: poll, never resubmit.
If reason=exit_status_missing, the SSH channel closed without exit status. Preserve and inspect
its output; success is unknown. The transport is retained unless actually disconnected. For future
network-device commands use an interactive connection, without automatically replaying this command.

## Waits, prompts and history

wait is fixed time from invocation (default 5s), timeout is output silence, waitfor is Go RE2 regex.
These conditions and SSH completion are OR. Set wait explicitly when 5s is too short; duration
examples: 100ms, 30s, 1min. Calls are capped at 115s; endpoint output silence for 110s disconnects.
Sending input does not refresh the output clock; timeout is bounded below by the current call start.
A waitfor match is only as reliable as the pattern. Huawei-style prompts may contain ~ or * and
change between <host>, [~host], [*host] and nested views. Match an observed full prompt, not a guessed
mode name or arbitrary command echo. Device command names vary; use device help rather than guessing.
Polling matches unread output, including bytes arriving between calls; input calls match new output.

Results are incremental and share one per-session cursor. For retained history use all=true,
tail=N, or offset=end to page. History is at most 1MiB; response output at most 64KiB. Inspect more,
truncated and retained_from. Explicit all/tail/offset do not move the default cursor.

Respect backpressure: at most one small command per prompt; input max 4096 bytes. New ordinary
input is rejected if the preceding write produced no output; Ctrl-C/Ctrl-D/Escape remain available.
An echo is not proof the device has completed the command. Never queue bulk serial input.

Use sess_list to inspect connections, sess_close to release them or abort a blocked connection.
Cancellation stops waiting, not the remote command. On connected=false, reconnect and inspect state
before retrying side effects. Maximum 32 sessions and 32 tool workers. Across-session waits are
concurrent; same-session contention returns busy. A non-reading stdout consumer causes the server
to exit after 3s instead of deadlocking. Restarting requires new remote connections.
