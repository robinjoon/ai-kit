---
name: ctx-start
description: Start a new ctx task and bind it to the current Codex or Claude Code client. Use when the user asks to begin, initialize, or track a new unit of development work with ctx.
---

# Start a ctx task

Create a new logical task without changing Git.

## Prepare the CLI

1. When `CTX_BIN` is set, require it to name an executable and use it exactly.
   Otherwise resolve `ctx` from `PATH`. Do not fall back to `PATH` after an invalid
   `CTX_BIN` override.
2. Run `<ctx> --version` before any mutation. Require release `0.1.0` or newer;
   accept `ctx dev` only as an unversioned source build. Stop instead of installing,
   downloading, searching the working tree, or falling back to `go run`.
3. Set the client to `com.openai.codex` in Codex or
   `com.anthropic.claude-code` in Claude Code.
4. Construct logical argv and prefer a structured argv execution API. If the host
   tool accepts shell text only, shell-escape each dynamic value as exactly one
   argument. Never use `eval`, unquoted concatenation, or command substitution.
   Add `--cwd <target-directory>`, optional `--store <CTX_STORE>`, and optional
   `--session-id <CTX_SESSION_ID>` only when those real values are available.

Use this notation below:

```text
<ctx> <global-args> --json <command>
```

## Start workflow

1. Derive a concise title from the user's stated work. Ask only when no meaningful
   title can be determined.
2. Resolve the repository before creating the task:

   ```text
   <ctx> <global-args> --json resolve
   ```

   Use the returned IDs exactly. Do not derive a repository ID from a path, branch,
   remote URL, or timestamp.
3. Create and bind the new task:

   ```text
   <ctx> <global-args> --json task create --title <title>
   ```

   Add repeated `--alias <alias>` argv items only for aliases supplied or explicitly
   requested by the user. Do not invent aliases.
4. Report the returned task ID, title, aliases, and client binding in the user's
   language.

Create a new task when the user explicitly asks to start new work even if `resolve`
shows an existing active task. Do not checkpoint, switch, or close the prior task
implicitly. Never run `repo link`; repository identity migration requires an
explicit user decision.

## Handle failures

- On exit 3, explain the missing repository context or binding and stop.
- On exit 4, list candidates with `task list` and ask the user; never auto-select.
- On exit 5, correct only an actionable title or alias problem once.
- On exits 6 or 7, report the Git or sidecar diagnostic without changing Git or
  claiming the task was saved.
- Treat stderr as diagnostics and parse successful stdout only.
