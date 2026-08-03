#!/usr/bin/env python3
"""Validate ctx v1 schemas, examples, and representative rejection cases."""

from __future__ import annotations

import copy
import hashlib
import json
import sys
from pathlib import Path

import rfc8785
from jsonschema import Draft202012Validator, FormatChecker
from referencing import Registry, Resource


ROOT = Path(__file__).resolve().parents[1]
SCHEMA_DIR = ROOT / "schemas" / "v1"

SCHEMA_FILES = {
    "common": SCHEMA_DIR / "common.schema.json",
    "capture_input": SCHEMA_DIR / "capture-input.schema.json",
    "runtime": SCHEMA_DIR / "runtime-snapshot.schema.json",
    "checkpoint": SCHEMA_DIR / "checkpoint.schema.json",
    "handoff": SCHEMA_DIR / "handoff.schema.json",
}

EXAMPLE_FILES = {
    "capture_input": SCHEMA_DIR / "examples" / "capture-input.example.json",
    "runtime": SCHEMA_DIR / "examples" / "runtime-snapshot.example.json",
    "checkpoint": SCHEMA_DIR / "examples" / "checkpoint.example.json",
    "handoff": SCHEMA_DIR / "examples" / "handoff.example.json",
}

HANDOFF_MARKDOWN_EXAMPLE = SCHEMA_DIR / "examples" / "handoff.example.md"


def load_json(path: Path) -> dict:
    with path.open(encoding="utf-8") as file:
        return json.load(file)


def build_registry(schemas: dict[str, dict]) -> Registry:
    return Registry().with_resources(
        (schema["$id"], Resource.from_contents(schema))
        for schema in schemas.values()
    )


def validator_for(
    name: str,
    schemas: dict[str, dict],
    registry: Registry,
) -> Draft202012Validator:
    return Draft202012Validator(
        schemas[name],
        registry=registry,
        format_checker=FormatChecker(),
    )


def content_digest(record: dict) -> str:
    digest_input = copy.deepcopy(record)
    del digest_input["content_digest"]
    return "sha256:" + hashlib.sha256(rfc8785.dumps(digest_input)).hexdigest()


def value_digest(value: object) -> str:
    return "sha256:" + hashlib.sha256(rfc8785.dumps(value)).hexdigest()


def yaml_scalar(value: object) -> str:
    if isinstance(value, str):
        return json.dumps(value, ensure_ascii=False)
    if isinstance(value, int) and not isinstance(value, bool):
        return str(value)
    if isinstance(value, dict):
        return rfc8785.dumps(value).decode("utf-8")
    raise TypeError(f"unsupported handoff YAML scalar: {type(value).__name__}")


def render_handoff(record: dict) -> tuple[str, str]:
    lines = ["---"]
    top_level_order = [
        "schema_version",
        "record_type",
        "handoff_id",
        "task_id",
        "checkpoint_id",
        "checkpoint_digest",
        "generated_at",
    ]
    for key in top_level_order:
        lines.append(f"{key}: {yaml_scalar(record[key])}")

    lines.append("producer:")
    producer_order = [
        "actor_type",
        "system",
        "version",
        "adapter",
        "device_id",
        "extensions",
    ]
    for key in producer_order:
        if key in record["producer"]:
            lines.append(f"  {key}: {yaml_scalar(record['producer'][key])}")

    if "target" in record:
        lines.append("target:")
        for key in ("system", "interface", "device_id"):
            if key in record["target"]:
                lines.append(f"  {key}: {yaml_scalar(record['target'][key])}")

    for key in ("render_profile", "rendered_body_digest", "extensions"):
        lines.append(f"{key}: {yaml_scalar(record[key])}")

    body = (
        "# ctx handoff\n\n"
        f"Load checkpoint `{record['checkpoint_id']}` for task "
        f"`{record['task_id']}` through `ctx resume`.\n"
    )
    document = "\n".join(lines) + "\n---\n\n" + body
    return document, body


