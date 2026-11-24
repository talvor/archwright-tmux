# Driving interactive claude/codex sessions from outside tmux

Researched 2026-11-24 for issue #2 (map #1). Sources: tmux 3.8 man page (local), `claude 2.1.285` and `codex-cli 0.171.0` local `--help`, official docs (WebFetch summaries, so re-verify exact wording before depending on it), one local tmux experiment. Doc base: `CC/` = https://code.claude.com/docs/en/ ; `CX/` = https://learn.chatgpt.com/docs/ (developers.openai.com/codex redirects here). ADR context: 0001, 0003, 0011. Anything not confirmed in a primary source is marked UNVERIFIED.

## Answer in brief

- Launch: one tmux session/window per run, `new-session -d -c <cwd> -e K=V ... '<claude|codex> [flags] "<brief>"'`. Both CLIs take the initial prompt as a positional arg and start interactive (CC/cli-reference: `claude "query"`; `codex --help`: `codex [OPTIONS] [PROMPT]`). Long briefs: pass a short prompt pointing at a file the Works wrote, or inject via `load-buffer` + `paste-buffer -p`.
- State detection: do NOT scrape the screen as the primary signal. Use vendor hooks (both support command hooks with `Stop`, `PermissionRequest`, `SessionStart`/`SessionEnd`, JSON on stdin including `session_id` and `transcript_path`) plus tmux `pane_dead` for process exit. Screen scraping (`capture-pane`) only as a fallback / for the dashboard preview.
- Nudges: `paste-buffer -p` (bracketed) then `send-keys Enter`, only when state is idle. Codex additionally has `codex queue --thread <id> --message <text>` (queues a message for an existing session). Claude inbox socket (ADR 0001) is separate, UNVERIFIED here.
- Transcripts: both give a `transcript_path` in hook input; Claude JSONL under `~/.claude/projects/<encoded-cwd>/<session-id>.jsonl`, Codex rollout JSONL under `~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl`. Both observed locally.
- Biggest constraint: managed policy. Hooks and settings are the state channel, and both vendors let admins restrict them (see Risks).

## 1. tmux primitives (tmux 3.8 man page, local)

- `new-session -d [-c start-directory] [-e environment] ... [shell-command]`: detached session with env and cwd (man tmux, new-session).
- `send-keys [-l] [-H] ... -t target-pane key...`: "-l disables key name lookup and processes the keys as literal UTF-8"; without -l, unrecognised strings are sent as characters (man, send-keys).
- `load-buffer`/`set-buffer` + `paste-buffer [-p] [-d] [-t pane]`: "-p ... paste bracket control codes are inserted around the buffer if the application has requested bracketed paste mode"; linefeeds are replaced by CR unless `-r` (man, paste-buffer). Use this for multi-line text so newlines are not read as Enter.
- `pipe-pane -O -t pane 'cat >> file'`: streams all pane output to a command; one pipe per pane (man, pipe-pane). Gives a raw byte log (ANSI included), not a transcript.
- `capture-pane -p [-S -E -J] -t pane`: snapshot of visible/history text (man, capture-pane).
- Liveness: format vars `pane_dead`, `pane_dead_status`, `pane_dead_signal`, `pane_dead_time`, `pane_current_command`, `pane_pid` (man, FORMATS). With `remain-on-exit on` the pane stays and hook `pane-died` fires; without it hook `pane-exited` fires (man, HOOKS; remain-on-exit). `set-hook` can run a shell command on these, so the Works can learn of exit without polling.
- `wait-for` channels exist for tmux-side sync (man, wait-for); not needed if hooks call the Works directly.
- Activity heuristics: `monitor-activity`, `monitor-silence <secs>` with `alert-silence` hook (man). Coarse idle proxy only: a TUI redraws spinners while working, and is silent while waiting on a prompt, but a long silent tool call looks the same.
- Experiment (this machine, tmux 3.8, private socket `-S`): `new-session -d 'sh -c "read x; echo got:$x; exit 3"'`, `set remain-on-exit on`, `load-buffer -` + `paste-buffer -p` + `send-keys Enter`. Result: child read "hello", `list-panes -F '#{pane_dead} #{pane_dead_status}'` returned `dead=1 status=3`. Confirms paste+Enter delivery and exit-status capture. Use a dedicated socket (`-L`/`-S`) so the Works never touches the developer's own tmux server.

## 2. Claude Code (interactive)

