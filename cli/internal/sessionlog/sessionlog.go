package sessionlog

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	ClientClaudeCode = "com.anthropic.claude-code"
	ClientCodex      = "com.openai.codex"
)

type Kind string

const (
	UserMessage        Kind = "user_message"
	AssistantMessage   Kind = "assistant_message"
	AssistantReasoning Kind = "assistant_reasoning"
	ToolCall           Kind = "tool_call"
	ToolResult         Kind = "tool_result"
	FileChange         Kind = "file_change"
)

type Event struct {
	Timestamp time.Time `json:"timestamp"`
	Client    string    `json:"client"`
	Kind      Kind      `json:"kind"`
	Content   string    `json:"content"`
}

type Collector struct {
	ClaudeRoot string
	CodexRoot  string
}

func NewDefault() (Collector, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Collector{}, fmt.Errorf("resolve user home directory: %w", err)
	}
	return Collector{
		ClaudeRoot: filepath.Join(home, ".claude", "projects"),
		CodexRoot:  filepath.Join(home, ".codex", "sessions"),
	}, nil
}

func (c Collector) Collect(worktree string, since time.Time) ([]Event, error) {
	worktree = filepath.Clean(worktree)
	var events []Event
	for _, source := range []struct {
		name  string
		root  string
		parse func(string, string, time.Time) ([]Event, error)
	}{
		{name: "Claude Code", root: c.ClaudeRoot, parse: parseClaudeFile},
		{name: "Codex", root: c.CodexRoot, parse: parseCodexFile},
	} {
		collected, err := collectRoot(source.root, worktree, since, source.parse)
		if err != nil {
			return nil, fmt.Errorf("collect %s logs: %w", source.name, err)
		}
		events = append(events, collected...)
	}
	sort.Slice(events, func(i, j int) bool {
		if !events[i].Timestamp.Equal(events[j].Timestamp) {
			return events[i].Timestamp.Before(events[j].Timestamp)
		}
		if events[i].Client != events[j].Client {
			return events[i].Client < events[j].Client
		}
		if events[i].Kind != events[j].Kind {
			return events[i].Kind < events[j].Kind
		}
		return events[i].Content < events[j].Content
	})
	return events, nil
}

func collectRoot(root, worktree string, since time.Time, parse func(string, string, time.Time) ([]Event, error)) ([]Event, error) {
	if root == "" {
		return nil, nil
	}
	if _, err := os.Stat(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var events []Event
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !since.IsZero() && !info.ModTime().After(since) {
			return nil
		}
		parsed, err := parse(path, worktree, since)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		events = append(events, parsed...)
		return nil
	})
	return events, err
}

