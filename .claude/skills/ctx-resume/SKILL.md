---
name: ctx-resume
description: Resume the latest local ctx checkpoint when the user asks to continue work in Claude Code or Codex on the same computer and Git working copy.
---

# Resume the latest context

1. Resolve `CTX_BIN` or `ctx` from `PATH` and verify `ctx --version`. Do not
   install or use `go run`.
2. Run the command below. The CLI automatically selects the current repository,
   worktree, and branch scope; never select a checkpoint from another scope.

   ```text
   ctx --cwd <repo> --client <client> --json resume
   ```

   Add `--store <CTX_STORE>` only when set.
3. Read the latest checkpoint's goal, summary, decisions, next actions, blockers,
   and Git differences into the current agent context.
4. Show Git differences before continuing. Never change Git automatically.
5. Continue from the next actions when the user asked for execution, or summarize
   them when the user asked only to inspect the state.

Switching branches or worktrees selects their independent latest contexts. There
is no task selection, remote sync, handoff pointer, parent, head, or merge.
