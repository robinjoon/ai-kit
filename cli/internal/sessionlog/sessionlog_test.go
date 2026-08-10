package sessionlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCollectorCollectsClaudeAndCodexEvents(t *testing.T) {
	root := t.TempDir()
	claudeRoot := filepath.Join(root, "claude")
	codexRoot := filepath.Join(root, "codex")
	copyFixture(t, "testdata/claude.jsonl", filepath.Join(claudeRoot, "project", "session.jsonl"))
	copyFixture(t, "testdata/codex.jsonl", filepath.Join(codexRoot, "2026", "08", "10", "rollout.jsonl"))

	since := mustTime(t, "2026-08-10T01:00:00Z")
	events, err := (Collector{ClaudeRoot: claudeRoot, CodexRoot: codexRoot}).Collect("/repo/project", since)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 15 {
		t.Fatalf("event count = %d, want 15\n%#v", len(events), events)
	}
	if events[0].Client != ClientClaudeCode || events[0].Kind != UserMessage || events[0].Content != "implement log collection" {
		t.Fatalf("first event = %#v", events[0])
	}
	assertEvent(t, events, ClientCodex, AssistantReasoning, "inspect the session metadata")
	assertEvent(t, events, ClientClaudeCode, AssistantReasoning, "compare the two log formats")
	assertEvent(t, events, ClientClaudeCode, FileChange, `"name":"Edit"`)
	assertEvent(t, events, ClientCodex, FileChange, `"main.go"`)
	assertEvent(t, events, ClientClaudeCode, ToolResult, `"name":"Bash"`)
	assertEvent(t, events, ClientCodex, ToolResult, `"name":"exec"`)
	assertEvent(t, events, ClientCodex, ToolCall, `"name":"read_file"`)
	assertEvent(t, events, ClientCodex, ToolResult, `"name":"read_file"`)
	for _, event := range events {
		if strings.Contains(event.Content, "old request") || strings.Contains(event.Content, "unrelated request") || strings.Contains(event.Content, "opaque-only") || strings.Contains(event.Content, "duplicate assistant event") {
			t.Fatalf("unexpected event = %#v", event)
		}
	}
}

func TestCollectorReturnsNoEventsForAnotherWorktree(t *testing.T) {
	root := t.TempDir()
	claudeRoot := filepath.Join(root, "claude")
	codexRoot := filepath.Join(root, "codex")
	copyFixture(t, "testdata/claude.jsonl", filepath.Join(claudeRoot, "session.jsonl"))
	copyFixture(t, "testdata/codex.jsonl", filepath.Join(codexRoot, "rollout.jsonl"))

	events, err := (Collector{ClaudeRoot: claudeRoot, CodexRoot: codexRoot}).Collect("/repo/unrelated", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %#v, want none", events)
	}
}

func TestCollectorAllowsMissingLogRoots(t *testing.T) {
	events, err := (Collector{ClaudeRoot: filepath.Join(t.TempDir(), "missing")}).Collect("/repo/project", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %#v, want none", events)
	}
}

func assertEvent(t *testing.T, events []Event, client string, kind Kind, content string) {
	t.Helper()
	for _, event := range events {
		if event.Client == client && event.Kind == kind && strings.Contains(event.Content, content) {
			return
		}
	}
	t.Fatalf("missing %s %s event containing %q\n%#v", client, kind, content, events)
}

func copyFixture(t *testing.T, source, destination string) {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
