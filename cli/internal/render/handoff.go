// Package render turns derived ctx records into their stable human-readable form.
package render

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/robinjoon/ai-kit/cli/internal/canonical"
)

// HandoffBody returns the fixed body defined by ctx-handoff-markdown-v1.
func HandoffBody(taskID, checkpointID string) string {
	return fmt.Sprintf("# ctx handoff\n\nLoad checkpoint `%s` for task `%s` through `ctx resume`.\n", checkpointID, taskID)
}

// Digest returns a schema-compatible sha256 digest for data.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// RenderHandoff serializes the supplied handoff record as handoff.md. It is
// deliberately narrow: callers must validate the record against the schema
// and referenced checkpoint before writing it.
func RenderHandoff(record map[string]any) ([]byte, error) {
	taskID, err := requiredString(record, "task_id")
	if err != nil {
		return nil, err
	}
	checkpointID, err := requiredString(record, "checkpoint_id")
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	b.WriteString("---\n")
	for _, key := range []string{
		"schema_version", "record_type", "handoff_id", "task_id", "checkpoint_id",
		"checkpoint_digest", "generated_at",
	} {
		if err := writeScalar(&b, key, record[key]); err != nil {
			return nil, err
		}
	}
	if err := writeProducer(&b, record["producer"]); err != nil {
		return nil, err
	}
	if target, ok := record["target"]; ok {
		if err := writeTarget(&b, target); err != nil {
			return nil, err
		}
	}
	for _, key := range []string{"render_profile", "rendered_body_digest", "extensions"} {
		if err := writeScalar(&b, key, record[key]); err != nil {
			return nil, err
		}
	}
	b.WriteString("---\n\n")
	b.WriteString(HandoffBody(taskID, checkpointID))
	return []byte(b.String()), nil
}

func writeProducer(b *strings.Builder, value any) error {
	producer, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("producer must be an object")
	}
	b.WriteString("producer:\n")
	for _, key := range []string{"actor_type", "system", "version", "adapter", "device_id", "extensions"} {
		value, exists := producer[key]
		if !exists {
			continue
		}
		if err := writeIndentedScalar(b, key, value); err != nil {
			return fmt.Errorf("producer.%s: %w", key, err)
		}
	}
	return nil
}

func writeTarget(b *strings.Builder, value any) error {
	target, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("target must be an object")
	}
	b.WriteString("target:\n")
	for _, key := range []string{"system", "interface", "device_id"} {
		value, exists := target[key]
		if !exists {
			continue
		}
		if err := writeIndentedScalar(b, key, value); err != nil {
			return fmt.Errorf("target.%s: %w", key, err)
		}
	}
	return nil
}

func writeScalar(b *strings.Builder, key string, value any) error {
	encoded, err := yamlJSONValue(value)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	fmt.Fprintf(b, "%s: %s\n", key, encoded)
	return nil
}

func writeIndentedScalar(b *strings.Builder, key string, value any) error {
	encoded, err := yamlJSONValue(value)
	if err != nil {
		return err
	}
	fmt.Fprintf(b, "  %s: %s\n", key, encoded)
	return nil
}

func yamlJSONValue(value any) (string, error) {
	if value == nil {
		return "", fmt.Errorf("is required")
	}
	var encoded []byte
	var err error
	if _, object := value.(map[string]any); object {
		encoded, err = canonical.JSON(value)
	} else {
		encoded, err = json.Marshal(value)
	}
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func requiredString(record map[string]any, key string) (string, error) {
	value, ok := record[key].(string)
	if !ok || value == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}