def valid_variants(examples: dict[str, dict]) -> list[tuple[str, str, dict]]:
    variants: list[tuple[str, str, dict]] = []

    partial_input = copy.deepcopy(examples["capture_input"])
    partial_input["capture"] = {
        "completeness": "partial",
        "warnings": ["Findings were not reviewed."],
        "omitted_sections": ["context.findings"],
    }
    variants.append(("partial capture input", "capture_input", partial_input))

    completed_input = copy.deepcopy(examples["capture_input"])
    completed_input["work_status"] = "completed"
    completed_input["context"]["progress"]["current"] = []
    completed_input["context"]["next_actions"] = []
    variants.append(("completed capture input", "capture_input", completed_input))

    detached = copy.deepcopy(examples["runtime"])
    detached["repository"]["git"]["head"] = {
        "state": "detached",
        "oid": "0123456789abcdef0123456789abcdef01234567",
    }
    variants.append(("detached HEAD", "runtime", detached))

    unborn = copy.deepcopy(examples["runtime"])
    unborn["repository"]["git"]["head"] = {
        "state": "unborn",
        "symbolic_ref": "refs/heads/main",
    }
    del unborn["repository"]["git"]["upstream"]
    variants.append(("unborn HEAD", "runtime", unborn))

    clean = copy.deepcopy(examples["runtime"])
    clean["repository"]["git"]["worktree"] = {
        "state": "clean",
        "fingerprint": copy.deepcopy(
            examples["runtime"]["repository"]["git"]["worktree"]["fingerprint"]
        ),
        "changes": {
            "complete": True,
            "total_entries": 0,
            "untracked_included": True,
            "ignored_included": False,
            "entries": [],
        },
    }
    variants.append(("clean worktree", "runtime", clean))

    unknown = copy.deepcopy(examples["runtime"])
    unknown["repository"]["git"]["worktree"] = {
        "state": "unknown",
        "changes": {
            "complete": False,
            "total_entries": 0,
            "untracked_included": False,
            "ignored_included": False,
            "entries": [],
        },
        "diagnostic": "The worktree changed while it was being captured.",
    }
    unknown["capture"] = {
        "completeness": "partial",
        "warnings": ["Git worktree capture was incomplete."],
        "omitted_sections": ["repository.git.worktree"],
    }
    variants.append(("unknown worktree", "runtime", unknown))

    merge = copy.deepcopy(examples["checkpoint"])
    merge["purpose"] = "merge"
    merge["parent_ids"].append("01ARZ3NDEKTSV4RRFFQ69G5FAR")
    variants.append(("merge checkpoint", "checkpoint", merge))

    completed = copy.deepcopy(examples["checkpoint"])
    completed["purpose"] = "completion"
    completed["work_status"] = "completed"
    completed["context"]["progress"]["current"] = []
    completed["context"]["next_actions"] = []
    variants.append(("completed checkpoint", "checkpoint", completed))

    return variants


