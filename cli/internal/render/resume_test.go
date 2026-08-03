package render

import (
	"strings"
	"testing"
)

func TestResumeContainsSelfContainedContextAndGitDifference(t *testing.T) {
	checkpoint := map[string]any{
		"task_id":       "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		"checkpoint_id": "01ARZ3NDEKTSV4RRFFQ69G5FAW",
		"context": map[string]any{
			"title":              "Implement ctx",
			"summary":            "Build the local CLI.",
			"objective":          map[string]any{"goal": "Resume without a transcript", "success_criteria": []any{"Render a complete summary"}},
			"constraints":        []any{map[string]any{"text": "Keep Git read-only"}},
			"assumptions":        []any{map[string]any{"text": "The store is local"}},
			"findings":           []any{map[string]any{"text": "A checkpoint is self-contained"}},
			"decisions":          []any{map[string]any{"statement": "Use JSON files", "status": "proposed", "rationale": "The format remains portable"}},
			"progress":           map[string]any{"current": []any{map[string]any{"summary": "Wire commands"}}, "completed": []any{}},
			"next_actions":       []any{map[string]any{"description": "Run tests", "done_when": "all tests pass"}},
			"blockers":           []any{},
			"open_questions":     []any{},
			"validations":        []any{map[string]any{"summary": "Schema passes", "outcome": "failed", "kind": "test", "command": "go test ./..."}},
			"relevant_resources": []any{map[string]any{"note": "CLI contract", "locator": map[string]any{"path": "README.md"}}},
		},
	}
	got, err := Resume(checkpoint, []string{"HEAD differs from the checkpoint baseline."}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Implement ctx", "[proposed] Use JSON files", "Rationale: The format remains portable", "Run tests", "HEAD differs", "[failed] Schema passes", "Kind: test", "Command: go test ./...", "CLI contract"} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("resume output does not include %q:\n%s", want, got)
		}
	}
	ordered := []string{"## Objective", "## Summary", "## Decisions", "## Progress", "## Next actions", "## Validations", "## Relevant resources", "## Git comparison"}
	previous := -1
	for _, heading := range ordered {
		position := strings.Index(string(got), heading)
		if position <= previous {
			t.Fatalf("section %q is out of semantic order:\n%s", heading, got)
		}
		previous = position
	}
}

func TestResumePrioritizesObjectiveActionsAndGitWithinOutputLimit(t *testing.T) {
	checkpoint := map[string]any{
		"task_id": "task", "checkpoint_id": "checkpoint",
		"context": map[string]any{
			"title": "title", "summary": strings.Repeat("x", 100),
			"objective":    map[string]any{"goal": "Keep the goal"},
			"next_actions": []any{map[string]any{"description": "Keep the next action"}},
			"constraints":  []any{map[string]any{"text": strings.Repeat("ignored", 80)}},
		},
	}
	got, err := Resume(checkpoint, []string{"Keep the Git difference"}, 350)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 350 {
		t.Fatalf("limited output = %q", got)
	}
	for _, want := range []string{"Keep the goal", "Keep the next action", "Keep the Git difference"} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("limited output lost %q:\n%s", want, got)
		}
	}
}

func TestResumeReservesCriticalSectionsWhenSuccessCriteriaIsHuge(t *testing.T) {
	checkpoint := map[string]any{
		"task_id": "task", "checkpoint_id": "checkpoint", "stability": "stable",
		"capture": map[string]any{"completeness": "complete"},
		"context": map[string]any{
			"title":   "Large objective",
			"summary": "Keep working from the checkpoint.",
			"objective": map[string]any{
				"goal":             "Preserve the objective core",
				"success_criteria": []any{strings.Repeat("very large criterion ", 3000)},
			},
			"next_actions": []any{map[string]any{"description": "Run the critical next action"}},
		},
	}
	got, err := Resume(checkpoint, []string{"HEAD or branch differs from the checkpoint baseline."}, 32*1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 32*1024 {
		t.Fatalf("resume output is %d bytes", len(got))
	}
	for _, want := range []string{"Preserve the objective core", "## Next actions", "Run the critical next action", "## Git comparison", "HEAD or branch differs"} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("budgeted resume lost %q:\n%s", want, got)
		}
	}
}

func TestResumeRendersBlockerDescriptionImpactAndUnblockCondition(t *testing.T) {
	checkpoint := map[string]any{
		"task_id": "task", "checkpoint_id": "checkpoint",
		"context": map[string]any{
			"title": "Blocked", "summary": "Waiting on a dependency.",
			"objective":    map[string]any{"goal": "Finish integration"},
			"next_actions": []any{},
			"blockers": []any{map[string]any{
				"description":       "Dependency endpoint is unavailable",
				"impact":            "Integration tests cannot run",
				"unblock_condition": "The endpoint responds successfully",
			}},
		},
	}
	got, err := Resume(checkpoint, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Dependency endpoint is unavailable", "Impact: Integration tests cannot run", "Unblock when: The endpoint responds successfully"} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("blocker output misses %q:\n%s", want, got)
		}
	}
}

func TestResumeKeepsBlockedTaskBlockerWithinDefaultBudget(t *testing.T) {
	checkpoint := map[string]any{
		"task_id": "task", "checkpoint_id": "checkpoint", "work_status": "blocked",
		"context": map[string]any{
			"title":   "Blocked task",
			"summary": strings.Repeat("large summary ", 5000),
			"objective": map[string]any{
				"goal": "Restore the dependency",
			},
			"findings": []any{map[string]any{"text": strings.Repeat("large finding ", 5000)}},
			"next_actions": []any{map[string]any{
				"description": "Retry after credentials arrive",
			}},
			"blockers": []any{map[string]any{
				"description":       "Production credentials are unavailable",
				"impact":            "Deployment validation cannot run",
				"unblock_condition": "Credentials are issued",
			}},
		},
	}
	got, err := Resume(checkpoint, []string{"Working tree differs from the checkpoint."}, 32*1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 32*1024 {
		t.Fatalf("resume output is %d bytes", len(got))
	}
	for _, want := range []string{"Retry after credentials arrive", "Production credentials are unavailable", "Deployment validation cannot run", "Credentials are issued", "Working tree differs from the checkpoint."} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("budgeted blocked resume lost %q:\n%s", want, got)
		}
	}
}
