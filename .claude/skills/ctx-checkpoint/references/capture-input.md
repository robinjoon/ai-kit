# Capture input

Create one JSON object with exactly these top-level fields and send it to
`ctx ... checkpoint --input -` on stdin.

```json
{
  "input_version": 1,
  "work_status": "active",
  "capture": {
    "completeness": "complete",
    "warnings": [],
    "omitted_sections": []
  },
  "context": {
    "title": "Concise task state title",
    "summary": "Self-contained current-state summary",
    "objective": {
      "goal": "Current goal",
      "success_criteria": ["Observable completion condition"],
      "scope": {"in_scope": [], "out_of_scope": []}
    },
    "constraints": [],
    "assumptions": [],
    "findings": [],
    "decisions": [],
    "progress": {"completed": [], "current": []},
    "next_actions": [
      {
        "action_id": "action-next",
        "description": "Describe the next concrete action",
        "priority": "normal",
        "done_when": "State the observable completion condition",
        "dependencies": [],
        "resource_refs": [],
        "evidence_refs": []
      }
    ],
    "blockers": [],
    "open_questions": [],
    "validations": [],
    "relevant_resources": []
  }
}
```

## Non-empty item shapes

Use only the fields listed below; every item rejects unknown fields. Fields marked
optional may be omitted. All other fields are required.

- Constraint: `text`, `strength` (`hard` or `soft`), `status` (`active`,
  `satisfied`, or `superseded`), `evidence_refs`.
- Assumption: `text`, `basis`, `status` (`active`, `validated`, or `invalidated`),
  `evidence_refs`.
- Finding: `text`, optional `details`, `evidence_refs`.
- Decision: `decision_id`, `statement`, `rationale`, `status` (`proposed`,
  `accepted`, or `superseded`), `alternatives`, `consequences`, `supersedes`,
  `evidence_refs`.
- Completed or current work item: `summary`, optional `current_state`, optional
  `result`, `resource_refs`, `evidence_refs`. Prefer `result` for completed work
  and `current_state` for current work.
- Next action: `action_id`, `description`, `priority` (`high`, `normal`, or `low`),
  `done_when`, `dependencies`, `resource_refs`, `evidence_refs`.
- Blocker: `blocker_id`, `description`, `impact`, `unblock_condition`, `status`
  (`active` or `resolved`), `evidence_refs`.
- Open question: `question`, `impact`, `evidence_refs`.
- Validation: `validation_id`, `kind` (`test`, `build`, `lint`, `typecheck`,
  `manual`, or `other`), optional `command`, optional `cwd` as
  `{"repo_id":"<resolved repo ID>","path":"<repo-relative path>"}`, `outcome`
  (`passed`, `failed`, `partial`, `not-run`, or `unknown`), optional integer or
  null `exit_code`, `summary`, optional RFC 3339 `observed_at`, optional
  `output_resource_ref`, `evidence_refs`.
- Relevant resource: `resource_id`, `locator`, `relation` (`created`, `modified`,
  `deleted`, `reviewed`, `planned`, `reference`, or `generated`), `role`, `note`,
  optional `selection`, optional `media_type`, optional `format`, optional
  `resource_digest`, and required `extensions` (use `{}` when none).
- `context.additional_context` is an optional non-blank string, not an object.

A resource `locator` is exactly one of:

- `{"kind":"repo_path","repo_id":"<resolved repo ID>","path":"<repo-relative path>"}`
- `{"kind":"store_path","path":"<store-relative path>"}`
- `{"kind":"home_path","device_id":"<device ID>","path":"<home-relative path>"}`
- `{"kind":"uri","uri":"<absolute URI>","scope":"portable"}` or device scope,
  which also requires `device_id`. A `file:` URI must use device scope.

An evidence reference is `{"ref_id":"<resource ID>"}` with optional `selector`
and optional `excerpt`. A selector is exactly one of `line_range` with
`start_line`/`end_line`, `byte_range` with `start_byte`/`end_byte`, `time_range`
with RFC 3339 `start_at`/`end_at`, `message_ids` with a non-empty `message_ids`
array, or `opaque` with `value`. End values cannot precede start values.

