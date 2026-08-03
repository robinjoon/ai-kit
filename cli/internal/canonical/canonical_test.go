package canonical

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpointExampleDigests(t *testing.T) {
	record := readObject(t, "checkpoint.example.json")

	contextDigest, err := Digest(record["context"])
	if err != nil {
		t.Fatalf("context digest: %v", err)
	}
	if got, want := contextDigest, record["context_digest"]; got != want {
		t.Fatalf("context digest = %q, want %q", got, want)
	}

	contentDigest, err := ContentDigest(record)
	if err != nil {
		t.Fatalf("content digest: %v", err)
	}
	if got, want := contentDigest, record["content_digest"]; got != want {
		t.Fatalf("content digest = %q, want %q", got, want)
	}
}

func TestRuntimeExampleContentDigest(t *testing.T) {
	record := readObject(t, "runtime-snapshot.example.json")
	got, err := ContentDigest(record)
	if err != nil {
		t.Fatalf("content digest: %v", err)
	}
	if want := record["content_digest"]; got != want {
		t.Fatalf("content digest = %q, want %q", got, want)
	}
}

func TestHandoffExampleBodyDigest(t *testing.T) {
	handoff := readObject(t, "handoff.example.json")
	body := HandoffBody(handoff["checkpoint_id"].(string), handoff["task_id"].(string))
	if got, want := BytesDigest(body), handoff["rendered_body_digest"]; got != want {
		t.Fatalf("handoff body digest = %q, want %q", got, want)
	}
}

func TestJSONUsesRFC8785ObjectOrder(t *testing.T) {
	got, err := JSON(map[string]any{"z": "<tag>", "a": 1})
	if err != nil {
		t.Fatalf("canonical JSON: %v", err)
	}
	if want := `{"a":1,"z":"<tag>"}`; string(got) != want {
		t.Fatalf("canonical JSON = %s, want %s", got, want)
	}
}

func TestDecodeObjectRejectsDuplicateKeys(t *testing.T) {
	for _, test := range []struct {
		name string
		json string
	}{
		{"top level", `{"task_id":"first","task_id":"second"}`},
		{"nested", `{"context":{"title":"first","title":"second"}}`},
		{"extensions", `{"extensions":{"com.example.provider":{"value":1,"value":2}}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeObject([]byte(test.json)); err == nil {
				t.Fatal("expected duplicate JSON key to fail decoding")
			}
		})
	}
}

func TestDecodeObjectPreservesNumbersAndRejectsTrailingData(t *testing.T) {
	record, err := DecodeObject([]byte(`{"schema_version":1}`))
	if err != nil {
		t.Fatalf("decode object: %v", err)
	}
	if _, ok := record["schema_version"].(json.Number); !ok {
		t.Fatalf("number type = %T, want json.Number", record["schema_version"])
	}
	if _, err := DecodeObject([]byte(`{} {}`)); err == nil {
		t.Fatal("expected trailing JSON object to fail decoding")
	}
}

func readObject(t *testing.T, name string) map[string]any {
	t.Helper()
	path := filepath.Join("..", "..", "..", "schemas", "v1", "examples", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	record, err := DecodeObject(data)
	if err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return record
}
