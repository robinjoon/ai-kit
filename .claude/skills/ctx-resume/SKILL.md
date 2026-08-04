---
name: ctx-resume
description: Restore a ctx task as Git-aware continuation context. Use when the user asks to resume, continue, pick up, or import work previously saved or handed off through ctx.
---

# Resume a ctx task

Load the complete continuation context and compare its Git baseline without changing
the working tree.

## Prepare the CLI

1. When `CTX_BIN` is set, require it to name an executable and use it exactly.
   Otherwise resolve `ctx` from `PATH`. Do not fall back to `PATH` after an invalid
   `CTX_BIN` override.
2. Run `<ctx> --version`. Require release `0.1.0` or newer; accept `ctx dev` as an
   unversioned source build. Do not install, download, search the working tree, or
   use `go run` as fallback.
3. Use client `com.openai.codex` in Codex or
   `com.anthropic.claude-code` in Claude Code.
4. Construct logical argv and prefer a structured argv execution API. If the host
   tool accepts shell text only, shell-escape each dynamic value as exactly one
   argument. Never use `eval`, unquoted concatenation, or command substitution.
   Pass `--cwd`, optional `--store` from `CTX_STORE`, and a real optional
   `--session-id` from `CTX_SESSION_ID`.

## Resume workflow

Run without explicit selectors first when the user did not provide them:

```text
<ctx> <global-args> resume --max-bytes 32768
```

When the user supplied a filesystem remote or `CTX_SYNC_REMOTE` is set, use:

```text
<ctx> <global-args> resume --max-bytes 32768 --sync --remote <filesystem-store>
```

When the user selected a task or checkpoint, add exact CLI-provided selectors:

```text
<ctx> <global-args> resume --task <task-id-or-alias> \
  [--checkpoint <checkpoint-id>] --max-bytes 32768
```

Do not use `--json` for resume. Consume the successful Markdown stdout in full as
the current continuation context; do not replace it with a summary or a list of
paths. Respect a user-specified output budget when provided.

## Resolve ambiguity

On exit 4, run:

```text
<ctx> <global-args> --json task list
```

Present viable tasks and the selected task summary's exact `stable_head_ids`, then
wait for the user. `task list` works even when that task is not the active binding;
use `status` for detailed graph context only when the ambiguous task is already
active. Never switch tasks merely to inspect heads, and never choose by timestamp,
ULID order, branch, or apparent recency. Retry with explicit `--task` and, when
needed, `--checkpoint`.

## Handle sync and Git differences

- On exit 8, state that freshness is unknown. Ask whether to continue from the last
  local state; only after approval retry without `--sync`.
- Surface the rendered `Git comparison` before proceeding when it reports a
  difference. Let the user decide whether to continue or prepare Git first.
- Never checkout, switch, merge, patch, commit, pull, or push Git automatically.
- On exit 3, explain the missing task/checkpoint and offer `ctx-start` or explicit
  selection. On exits 6 or 7, report the diagnostic and stop.

After a successful resume, identify the selected task and checkpoint, acknowledge
important blockers or next actions, and continue only within the user's request.
