package schema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robinjoon/ai-kit/cli/internal/canonical"
)

func TestExamplesValidate(t *testing.T) {
	for _, test := range []struct {
		kind string
		file string
	}{
		{CaptureInput, "capture-input.example.json"},
		{RuntimeSnapshot, "runtime-snapshot.example.json"},
		{Checkpoint, "checkpoint.example.json"},
		{Handoff, "handoff.example.json"},
	} {
		t.Run(test.kind, func(t *testing.T) {
			record := readExample(t, test.file)
			if err := Validate(test.kind, record); err != nil {
				t.Fatalf("validate example: %v", err)
			}
		})
	}
}

func TestCaptureInputRejectsCLIOwnedField(t *testing.T) {
	record := readExample(t, "capture-input.example.json")
	record["checkpoint_id"] = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	if err := Validate(CaptureInput, record); err == nil {
		t.Fatal("expected CLI-owned field to fail validation")
	}
}

func TestCheckpointRejectsDigestMismatch(t *testing.T) {
	record := readExample(t, "checkpoint.example.json")
	record["content_digest"] = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := Validate(Checkpoint, record); err == nil {
		t.Fatal("expected mismatched content digest to fail validation")
	}
}

func TestCheckpointValidatesRecognizedHandoffTargetExtension(t *testing.T) {
	valid := readExample(t, "checkpoint.example.json")
	valid["purpose"] = "handoff"
	valid["extensions"] = map[string]any{
		"io.github.robinjoon.ctx": map[string]any{
			"handoff_target": map[string]any{"system": "com.openai.codex", "interface": "desktop"},
		},
	}
	digest, err := canonical.ContentDigest(valid)
	if err != nil {
		t.Fatal(err)
	}
	valid["content_digest"] = digest
	if err := Validate(Checkpoint, valid); err != nil {
		t.Fatalf("valid recognized handoff target: %v", err)
	}

	for name, target := range map[string]any{
		"not an object":     "codex",
		"empty":             map[string]any{},
		"invalid interface": map[string]any{"interface": "terminal"},
		"unknown property":  map[string]any{"system": "com.openai.codex", "channel": "cli"},
	} {
		t.Run(name, func(t *testing.T) {
			record := readExample(t, "checkpoint.example.json")
			record["purpose"] = "handoff"
			record["extensions"] = map[string]any{
				"io.github.robinjoon.ctx": map[string]any{"handoff_target": target},
			}
			digest, err := canonical.ContentDigest(record)
			if err != nil {
				t.Fatal(err)
			}
			record["content_digest"] = digest
			if err := Validate(Checkpoint, record); err == nil || !strings.Contains(err.Error(), "handoff_target") {
				t.Fatalf("malformed recognized handoff target error = %v", err)
			}
		})
	}

	nonHandoff := readExample(t, "checkpoint.example.json")
	nonHandoff["purpose"] = "progress"
	nonHandoff["extensions"] = valid["extensions"]
	digest, err = canonical.ContentDigest(nonHandoff)
	if err != nil {
		t.Fatal(err)
	}
	nonHandoff["content_digest"] = digest
	if err := Validate(Checkpoint, nonHandoff); err == nil || !strings.Contains(err.Error(), "only valid for handoff") {
		t.Fatalf("non-handoff recognized target error = %v", err)
	}
}

func TestCheckpointRejectsUnknownWorkspacePrimary(t *testing.T) {
	record := readExample(t, "checkpoint.example.json")
	workspace := record["workspace"].(map[string]any)
	workspace["primary_repo_id"] = "other-repo"
	digest, err := canonical.ContentDigest(record)
	if err != nil {
		t.Fatalf("content digest: %v", err)
	}
	record["content_digest"] = digest
	err = Validate(Checkpoint, record)
	if err == nil || !strings.Contains(err.Error(), "primary_repo_id") {
		t.Fatalf("expected unknown primary repository error, got %v", err)
	}
}