- Launch flags (local `claude --help`, CC/cli-reference): positional `[prompt]` starts interactive; `--session-id <uuid>` pins the id (so the Works knows the transcript path up front); `-n/--name`; `--permission-mode <acceptEdits|auto|bypassPermissions|manual|dontAsk|plan>`; `--settings <file-or-json>`; `--setting-sources`; `--mcp-config`/`--strict-mcp-config`; `--append-system-prompt`; `-r/--resume`; `--fork-session`; `--add-dir`; `--allowedTools/--disallowedTools`. `--bare` skips hooks, so never use it for tmux sessions.
- Hooks (CC/hooks): events include `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PermissionRequest`, `PermissionDenied`, `PostToolUse`, `Notification`, `Stop`, `StopFailure`, `SubagentStop`, `SessionEnd`. Common stdin JSON: `session_id`, `transcript_path` ("may lag current turn"), `cwd`, `permission_mode`, `hook_event_name`, `agent_id`/`agent_type` for subagents. Hook can be `command` or HTTP (POST body).
- State mapping:
  - working: `UserPromptSubmit` / `PreToolUse` seen, no `Stop` since.
  - awaiting permission: `PermissionRequest` ("when a tool call needs a permission decision"), or `Notification` with `notification_type: permission_prompt`.
  - idle waiting for input: `Stop` ("when Claude finishes responding"), or `Notification` `idle_prompt`.
  - needs input: `Notification` types `elicitation_dialog`, `agent_needs_input`.
  - errored turn: `StopFailure` (matchers: `rate_limit`, `authentication_failed`, `billing_error`, `max_output_tokens`, ...). Output/exit code ignored.
  - finished: `SessionEnd` (reasons: clear, resume, logout, prompt_input_exit, other). `SessionEnd` hooks share a 1.5 s budget, so the hook should only do a fast local write/POST; also confirm with tmux `pane_dead`.