IDs such as `decision_id`, `action_id`, `blocker_id`, `validation_id`, and
`resource_id` start with an ASCII letter and then use letters, digits, `.`, `_`,
`:`, or `-` (at most 128 characters). Every `resource_refs` and
`output_resource_ref` value must name a relevant resource. Every `evidence_refs`
entry must name a relevant resource and stay within its selection. Dependencies
must name next actions; `supersedes` must name decisions; both graphs must be
acyclic and IDs within each collection must be unique.

Plural fields are arrays. `resource_refs`, `dependencies`, `supersedes`,
`alternatives`, and `consequences` contain strings; `evidence_refs` contains
evidence-reference objects. This compact fragment shows valid cross-reference
wiring after replacing the resolved repository ID and content:

```jsonc
{
  "decisions": [{
    "decision_id": "decision-format",
    "statement": "Use one portable capture format",
    "rationale": "Both clients need the same state",
    "status": "accepted",
    "alternatives": [],
    "consequences": ["Both clients validate the same fields"],
    "supersedes": [],
    "evidence_refs": [{"ref_id": "resource-readme", "selector": {"kind": "line_range", "start_line": 1, "end_line": 3}}]
  }],
  "progress": {
    "completed": [{
      "summary": "Documented the format",
      "result": "The contract is readable",
      "resource_refs": ["resource-readme"],
      "evidence_refs": []
    }],
    "current": []
  },
  "validations": [{
    "validation_id": "validation-readme",
    "kind": "manual",
    "command": "test -s README.md",
    "cwd": {"repo_id": "resolved-repo-id", "path": "."},
    "outcome": "passed",
    "exit_code": 0,
    "summary": "README exists and is non-empty",
    "output_resource_ref": "resource-readme",
    "evidence_refs": []
  }],
  "relevant_resources": [{
    "resource_id": "resource-readme",
    "locator": {"kind": "repo_path", "repo_id": "resolved-repo-id", "path": "README.md"},
    "relation": "reviewed",
    "role": "documentation",
    "note": "Describes the portable format",
    "selection": {"kind": "line_range", "start_line": 1, "end_line": 20},
    "media_type": "text/markdown",
    "format": "markdown",
    "extensions": {}
  }]
}
```

Line selectors start at 1 and byte selectors at 0; message IDs are non-empty and
unique, and an opaque selector has a non-empty value. A narrower evidence selector
must be a subset of the resource selection. Values in `resource_refs`,
`dependencies`, and `supersedes` are unique within each array.
Portable URI locators forbid `device_id`; device URI locators require it. `role`
and `format` use lowercase identifiers such as `documentation` or `json-schema`.
When supplied, `resource_digest` is `sha256:` plus 64 lowercase hexadecimal
characters.

Apply these rules:

- Set `work_status` to exactly one of `active`, `blocked`, `completed`, or
  `abandoned`.
- Capture the full current meaning of the work, not only changes since a prior
  checkpoint.
- Preserve stable semantic IDs when the same decision, action, blocker, resource,
  or validation remains present.
- Keep every evidence, resource, dependency, and supersedes reference resolvable
  inside this capture.
- Use the `repo_id` from the immediately preceding `ctx resolve` for repo-path
  resources and validation working directories. Keep repository paths relative.
- Include only validations that actually ran and their observed outcomes.
- Do not add task/checkpoint IDs, parents, timestamps, producer/session data, Git
  state, or digests. The CLI owns machine-derived fields.
- Use `partial` with a concrete warning and exact `context.*` omitted path when a
  section was not reviewed. An empty reviewed section remains `[]`.
- A `complete` capture has empty `warnings` and `omitted_sections` arrays.
- An omitted path must be one of `context.constraints`, `context.assumptions`,
  `context.findings`, `context.decisions`, `context.progress`,
  `context.next_actions`, `context.blockers`, `context.open_questions`,
  `context.validations`, `context.relevant_resources`, or
  `context.additional_context`.
- `blocked` requires an active blocker. `completed` must have no current work or
  active blocker.
- Purpose `completion` requires `work_status: completed` and a complete capture.