def invalid_cases(examples: dict[str, dict]) -> list[tuple[str, str, dict]]:
    cases: list[tuple[str, str, dict]] = []

    input_with_cli_owned_field = copy.deepcopy(examples["capture_input"])
    input_with_cli_owned_field["checkpoint_id"] = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
    cases.append(
        (
            "capture input with CLI-owned checkpoint ID",
            "capture_input",
            input_with_cli_owned_field,
        )
    )

    blocked_input_without_blocker = copy.deepcopy(examples["capture_input"])
    blocked_input_without_blocker["work_status"] = "blocked"
    cases.append(
        (
            "blocked capture input without active blocker",
            "capture_input",
            blocked_input_without_blocker,
        )
    )

    completed_input_with_current_work = copy.deepcopy(examples["capture_input"])
    completed_input_with_current_work["work_status"] = "completed"
    cases.append(
        (
            "completed capture input with current work",
            "capture_input",
            completed_input_with_current_work,
        )
    )

    input_with_machine_omission = copy.deepcopy(examples["capture_input"])
    input_with_machine_omission["capture"] = {
        "completeness": "partial",
        "warnings": ["Git could not be inspected."],
        "omitted_sections": ["workspace.repositories.worktree"],
    }
    cases.append(
        (
            "capture input with CLI-owned omitted section",
            "capture_input",
            input_with_machine_omission,
        )
    )

    invalid_ulid = copy.deepcopy(examples["runtime"])
    invalid_ulid["snapshot_id"] = "81ARZ3NDEKTSV4RRFFQ69G5FAX"
    cases.append(("ULID first character outside 0-7", "runtime", invalid_ulid))

    missing_task = copy.deepcopy(examples["runtime"])
    del missing_task["task_id"]
    cases.append(
        ("active checkpoint without task ID", "runtime", missing_task)
    )

    dirty_without_fingerprint = copy.deepcopy(examples["runtime"])
    del dirty_without_fingerprint["repository"]["git"]["worktree"]["fingerprint"]
    cases.append(
        ("dirty worktree without fingerprint", "runtime", dirty_without_fingerprint)
    )

    rename_without_original = copy.deepcopy(examples["runtime"])
    rename_without_original["repository"]["git"]["worktree"]["changes"]["entries"][0][
        "worktree_status"
    ] = "renamed"
    cases.append(
        ("rename without original path", "runtime", rename_without_original)
    )

    device_log_without_device = copy.deepcopy(examples["runtime"])
    del device_log_without_device["session_refs"][0]["logs"][0]["locator"]["device_id"]
    cases.append(
        ("device-scoped home log without device ID", "runtime", device_log_without_device)
    )

    log_without_reference_id = copy.deepcopy(examples["runtime"])
    del log_without_reference_id["session_refs"][0]["logs"][0]["log_ref_id"]
    cases.append(("log without reference ID", "runtime", log_without_reference_id))

    portable_file_log = copy.deepcopy(examples["runtime"])
    portable_file_log["session_refs"][0]["logs"][0]["locator"] = {
        "kind": "uri",
        "uri": "file:///Users/example/session.jsonl",
        "scope": "portable",
    }
    cases.append(("portable file URI", "runtime", portable_file_log))

    portable_log_with_device = copy.deepcopy(examples["runtime"])
    portable_log_with_device["session_refs"][0]["logs"][0]["locator"] = {
        "kind": "uri",
        "uri": "https://example.invalid/session.jsonl",
        "scope": "portable",
        "device_id": "personal-macbook",
    }
    cases.append(
        ("portable URI with device ID", "runtime", portable_log_with_device)
    )

    partial_handoff = copy.deepcopy(examples["checkpoint"])
    partial_handoff["capture"] = {
        "completeness": "partial",
        "warnings": ["Some context was omitted."],
        "omitted_sections": ["context.findings"],
    }
    cases.append(("partial handoff checkpoint", "checkpoint", partial_handoff))

    merge_with_one_parent = copy.deepcopy(examples["checkpoint"])
    merge_with_one_parent["purpose"] = "merge"
    cases.append(("merge with one parent", "checkpoint", merge_with_one_parent))

    blocked_without_blocker = copy.deepcopy(examples["checkpoint"])
    blocked_without_blocker["work_status"] = "blocked"
    cases.append(
        ("blocked checkpoint without blockers", "checkpoint", blocked_without_blocker)
    )

    blocked_with_resolved_blocker = copy.deepcopy(examples["checkpoint"])
    blocked_with_resolved_blocker["work_status"] = "blocked"
    blocked_with_resolved_blocker["context"]["blockers"] = [
        {
            "blocker_id": "blocker-resolved",
            "description": "A previously resolved blocker.",
            "impact": "No current impact.",
            "unblock_condition": "Already satisfied.",
            "status": "resolved",
            "evidence_refs": [],
        }
    ]
    cases.append(
        (
            "blocked checkpoint with only resolved blockers",
            "checkpoint",
            blocked_with_resolved_blocker,
        )
    )

    completed_with_current_work = copy.deepcopy(examples["checkpoint"])
    completed_with_current_work["work_status"] = "completed"
    cases.append(
        ("completed checkpoint with current work", "checkpoint", completed_with_current_work)
    )

    path_traversal = copy.deepcopy(examples["checkpoint"])
    path_traversal["context"]["relevant_resources"][0]["locator"]["path"] = "../README.md"
    cases.append(("repository path traversal", "checkpoint", path_traversal))

    for label, path in (
        ("dot-prefixed repository path", "./README.md"),
        ("repository path with empty segment", "src//example.ts"),
        ("repository path with trailing slash", "src/"),
    ):
        invalid_path = copy.deepcopy(examples["checkpoint"])
        invalid_path["context"]["relevant_resources"][0]["locator"]["path"] = path
        cases.append((label, "checkpoint", invalid_path))

    conflict_mismatch = copy.deepcopy(examples["runtime"])
    conflict_mismatch["repository"]["git"]["worktree"]["changes"]["entries"][0][
        "conflict"
    ] = True
    cases.append(("conflict flag without unmerged status", "runtime", conflict_mismatch))

    unknown_with_complete_capture = copy.deepcopy(examples["runtime"])
    unknown_with_complete_capture["repository"]["git"]["worktree"] = {
        "state": "unknown",
        "changes": {
            "complete": False,
            "total_entries": 0,
            "untracked_included": False,
            "ignored_included": False,
            "entries": [],
        },
        "diagnostic": "The worktree could not be read.",
    }
    cases.append(
        (
            "unknown worktree with complete capture",
            "runtime",
            unknown_with_complete_capture,
        )
    )

    unknown_with_fingerprint = copy.deepcopy(examples["runtime"])
    unknown_with_fingerprint["repository"]["git"]["worktree"] = {
        "state": "unknown",
        "fingerprint": copy.deepcopy(
            examples["runtime"]["repository"]["git"]["worktree"]["fingerprint"]
        ),
        "changes": {
            "complete": False,
            "total_entries": 0,
            "untracked_included": False,
            "ignored_included": False,
            "entries": [],
        },
        "diagnostic": "The worktree could not be read.",
    }
    unknown_with_fingerprint["capture"] = {
        "completeness": "partial",
        "warnings": ["Git worktree capture was incomplete."],
        "omitted_sections": ["repository.git.worktree"],
    }
    cases.append(
        ("unknown worktree with fingerprint", "runtime", unknown_with_fingerprint)
    )

    unknown_with_complete_changes = copy.deepcopy(examples["runtime"])
    unknown_with_complete_changes["repository"]["git"]["worktree"] = {
        "state": "unknown",
        "changes": {
            "complete": True,
            "total_entries": 0,
            "untracked_included": False,
            "ignored_included": False,
            "entries": [],
        },
        "diagnostic": "The worktree could not be read.",
    }
    unknown_with_complete_changes["capture"] = {
        "completeness": "partial",
        "warnings": ["Git worktree capture was incomplete."],
        "omitted_sections": ["repository.git.worktree"],
    }
    cases.append(
        (
            "unknown worktree with complete change list",
            "runtime",
            unknown_with_complete_changes,
        )
    )

    checkpoint_with_unknown_git = copy.deepcopy(examples["checkpoint"])
    checkpoint_with_unknown_git["workspace"]["repositories"][0]["worktree"] = {
        "state": "unknown",
        "diagnostic": "The Git baseline could not be captured.",
    }
    cases.append(
        (
            "complete checkpoint with unknown Git baseline",
            "checkpoint",
            checkpoint_with_unknown_git,
        )
    )

    unknown_baseline_with_fingerprint = copy.deepcopy(examples["checkpoint"])
    baseline_fingerprint = copy.deepcopy(
        examples["checkpoint"]["workspace"]["repositories"][0]["worktree"][
            "fingerprint"
        ]
    )
    unknown_baseline_with_fingerprint["purpose"] = "progress"
    unknown_baseline_with_fingerprint["stability"] = "draft"
    unknown_baseline_with_fingerprint["capture"] = {
        "completeness": "partial",
        "warnings": ["Git baseline capture was incomplete."],
        "omitted_sections": ["workspace.repositories.worktree"],
    }
    unknown_baseline_with_fingerprint["workspace"]["repositories"][0][
        "worktree"
    ] = {
        "state": "unknown",
        "fingerprint": baseline_fingerprint,
        "diagnostic": "The Git baseline could not be captured.",
    }
    cases.append(
        (
            "unknown checkpoint baseline with fingerprint",
            "checkpoint",
            unknown_baseline_with_fingerprint,
        )
    )

    unknown_handoff_field = copy.deepcopy(examples["handoff"])
    unknown_handoff_field["summary"] = "This information belongs to the checkpoint."
    cases.append(
        ("handoff carrying duplicated semantic context", "handoff", unknown_handoff_field)
    )

    return cases