func TestCheckpointAcceptsDependencyOnActionWithoutDependencies(t *testing.T) {
	record := readExample(t, "checkpoint.example.json")
	context := record["context"].(map[string]any)
	actions := context["next_actions"].([]any)
	context["next_actions"] = append(actions, map[string]any{
		"action_id":     "action-followup",
		"description":   "Run the follow-up validation.",
		"priority":      "normal",
		"done_when":     "The follow-up validation is recorded.",
		"dependencies":  []any{"action-validate-schemas"},
		"resource_refs": []any{"res-checkpoint-schema"},
		"evidence_refs": []any{},
	})
	contextDigest, err := canonical.Digest(context)
	if err != nil {
		t.Fatalf("context digest: %v", err)
	}
	record["context_digest"] = contextDigest
	digest, err := canonical.ContentDigest(record)
	if err != nil {
		t.Fatalf("content digest: %v", err)
	}
	record["content_digest"] = digest
	if err := Validate(Checkpoint, record); err != nil {
		t.Fatalf("validate checkpoint with ordinary dependency: %v", err)
	}
}

func TestSelectorRangesAndEvidenceSubsets(t *testing.T) {
	valid := []struct {
		name     string
		source   map[string]any
		evidence map[string]any
	}{
		{
			name:     "line range",
			source:   map[string]any{"kind": "line_range", "start_line": 10, "end_line": 20},
			evidence: map[string]any{"kind": "line_range", "start_line": 12, "end_line": 18},
		},
		{
			name:     "byte range",
			source:   map[string]any{"kind": "byte_range", "start_byte": 10, "end_byte": 20},
			evidence: map[string]any{"kind": "byte_range", "start_byte": 11, "end_byte": 19},
		},
		{
			name:     "time range",
			source:   map[string]any{"kind": "time_range", "start_at": "2026-08-03T06:00:00Z", "end_at": "2026-08-03T07:00:00Z"},
			evidence: map[string]any{"kind": "time_range", "start_at": "2026-08-03T06:15:00Z", "end_at": "2026-08-03T06:45:00Z"},
		},
		{
			name:     "message IDs",
			source:   map[string]any{"kind": "message_ids", "message_ids": []any{"message-a", "message-b"}},
			evidence: map[string]any{"kind": "message_ids", "message_ids": []any{"message-b"}},
		},
		{
			name:     "opaque",
			source:   map[string]any{"kind": "opaque", "value": "selection-token"},
			evidence: map[string]any{"kind": "opaque", "value": "selection-token"},
		},
	}
	for _, test := range valid {
		t.Run(test.name, func(t *testing.T) {
			record := captureWithSelectors(t, test.source, test.evidence)
			if err := Validate(CaptureInput, record); err != nil {
				t.Fatalf("validate selector subset: %v", err)
			}
		})
	}
}

func TestSelectorViolationsAreRejected(t *testing.T) {
	invalid := []struct {
		name     string
		source   map[string]any
		evidence map[string]any
	}{
		{
			name:     "reversed source range",
			source:   map[string]any{"kind": "line_range", "start_line": 20, "end_line": 10},
			evidence: map[string]any{"kind": "line_range", "start_line": 20, "end_line": 20},
		},
		{
			name:     "evidence outside source range",
			source:   map[string]any{"kind": "line_range", "start_line": 1, "end_line": 20},
			evidence: map[string]any{"kind": "line_range", "start_line": 1, "end_line": 30},
		},
		{
			name:     "selector kind mismatch",
			source:   map[string]any{"kind": "line_range", "start_line": 1, "end_line": 20},
			evidence: map[string]any{"kind": "byte_range", "start_byte": 1, "end_byte": 10},
		},
		{
			name:     "byte range outside source",
			source:   map[string]any{"kind": "byte_range", "start_byte": 10, "end_byte": 20},
			evidence: map[string]any{"kind": "byte_range", "start_byte": 9, "end_byte": 20},
		},
		{
			name:     "time range outside source",
			source:   map[string]any{"kind": "time_range", "start_at": "2026-08-03T06:00:00Z", "end_at": "2026-08-03T07:00:00Z"},
			evidence: map[string]any{"kind": "time_range", "start_at": "2026-08-03T05:59:59Z", "end_at": "2026-08-03T06:30:00Z"},
		},
		{
			name:     "message ID outside source",
			source:   map[string]any{"kind": "message_ids", "message_ids": []any{"message-a", "message-b"}},
			evidence: map[string]any{"kind": "message_ids", "message_ids": []any{"message-c"}},
		},
		{
			name:     "opaque selector mismatch",
			source:   map[string]any{"kind": "opaque", "value": "selection-token"},
			evidence: map[string]any{"kind": "opaque", "value": "different-token"},
		},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			record := captureWithSelectors(t, test.source, test.evidence)
			if err := Validate(CaptureInput, record); err == nil {
				t.Fatal("expected selector violation to fail validation")
			}
		})
	}
}

