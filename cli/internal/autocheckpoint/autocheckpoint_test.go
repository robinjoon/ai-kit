package autocheckpoint

import (
	"errors"
	"os/exec"
	"reflect"
	"testing"
	"time"

	"github.com/robinjoon/ai-kit/cli/internal/core"
	"github.com/robinjoon/ai-kit/cli/internal/sessionlog"
)

func TestCaptureReconstructsAndSavesACompleteCheckpoint(t *testing.T) {
	repository := testRepository(t)
	contexts, err := core.New(t.TempDir(), "ctx.auto-test")
	if err != nil {
		t.Fatal(err)
	}
	started, err := contexts.Start(repository, "Build automatic checkpoints")
	if err != nil {
		t.Fatal(err)
	}
	event := sessionlog.Event{
		Timestamp: started.Checkpoint.CreatedAt.Add(time.Second),
		Client:    sessionlog.ClientCodex,
		Kind:      sessionlog.AssistantMessage,
		Content:   "The log collector is implemented.",
	}
	collector := &recordingCollector{events: []sessionlog.Event{event}}
	reconstructor := &recordingReconstructor{result: core.CheckpointInput{
		Goal:        "Build automatic checkpoints",
		Summary:     "The log collector is implemented.",
		Decisions:   []string{"Use one common event format"},
		NextActions: []string{"Connect a model"},
		Blockers:    []string{"The model contract is not defined"},
	}}

	saved, err := New(contexts, collector, reconstructor).Capture(repository)
	if err != nil {
		t.Fatal(err)
	}
	if collector.worktree != started.Scope.WorktreeRoot || !collector.since.Equal(started.Checkpoint.CreatedAt) {
		t.Fatalf("collector input = %q, %s", collector.worktree, collector.since)
	}
	if len(reconstructor.evidence.Events) != 1 || reconstructor.evidence.Events[0] != event {
		t.Fatalf("evidence events = %#v", reconstructor.evidence.Events)
	}
	if reconstructor.evidence.Previous.Goal != "Build automatic checkpoints" {
		t.Fatalf("previous context = %#v", reconstructor.evidence.Previous)
	}
	if saved.Reason != "auto" || !reflect.DeepEqual(saved.Context, reconstructor.result) {
		t.Fatalf("saved checkpoint = %#v", saved)
	}
	state, err := contexts.Resume(repository)
	if err != nil {
		t.Fatal(err)
	}
	if state.Latest.ID != saved.ID || state.Latest.Context.NextActions[0] != "Connect a model" {
		t.Fatalf("latest checkpoint = %#v", state.Latest)
	}
}

func TestCaptureSkipsWhenThereAreNoNewEvents(t *testing.T) {
	repository := testRepository(t)
	contexts, err := core.New(t.TempDir(), "ctx.auto-test")
	if err != nil {
		t.Fatal(err)
	}
	started, err := contexts.Start(repository, "No new work")
	if err != nil {
		t.Fatal(err)
	}

	_, err = New(contexts, &recordingCollector{}, &recordingReconstructor{}).Capture(repository)
	if !errors.Is(err, ErrNoNewEvents) {
		t.Fatalf("error = %v, want ErrNoNewEvents", err)
	}
	state, err := contexts.Resume(repository)
	if err != nil {
		t.Fatal(err)
	}
	if state.Latest.ID != started.Checkpoint.ID {
		t.Fatalf("latest checkpoint changed: %s", state.Latest.ID)
	}
}

type recordingCollector struct {
	worktree string
	since    time.Time
	events   []sessionlog.Event
}

func (c *recordingCollector) Collect(worktree string, since time.Time) ([]sessionlog.Event, error) {
	c.worktree = worktree
	c.since = since
	return c.events, nil
}

type recordingReconstructor struct {
	evidence Evidence
	result   core.CheckpointInput
}

func (r *recordingReconstructor) Reconstruct(evidence Evidence) (core.CheckpointInput, error) {
	r.evidence = evidence
	return r.result, nil
}

func testRepository(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	command := exec.Command("git", "-C", repository, "init", "-q")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	return repository
}
