---
name: ctx-handoff
description: Create a stable ctx checkpoint and handoff for another coding agent. Use when the user asks to transfer, hand over, or continue work in Codex, Claude Code, another interface, or another device.
---

# Create a ctx handoff

Create one complete stable checkpoint and its thin handoff pointer without changing
Git.

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

## Prepare the handoff

1. Run `<ctx> <global-args> --json resolve` and require an active task.
2. Run `<ctx> <global-args> --json status`. If it reports multiple current
   checkpoint `heads`, inspect their stability in `checkpoint_graph` and present
   the exact IDs. When more than one stable head would remain, require a
   user-approved merge checkpoint before handoff; selecting only one stable branch
   would leave the receiver unable to resume unambiguously. With at most one stable
   head, obtain the user's current-head choice when a parent is still ambiguous.
   Parent selection is based on current heads, not only stable heads.
3. Read [references/capture-input.md](references/capture-input.md) completely.
4. Review every semantic section and construct a complete, self-contained capture.
   If any section cannot be reviewed, stop and state what is missing. Never mark
   unknown content complete or silently create a draft checkpoint.
5. Set the target system from the actual destination:
   - Claude Code: `com.anthropic.claude-code`
   - Codex: `com.openai.codex`
6. Add `--target-interface` only when known. Use one of `desktop`, `cli`, `ide`,
   `web`, `api`, or `unknown`.

Create a local handoff with capture JSON on stdin:

```text
<ctx> <global-args> --json handoff --input - \
  --target-system <system> [--target-interface <interface>] [--parent <head>]
```

When the user supplied a filesystem remote or `CTX_SYNC_REMOTE` is set, add:

```text
--sync --remote <filesystem-store>
```

Do not discover or invent a remote. Explain that ctx sync transfers context only;
the user must prepare code through their normal Git workflow.

## Handle synchronization and failures

- On exit 4, show task or head candidates and wait; never auto-select. If handoff
  reports multiple stable heads, create a semantic merge checkpoint only after the
  user approves the heads and integrated context, then retry handoff.
- On exit 5, correct generated capture once only when actionable.
- On exit 8 from `handoff --sync`, treat the local handoff as created and the push
  as failed. The CLI withholds successful handoff JSON in this path, so do not
  guess or derive the handoff ID, checkpoint ID, or local path. Do not rerun the
  whole handoff. Report the split state and, when the user wants a retry, run:

  ```text
  <ctx> <global-args> --json sync --remote <filesystem-store> --direction push
  ```

- On exits 6 or 7, report the diagnostic without changing Git or claiming remote
  success.
- After successful handoff JSON, report handoff ID, checkpoint ID, target, local
  path, and the sync result confirmed by the command's successful exit.