type claudeRecord struct {
	Type      string          `json:"type"`
	CWD       string          `json:"cwd"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

type claudeMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	ToolUseID string          `json:"tool_use_id"`
	Input     json.RawMessage `json:"input"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

func parseClaudeFile(path, worktree string, since time.Time) ([]Event, error) {
	lines, err := readLines(path)
	if err != nil {
		return nil, err
	}
	toolNames := make(map[string]string)
	var events []Event
	for _, line := range lines {
		var record claudeRecord
		if json.Unmarshal(line, &record) != nil || !insideWorktree(worktree, record.CWD) {
			continue
		}
		timestamp, ok := after(record.Timestamp, since)
		if !ok || record.Type != "user" && record.Type != "assistant" {
			continue
		}
		var message claudeMessage
		if json.Unmarshal(record.Message, &message) != nil {
			continue
		}
		var plain string
		if json.Unmarshal(message.Content, &plain) == nil {
			kind := UserMessage
			if message.Role == "assistant" {
				kind = AssistantMessage
			}
			events = appendText(events, timestamp, ClientClaudeCode, kind, plain)
			continue
		}
		var blocks []claudeBlock
		if json.Unmarshal(message.Content, &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			switch block.Type {
			case "text":
				kind := UserMessage
				if message.Role == "assistant" {
					kind = AssistantMessage
				}
				events = appendText(events, timestamp, ClientClaudeCode, kind, block.Text)
			case "thinking":
				events = appendText(events, timestamp, ClientClaudeCode, AssistantReasoning, block.Thinking)
			case "tool_use":
				toolNames[block.ID] = block.Name
				kind := ToolCall
				if isFileTool(block.Name) {
					kind = FileChange
				}
				events = append(events, Event{Timestamp: timestamp, Client: ClientClaudeCode, Kind: kind, Content: encodeTool(block.Name, "input", block.Input, false)})
			case "tool_result":
				events = append(events, Event{Timestamp: timestamp, Client: ClientClaudeCode, Kind: ToolResult, Content: encodeTool(toolNames[block.ToolUseID], "output", block.Content, block.IsError)})
			}
		}
	}
	return events, nil
}

type codexEnvelope struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexPayload struct {
	Type      string          `json:"type"`
	CWD       string          `json:"cwd"`
	Role      string          `json:"role"`
	Name      string          `json:"name"`
	CallID    string          `json:"call_id"`
	Content   []codexText     `json:"content"`
	Summary   []codexText     `json:"summary"`
	Input     json.RawMessage `json:"input"`
	Arguments json.RawMessage `json:"arguments"`
	Output    json.RawMessage `json:"output"`
	Changes   json.RawMessage `json:"changes"`
}

type codexText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func parseCodexFile(path, worktree string, since time.Time) ([]Event, error) {
	lines, err := readLines(path)
	if err != nil {
		return nil, err
	}
	matched := false
	for _, line := range lines {
		var envelope codexEnvelope
		var payload codexPayload
		if json.Unmarshal(line, &envelope) == nil && envelope.Type == "session_meta" && json.Unmarshal(envelope.Payload, &payload) == nil && insideWorktree(worktree, payload.CWD) {
			matched = true
			break
		}
	}
	if !matched {
		return nil, nil
	}
	toolNames := make(map[string]string)
	var events []Event
	for _, line := range lines {
		var envelope codexEnvelope
		if json.Unmarshal(line, &envelope) != nil {
			continue
		}
		timestamp, ok := after(envelope.Timestamp, since)
		if !ok {
			continue
		}
		var payload codexPayload
		if json.Unmarshal(envelope.Payload, &payload) != nil {
			continue
		}
		switch envelope.Type {
		case "response_item":
			switch payload.Type {
			case "message":
				kind := UserMessage
				if payload.Role == "assistant" {
					kind = AssistantMessage
				} else if payload.Role != "user" {
					continue
				}
				events = appendText(events, timestamp, ClientCodex, kind, joinText(payload.Content))
			case "reasoning":
				events = appendText(events, timestamp, ClientCodex, AssistantReasoning, joinText(payload.Summary))
			case "custom_tool_call", "function_call":
				toolNames[payload.CallID] = payload.Name
				input := payload.Input
				if payload.Type == "function_call" {
					input = payload.Arguments
				}
				events = append(events, Event{Timestamp: timestamp, Client: ClientCodex, Kind: ToolCall, Content: encodeTool(payload.Name, "input", input, false)})
			case "custom_tool_call_output", "function_call_output":
				events = append(events, Event{Timestamp: timestamp, Client: ClientCodex, Kind: ToolResult, Content: encodeTool(toolNames[payload.CallID], "output", payload.Output, false)})
			}
		case "event_msg":
			if payload.Type == "patch_apply_end" && len(payload.Changes) > 0 {
				events = append(events, Event{Timestamp: timestamp, Client: ClientCodex, Kind: FileChange, Content: string(payload.Changes)})
			}
		}
	}
	return events, nil
}

func readLines(path string) ([][]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var lines [][]byte
	for scanner.Scan() {
		lines = append(lines, append([]byte(nil), scanner.Bytes()...))
	}
	return lines, scanner.Err()
}

func after(value string, since time.Time) (time.Time, bool) {
	timestamp, err := time.Parse(time.RFC3339Nano, value)
	return timestamp, err == nil && timestamp.After(since)
}

func insideWorktree(worktree, cwd string) bool {
	if cwd == "" {
		return false
	}
	relative, err := filepath.Rel(filepath.Clean(worktree), filepath.Clean(cwd))
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func appendText(events []Event, timestamp time.Time, client string, kind Kind, content string) []Event {
	content = strings.TrimSpace(content)
	if content == "" {
		return events
	}
	return append(events, Event{Timestamp: timestamp, Client: client, Kind: kind, Content: content})
}

func joinText(parts []codexText) string {
	var text []string
	for _, part := range parts {
		if value := strings.TrimSpace(part.Text); value != "" {
			text = append(text, value)
		}
	}
	return strings.Join(text, "\n")
}

func encodeTool(name, field string, value json.RawMessage, failed bool) string {
	payload := struct {
		Name   string          `json:"name,omitempty"`
		Input  json.RawMessage `json:"input,omitempty"`
		Output json.RawMessage `json:"output,omitempty"`
		Error  bool            `json:"error,omitempty"`
	}{Name: name, Error: failed}
	if field == "input" {
		payload.Input = value
	} else {
		payload.Output = value
	}
	encoded, _ := json.Marshal(payload)
	return string(encoded)
}

func isFileTool(name string) bool {
	switch strings.ToLower(name) {
	case "edit", "write", "multiedit", "notebookedit", "apply_patch":
		return true
	default:
		return false
	}
}
