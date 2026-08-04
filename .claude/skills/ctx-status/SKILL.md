---
name: ctx-status
description: Inspect and explain ctx task, checkpoint, Git, snapshot, and sync state without creating semantic or runtime records, changing task bindings, or changing Git. Use when the user asks what ctx knows, whether work is resumable, or why selection or sync is blocked.
---

# Inspect ctx status

Explain ctx and Git observations without creating semantic/runtime records, changing
task bindings, or changing Git.

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

## Inspect and explain

Run:

```text
<ctx> <global-args> --json status
```

Explain, in the user's language:

- repository and working-copy identity
- active task, work status, and aliases
- all checkpoint heads and stable heads
- checkpoint graph purpose, stability, and work status
- current Git HEAD, operation, upstream, and worktree observation
- Git baseline differences or why comparison is unavailable
- latest runtime snapshot, when present
- last sync remote, direction, status, time, and failure message, when present

If no task is active, optionally run:

```text
<ctx> <global-args> --json task list
```

Present selectable tasks without binding one. When multiple heads exist, explain the
ambiguity and the available explicit resume or semantic merge choices. Never select
the newest head automatically.

## Preserve non-mutating workflow behavior

Do not run `task switch`, `snapshot`, `checkpoint`, `handoff`, `sync`, or any Git
mutation as part of status inspection. The CLI may create or refresh `repo.yaml`
working-copy metadata while resolving the repository; do not describe that metadata
registration as a semantic checkpoint, snapshot, binding change, or strictly
read-only filesystem operation. Treat successful stdout as data and stderr as
diagnostics. On exits 3, 6, or 7, report the missing state, Git observation failure,
or sidecar failure without claiming a current status.