func TestTimeRangeUsesExactFractionalSecondsAcrossOffsets(t *testing.T) {
	t.Run("valid subset", func(t *testing.T) {
		record := captureWithSelectors(t,
			map[string]any{
				"kind":     "time_range",
				"start_at": "2026-08-03T06:00:00.123456789123Z",
				"end_at":   "2026-08-03T07:00:00.000000000001Z",
			},
			map[string]any{
				"kind":     "time_range",
				"start_at": "2026-08-03T07:00:00.123456789124+01:00",
				"end_at":   "2026-08-03T07:30:00.999999999999+01:00",
			},
		)
		if err := Validate(CaptureInput, record); err != nil {
			t.Fatalf("validate exact time subset: %v", err)
		}
	})

	t.Run("reversed beyond nanoseconds", func(t *testing.T) {
		record := captureWithSelectors(t,
			map[string]any{
				"kind":     "time_range",
				"start_at": "2026-08-03T06:00:00.123456789123Z",
				"end_at":   "2026-08-03T06:00:00.123456789122Z",
			},
			map[string]any{
				"kind":     "time_range",
				"start_at": "2026-08-03T06:00:00.123456789123Z",
				"end_at":   "2026-08-03T06:00:00.123456789123Z",
			},
		)
		if err := Validate(CaptureInput, record); err == nil {
			t.Fatal("expected exact fractional reversal to fail validation")
		}
	})

	t.Run("outside source across offset", func(t *testing.T) {
		record := captureWithSelectors(t,
			map[string]any{
				"kind":     "time_range",
				"start_at": "2026-08-03T06:00:00.123456789123Z",
				"end_at":   "2026-08-03T07:00:00Z",
			},
			map[string]any{
				"kind":     "time_range",
				"start_at": "2026-08-03T07:00:00.123456789122+01:00",
				"end_at":   "2026-08-03T07:30:00+01:00",
			},
		)
		if err := Validate(CaptureInput, record); err == nil {
			t.Fatal("expected exact fractional subset violation to fail validation")
		}
	})
}

func TestDecodeRejectsDuplicateFindingKey(t *testing.T) {
	raw := []byte(`{"context":{"findings":[{"text":"first","text":"second"}]}}`)
	if _, err := Decode(raw); err == nil {
		t.Fatal("expected duplicate finding key to fail decoding")
	}
}

func TestLogSelectorConstrainsEvidenceSelector(t *testing.T) {
	record := readExample(t, "checkpoint.example.json")
	context := record["context"].(map[string]any)
	finding := context["findings"].([]any)[0].(map[string]any)
	evidence := finding["evidence_refs"].([]any)[0].(map[string]any)
	evidence["selector"] = map[string]any{
		"kind":        "message_ids",
		"message_ids": []any{"message-outside-log-selection"},
	}
	refreshCheckpointDigests(t, record)
	if err := Validate(Checkpoint, record); err == nil {
		t.Fatal("expected evidence outside log selector to fail validation")
	}
}

