# ctx skill behavior contract v1

This contract governs the portable `ctx-start`, `ctx-checkpoint`, `ctx-handoff`,
`ctx-resume`, and `ctx-status` Agent Skills. The same skill sources run in Codex
and Claude Code; only the explicit invocation syntax and client identifier differ.

## Packaging and invocation

The canonical sources live under `.claude/skills/`. Project-local Codex entries
under `.agents/skills/` point to those same directories so the instructions cannot
drift.

| Host | Invocation | `--client` value |
|---|---|---|
| Codex | `$ctx-start`, `$ctx-checkpoint`, `$ctx-handoff`, `$ctx-resume`, `$ctx-status` | `com.openai.codex` |
| Claude Code | `/ctx-start`, `/ctx-checkpoint`, `/ctx-handoff`, `/ctx-resume`, `/ctx-status` | `com.anthropic.claude-code` |

Each skill remains usable as a standalone Agent Skills directory. OpenAI-specific
`agents/openai.yaml` contains display metadata only and does not change behavior.

## CLI discovery and compatibility

Before the first `ctx` operation in a skill invocation:

1. If `CTX_BIN` is set, use that executable exactly and quote it as one argv item.
2. Otherwise resolve `ctx` from `PATH`.
3. Do not search the working tree, download a binary, install a package, or fall
   back to `go run`.
4. Run `<ctx> --version` before any mutating `ctx` command.
5. Accept a release SemVer of `0.1.0` or newer. A single leading `v` may be
   normalized for comparison. Accept `ctx dev` as a source build, but never
   describe it as a versioned release.
6. Stop with setup guidance when the executable is absent, non-executable, the
   version output is malformed, or the release is too old.

The skills, not the CLI, recognize these optional environment values:

| Variable | Meaning |
|---|---|
| `CTX_BIN` | Exact `ctx` executable override |
| `CTX_STORE` | Local sidecar root passed through `--store` |
| `CTX_SYNC_REMOTE` | Filesystem sync root used only by handoff and resume |
| `CTX_SESSION_ID` | Real host session ID passed through `--session-id`; never synthesize one |

Construct logical argv and prefer a structured argv execution API. When a host tool
accepts only shell text, shell-escape every dynamic value as exactly one argument.
Never use `eval`, unquoted concatenation, or command substitution. Pass the target
working directory through `--cwd`, the host identifier through `--client`, and
optional store/session values only when present.

## Shared behavioral rules

- Call `ctx ... --json resolve` before creating a task or semantic capture.
- Let the CLI determine repository, task, checkpoint, parent, and working-copy IDs.
  Do not derive them from paths, branches, timestamps, or remote URLs.
- Use `--json` for structured commands and parse successful stdout only. Treat
  stderr as diagnostics. `ctx resume` is the exception: consume its complete
  Markdown stdout as continuation context.
- Send capture JSON through stdin with `--input -`; do not persist conversation
  content merely to invoke the CLI.
- Do not run Git checkout, switch, merge, patch, commit, push, or other Git
  mutations. `ctx` observes Git but does not synchronize code.
- Do not retry an unchanged write indefinitely. A successful immutable record may
  be deduplicated by the CLI and must still be treated as success.
- Preserve ambiguity. Never choose a task or checkpoint because it has the newest
  timestamp or lexicographically greatest ID.

## Capture boundary

Only `ctx-checkpoint` and `ctx-handoff` create semantic capture input. The input has
exactly four top-level fields: `input_version`, `work_status`, `capture`, and
`context`.

- Capture the complete current state, not a delta from the previous checkpoint.
- Use the `repo_id` returned by the immediately preceding `resolve` in repository
  resource and validation references.
- Keep paths repository-relative. Do not include task IDs, checkpoint IDs, parent
  IDs, timestamps, producer/session data, Git observations, or hashes; the CLI adds
  those fields.
- Use empty arrays for reviewed sections with no entries.
- If a semantic section was not reviewed, set capture completeness to `partial`,
  add a concrete warning, and list its exact `context.*` path in
  `omitted_sections`.
- Record only validations that actually ran and only findings supported by the
  captured evidence.
- A handoff requires `complete`. Never fabricate missing information or silently
  downgrade a handoff to a draft checkpoint.

## Selection and synchronization

When selection is ambiguous, query `task list` or `status` and wait for the user's
choice. For checkpoint or handoff parent selection, present current checkpoint
`heads`. For resume, present the selected task's `stable_head_ids` from `task list`;
this also works for an inactive task. Merge checkpoints integrate meaning only;
they never merge code.

Use filesystem sync only when the user supplied a remote or `CTX_SYNC_REMOTE` is
configured. If handoff sync fails, distinguish the locally created handoff from the
failed push and retry only `sync --direction push` when appropriate. A failed
`handoff --sync` returns before emitting handoff JSON, so never derive or guess its
handoff ID, checkpoint ID, or path. If resume sync fails, do not claim the local
store is current; ask before resuming without sync.

## Exit handling

| Exit | Required handling |
|---:|---|
| 1 | Report the unexpected diagnostic and stop. |
| 2 | Treat it as a skill invocation defect or missing required option; stop rather than guessing. |
| 3 | Explain which task, checkpoint, or binding is absent and offer start or explicit selection. |
| 4 | List candidates and ask the user; never auto-select. |
| 5 | Correct a generated capture once when the diagnostic is actionable; otherwise report the validation failure. |
| 6 | Report the Git observation failure without changing Git. |
| 7 | Report sidecar failure and do not claim anything was saved. |
| 8 | Report sync failure and clearly separate known local state from unknown remote state. |