def main() -> int:
    schemas = {name: load_json(path) for name, path in SCHEMA_FILES.items()}
    examples = {name: load_json(path) for name, path in EXAMPLE_FILES.items()}

    for name, schema in schemas.items():
        Draft202012Validator.check_schema(schema)
        print(f"schema ok: {name}")

    registry = build_registry(schemas)

    failed = False
    for name, example in examples.items():
        errors = sorted(
            validator_for(name, schemas, registry).iter_errors(example),
            key=lambda error: list(error.absolute_path),
        )
        if errors:
            failed = True
            print(f"example failed: {name}", file=sys.stderr)
            for error in errors:
                location = "/".join(str(part) for part in error.absolute_path) or "<root>"
                print(f"  {location}: {error.message}", file=sys.stderr)
        else:
            print(f"example ok: {name}")

    for case_name, schema_name, instance in valid_variants(examples):
        errors = list(
            validator_for(schema_name, schemas, registry).iter_errors(instance)
        )
        if errors:
            failed = True
            print(f"valid variant failed: {case_name}", file=sys.stderr)
            for error in errors:
                location = "/".join(
                    str(part) for part in error.absolute_path
                ) or "<root>"
                print(f"  {location}: {error.message}", file=sys.stderr)
        else:
            print(f"valid variant ok: {case_name}")

    for name in ("runtime", "checkpoint"):
        expected_digest = examples[name]["content_digest"]
        actual_digest = content_digest(examples[name])
        if expected_digest != actual_digest:
            failed = True
            print(
                f"example content digest failed: {name}\n"
                f"  expected: {expected_digest}\n"
                f"  actual:   {actual_digest}",
                file=sys.stderr,
            )
        else:
            print(f"example content digest ok: {name}")

    expected_context_digest = examples["checkpoint"]["context_digest"]
    actual_context_digest = value_digest(examples["checkpoint"]["context"])
    if expected_context_digest != actual_context_digest:
        failed = True
        print(
            "checkpoint context digest failed\n"
            f"  expected: {expected_context_digest}\n"
            f"  actual:   {actual_context_digest}",
            file=sys.stderr,
        )
    else:
        print("checkpoint context digest ok")

    checkpoint_digest = examples["checkpoint"]["content_digest"]
    handoff_digest = examples["handoff"]["checkpoint_digest"]
    if handoff_digest != checkpoint_digest:
        failed = True
        print(
            "handoff checkpoint digest does not match checkpoint example",
            file=sys.stderr,
        )
    else:
        print("handoff checkpoint digest ok")

    rendered_handoff, rendered_body = render_handoff(examples["handoff"])
    golden_handoff = HANDOFF_MARKDOWN_EXAMPLE.read_text(encoding="utf-8")
    if golden_handoff != rendered_handoff:
        failed = True
        print("handoff Markdown golden fixture failed", file=sys.stderr)
    else:
        print("handoff Markdown golden fixture ok")

    rendered_body_digest = (
        "sha256:" + hashlib.sha256(rendered_body.encode("utf-8")).hexdigest()
    )
    expected_body_digest = examples["handoff"]["rendered_body_digest"]
    if rendered_body_digest != expected_body_digest:
        failed = True
        print(
            "handoff rendered body digest failed\n"
            f"  expected: {expected_body_digest}\n"
            f"  actual:   {rendered_body_digest}",
            file=sys.stderr,
        )
    else:
        print("handoff rendered body digest ok")

    for case_name, schema_name, instance in invalid_cases(examples):
        errors = list(
            validator_for(schema_name, schemas, registry).iter_errors(instance)
        )
        if not errors:
            failed = True
            print(f"invalid case unexpectedly passed: {case_name}", file=sys.stderr)
        else:
            print(f"invalid case rejected: {case_name}")

    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
