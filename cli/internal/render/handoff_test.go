package render

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderHandoffMatchesExample(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	jsonBytes, err := os.ReadFile(filepath.Join(repoRoot, "schemas", "v1", "examples", "handoff.example.json"))
	if err != nil {
		t.Fatalf("read example json: %v", err)
	}
	var record map[string]any
	if err := json.Unmarshal(jsonBytes, &record); err != nil {
		t.Fatalf("decode example json: %v", err)
	}

	got, err := RenderHandoff(record)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want, err := os.ReadFile(filepath.Join(repoRoot, "schemas", "v1", "examples", "handoff.example.md"))
	if err != nil {
		t.Fatalf("read example markdown: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("rendered markdown differs\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if gotDigest := Digest([]byte(HandoffBody(record["task_id"].(string), record["checkpoint_id"].(string)))); gotDigest != record["rendered_body_digest"] {
		t.Fatalf("body digest = %s, want %s", gotDigest, record["rendered_body_digest"])
	}
}

func TestRenderHandoffCanonicalizesExtensions(t *testing.T) {
	record := map[string]any{
		"schema_version": 1, "record_type": "ctx.handoff", "handoff_id": "01ARZ3NDEKTSV4RRFFQ69G5FAY",
		"task_id": "01ARZ3NDEKTSV4RRFFQ69G5FAV", "checkpoint_id": "01ARZ3NDEKTSV4RRFFQ69G5FAW",
		"checkpoint_digest": "sha256:05b4e564c6dd58c0bf2c44284e3d28cac303d6a4678b08b9d35b24c5aa27c285",
		"generated_at":      "2026-08-03T06:06:00Z",
		"producer":          map[string]any{"actor_type": "cli", "system": "ctx.cli", "extensions": map[string]any{}},
		"render_profile":    "ctx-handoff-markdown-v1", "rendered_body_digest": "sha256:eb53a863c21b0edc4392a2090bf16a2990c22d0ada44155813f1de484fdfd528",
		"extensions": map[string]any{"com.example": map[string]any{"z": 1, "a": 2}},
	}
	got, err := RenderHandoff(record)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `extensions: {"com.example":{"a":2,"z":1}}`) {
		t.Fatalf("extensions are not canonical JSON:\n%s", got)
	}
}

func TestRenderHandoffRejectsMissingIdentity(t *testing.T) {
	_, err := RenderHandoff(map[string]any{})
	if err == nil {
		t.Fatal("RenderHandoff accepted missing identity")
	}
}