- Hooks can also steer: `Stop` hook may return `decision: "block"` + reason or `additionalContext`, which "continues the conversation" (CC/hooks). Hooks `additionalContext` injects text next to tool results / at turn end; capped at 10,000 chars. This is the in-band alternative to `send-keys` for delivering Works messages mid-turn (ADR 0001's PostToolUse inbox).
- `PermissionRequest` hook returns `hookSpecificOutput.decision.behavior = allow|deny` (CC/hooks). That is a programmatic approval path, but the Works must not use it to widen permissions (ADR 0011 never-list); the intended model for interactive is a human attaching and approving.
- Transcript: path given in every hook; file observed locally at `~/.claude/projects/<cwd with / and . replaced by ->/<session-id>.jsonl`.
- Remote/other: `--remote-control`, `--bg/--background` (`claude attach|logs|stop|rm`, `claude agents`) exist in 2.1.285 `--help`; not evaluated here (open question: could `--bg` replace tmux for Claude? Spec decision says tmux).

## 3. Codex (interactive)

- Launch (local `codex --help`): `codex [OPTIONS] [PROMPT]`; `-C/--cd`, `-s/--sandbox <read-only|workspace-write|danger-full-access>`, `-a/--ask-for-approval <policy>`, `-c key=value` config override, `-m`, `--no-alt-screen` (inline mode; keeps scrollback, better for `capture-pane`/`pipe-pane`), `--worktree`, `resume`/`fork` subcommands, `--strict-config`. `--dangerously-bypass-hook-trust` exists and is on the ADR 0011 never-list by spirit.
- Hooks (CX/hooks): events `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PermissionRequest` ("runs when Codex is about to ask for approval, such as a shell escalation or managed-network approval"), `PostToolUse`, `Stop`, `SubagentStop`, `Interrupt`, `PreCompact/PostCompact`, `PreModelRequest/PostModelRequest`, `SessionEnd`. Input JSON: `session_id`, `transcript_path` (string or null), `cwd`, `hook_event_name`, `model`, `permission_mode`, plus `turn_id` on turn-scoped events. `Stop` receives the final assistant message and can `decision: "block"` to continue. Hooks load from `~/.codex/hooks.json`, `~/.codex/config.toml`, and, if the project is trusted, `<repo>/.codex/hooks.json` / `config.toml`. "Before a non-managed hook can run, Codex requires you to review and trust the exact hook definition"; managed hooks bypass this. Local `codex features list`: `hooks` stable/true. Hook trust prompt is a blocker for unattended spawn unless the hook is managed or pre-trusted (UNVERIFIED how to pre-trust non-interactively besides the bypass flag).
- Older/simpler: `notify = ["prog", "args"]` in config runs a program with one JSON arg on `agent-turn-complete` only (fields `type`, `thread-id`, `turn-id`, `cwd`, `input-messages`, `last-assistant-message`) (CX/config-advanced). `tui.notifications` can filter on `agent-turn-complete` and `approval-requested`, delivered by `osc9` or `bel` escape sequences in the pane (`tui.notification_method`, `tui.notification_condition`). So approval-requested is visible in-band only via terminal escape/TUI, or via the `PermissionRequest` hook.
- State mapping: working = `UserPromptSubmit`/`PreToolUse` since last `Stop`; awaiting approval = `PermissionRequest`; idle = `Stop` (or `notify` turn-complete); exit = `SessionEnd` + `pane_dead`. Rollout events seen locally: `task_started`, `task_complete`, `token_count`, `item_completed`, `thread_settings_applied` (event_msg types in a real rollout file), usable as a cross-check by tailing the file.
- Nudge: `codex queue --thread <session UUID or exact name> --message <text> [--resume]` queues a message for an existing session (local `codex queue --help`; semantics, delivery timing and TUI interaction UNVERIFIED, needs a test). Otherwise paste+Enter as for Claude. `codex agents`, `--remote ws://|unix://` (app-server) exist; app-server is a richer control channel but is a different architecture from tmux (not evaluated).
- Transcript: rollout JSONL at `~/.codex/sessions/YYYY/MM/DD/rollout-<local-ts>-<uuid>.jsonl` (observed locally; first record `session_meta`). `transcript_path` in hook input may be null, so also locate by `session_id`/thread-id suffix.

## 4. Claude vs Codex differences that matter

| Concern | Claude Code | Codex |
|---|---|---|
| Initial prompt | positional | positional |
| Pin session id at launch | `--session-id <uuid>` | none seen; learn from `SessionStart` hook (UNVERIFIED if settable) |
| Permission-needed signal | `PermissionRequest` hook, `Notification:permission_prompt` | `PermissionRequest` hook; TUI `approval-requested` notification |
| Idle/turn-done signal | `Stop`, `Notification:idle_prompt` | `Stop`, `notify` `agent-turn-complete` |
| Error turn signal | `StopFailure` | not seen in docs fetched |
| Exit signal | `SessionEnd` (1.5 s budget) | `SessionEnd` |
| Mid-turn injection | hook `additionalContext` | `Stop` block + reason; `codex queue` |
| Hook trust | settings precedence, managed can disable all hooks (`disableAllHooks` seen in this machine's remote-settings, docs/research/headless-managed-permissions.md) | per-hook review/trust required unless managed |
| Transcript | `~/.claude/projects/<cwd>/<id>.jsonl` | `~/.codex/sessions/Y/M/D/rollout-*.jsonl` |
| Inline TUI for scrape | default | `--no-alt-screen` |

## 5. Risks and open questions

1. Hooks may be disabled or untrusted by policy. This machine's remote-settings carries a `disableAllHooks` key (value not recorded here; check, see docs/research/headless-managed-permissions.md and managed-policy-mcp-and-hooks-check.md). If hooks cannot run, state detection falls back to screen/transcript parsing, which is fragile. Needs a test with real hooks under this tenant's policy for both vendors.
2. Hook delivery path to the Works: sandboxed Bash cannot reach Unix sockets or loopback (docs/research/works-endpoint-reachability-from-runner-sandboxes.md), but hooks run as the CLI's own process outside the tool sandbox (UNVERIFIED; test). Fallback: hook appends to a spool file in the run dir, Works tails it (matches ADR 0018 spool).
3. Paste vs send-keys: bracketed paste into a TUI mid-turn may be queued, dropped, or interleave with a permission dialog. Gate nudges on state = idle; test paste while working for both CLIs.
4. `codex queue` semantics and whether Codex session id/name can be pinned at launch: UNVERIFIED.
5. Claude inbox socket (ADR 0001) mechanism not found in primary docs fetched here; verify separately.
6. Security: unattended interactive sessions run at developer permissions and may block forever on a prompt; need timeouts and an awaiting-permission surface in the dashboard (map "Not yet specified"). Never pass `--dangerously-skip-permissions`/`--dangerously-bypass-*` (ADR 0011).
7. Use a dedicated tmux socket per Works instance; set `remain-on-exit on` so exit status and final screen can be read before cleanup.