func TestExtensionsAreOpaqueToEvidenceValidation(t *testing.T) {
	record := readExample(t, "capture-input.example.json")
	context := record["context"].(map[string]any)
	resource := context["relevant_resources"].([]any)[0].(map[string]any)
	resource["extensions"] = map[string]any{
		"com.example.provider": map[string]any{
			"evidence_refs": []any{map[string]any{"ref_id": "provider-private-reference"}},
		},
	}
	if err := Validate(CaptureInput, record); err != nil {
		t.Fatalf("provider extension payload must remain opaque: %v", err)
	}
}

func TestSessionEvidenceSelectorIsRejected(t *testing.T) {
	record := readExample(t, "checkpoint.example.json")
	context := record["context"].(map[string]any)
	decision := context["decisions"].([]any)[0].(map[string]any)
	decision["evidence_refs"] = []any{map[string]any{
		"ref_id": "session-claude-authoring",
		"selector": map[string]any{
			"kind":        "message_ids",
			"message_ids": []any{"message-188"},
		},
	}}
	refreshCheckpointDigests(t, record)
	if err := Validate(Checkpoint, record); err == nil {
		t.Fatal("expected selector on session evidence reference to fail validation")
	}
}

func TestUnselectedResourceAndLogAllowAbsoluteEvidenceSelector(t *testing.T) {
	t.Run("resource", func(t *testing.T) {
		record := readExample(t, "capture-input.example.json")
		context := record["context"].(map[string]any)
		decision := context["decisions"].([]any)[0].(map[string]any)
		decision["evidence_refs"] = []any{map[string]any{
			"ref_id": "resource-capture-schema",
			"selector": map[string]any{
				"kind":       "line_range",
				"start_line": 1,
				"end_line":   5,
			},
		}}
		if err := Validate(CaptureInput, record); err != nil {
			t.Fatalf("validate absolute resource evidence selector: %v", err)
		}
	})

	t.Run("log", func(t *testing.T) {
		record := readExample(t, "checkpoint.example.json")
		session := record["session_refs"].([]any)[0].(map[string]any)
		log := session["logs"].([]any)[0].(map[string]any)
		delete(log, "selector")
		refreshCheckpointDigests(t, record)
		if err := Validate(Checkpoint, record); err != nil {
			t.Fatalf("validate absolute log evidence selector: %v", err)
		}
	})
}

func captureWithSelectors(t *testing.T, source, evidence map[string]any) map[string]any {
	t.Helper()
	record := readExample(t, "capture-input.example.json")
	context := record["context"].(map[string]any)
	resource := context["relevant_resources"].([]any)[0].(map[string]any)
	resource["selection"] = source
	decision := context["decisions"].([]any)[0].(map[string]any)
	decision["evidence_refs"] = []any{map[string]any{
		"ref_id":   resource["resource_id"],
		"selector": evidence,
	}}
	return record
}

func refreshCheckpointDigests(t *testing.T, record map[string]any) {
	t.Helper()
	contextDigest, err := canonical.Digest(record["context"])
	if err != nil {
		t.Fatalf("context digest: %v", err)
	}
	record["context_digest"] = contextDigest
	contentDigest, err := canonical.ContentDigest(record)
	if err != nil {
		t.Fatalf("content digest: %v", err)
	}
	record["content_digest"] = contentDigest
}

func TestEmbeddedSchemasMatchSpecification(t *testing.T) {
	for _, file := range []string{
		"common.schema.json",
		"capture-input.schema.json",
		"checkpoint.schema.json",
		"runtime-snapshot.schema.json",
		"handoff.schema.json",
	} {
		source, err := os.ReadFile(filepath.Join("..", "..", "..", "schemas", "v1", file))
		if err != nil {
			t.Fatalf("read source %s: %v", file, err)
		}
		embedded, err := os.ReadFile(filepath.Join("assets", file))
		if err != nil {
			t.Fatalf("read embedded %s: %v", file, err)
		}
		if string(embedded) != string(source) {
			t.Fatalf("embedded %s differs from schemas/v1", file)
		}
	}
}

func readExample(t *testing.T, name string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "schemas", "v1", "examples", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	record, err := Decode(data)
	if err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return record
}
