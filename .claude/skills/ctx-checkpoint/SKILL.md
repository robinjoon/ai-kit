---
name: ctx-checkpoint
description: Capture the current work as a self-contained ctx checkpoint. Use when the user asks to save progress, record a milestone or completion, recover state, or merge multiple context heads.
---

# Create a ctx checkpoint

Save durable semantic state without committing or modifying Git.

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

## Resolve the active task

1. Run:

   ```text
   <ctx> <global-args> --json resolve
   ```

2. Require an active task. Do not create one implicitly. If none is bound, query:

   ```text
   <ctx> <global-args> --json task list
   ```

3. Present viable tasks and wait for a user choice. Bind the chosen task with:

   ```text
   <ctx> <global-args> --json task switch <task-id-or-alias>
   ```

Never infer repository, task, checkpoint, or parent IDs.

## Build and save the capture

Read [references/capture-input.md](references/capture-input.md) completely before
constructing input. Synthesize a self-contained current state from the conversation,
inspected repository material, and validations that actually ran. Send the JSON on
stdin; do not persist conversation content merely to invoke the CLI.

Choose purpose deliberately:

- `progress` for ordinary intermediate state
- `milestone` for a completed stage
- `completion` for completed work
- `recovery` for reconstructed state after interruption
- `merge` only for user-selected integration of at least two current heads

Run a normal checkpoint as:

```text
<ctx> <global-args> --json checkpoint --input - --purpose <purpose> [--parent <id>]
```

For `merge`, first use `status` to show current heads, obtain the user's selection,
and create one complete semantic synthesis:

```text
<ctx> <global-args> --json checkpoint --input - --purpose merge \
  --parent <head-1> --parent <head-2> [--parent <head-n>]
```

Merge context only; never merge code. Report checkpoint ID, stability, digest, and
whether the CLI deduplicated the request.

## Handle failures

- On exit 4, show task or head candidates and wait; never select the newest ID.
- On exit 5, correct generated capture once when the diagnostic is actionable.
  Otherwise report the invariant failure without retrying unchanged input.
- On exits 6 or 7, report the diagnostic without changing Git or claiming success.
- Parse successful stdout only and treat stderr as diagnostics.
