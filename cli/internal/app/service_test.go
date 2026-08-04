package app

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/robinjoon/ai-kit/cli/internal/canonical"
	"github.com/robinjoon/ai-kit/cli/internal/gitobs"
	"github.com/robinjoon/ai-kit/cli/internal/model"
	"github.com/robinjoon/ai-kit/cli/internal/schema"
	"go.yaml.in/yaml/v3"
)

func TestServiceCreatesCheckpointHandoffAndResumes(t *testing.T) {
	store := testStore{root: t.TempDir()}
	service := New(store, Config{Client: "ctx.cli", DeviceID: "test-device", Version: "test"})
	service.now = func() time.Time { return time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC) }
	ids := []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAV",
		"01ARZ3NDEKTSV4RRFFQ69G5FAW",
		"01ARZ3NDEKTSV4RRFFQ69G5FAX",
		"01ARZ3NDEKTSV4RRFFQ69G5FAY",
		"01ARZ3NDEKTSV4RRFFQ69G5FAZ",
		"01ARZ3NDEKTSV4RRFFQ69G5FB0",
	}
	service.nextID = func(time.Time) (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	}
	service.observe = func(string) (gitobs.Observation, error) { return cleanObservation(), nil }

	task, err := service.CreateTask(context.Background(), t.TempDir(), "Implement ctx", []string{"ctx-cli"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if task.TaskID == "" {
		t.Fatal("task ID is empty")
	}
	input := captureInput(t, "repo-test")
	checkpoint, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: input})
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if checkpoint.Stability != "stable" || checkpoint.Deduplicated {
		t.Fatalf("checkpoint result = %#v", checkpoint)
	}
	handoff, err := service.Handoff(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: input}, Record{"system": "com.openai.codex", "interface": "desktop"})
	if err != nil {
		t.Fatalf("handoff: %v", err)
	}
	if handoff.CheckpointID == checkpoint.CheckpointID || handoff.HandoffID == "" {
		t.Fatalf("handoff = %#v", handoff)
	}
	if _, err := os.Stat(handoff.Path); err != nil {
		t.Fatalf("handoff file: %v", err)
	}
	firstSnapshot, err := service.Snapshot(context.Background(), t.TempDir(), "manual", "")
	if err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	secondSnapshot, err := service.Snapshot(context.Background(), t.TempDir(), "manual", "")
	if err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
	snapshot, err := store.ReadJSON(store.SnapshotPath("repo-test", task.TaskID, secondSnapshot.SnapshotID))
	if err != nil {
		t.Fatalf("read second snapshot: %v", err)
	}
	if snapshot["previous_snapshot_id"] != firstSnapshot.SnapshotID || snapshot["active_checkpoint_id"] != handoff.CheckpointID {
		t.Fatalf("snapshot links = %#v", snapshot)
	}
	resume, err := service.Resume(context.Background(), t.TempDir(), "ctx-cli", "", 0)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resume.CheckpointID != handoff.CheckpointID {
		t.Fatalf("resumed %s, want handoff checkpoint %s", resume.CheckpointID, handoff.CheckpointID)
	}
	if len(resume.Content) == 0 {
		t.Fatal("resume content is empty")
	}
	status, err := service.Status(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.LatestRuntimeSnapshot["snapshot_id"] != secondSnapshot.SnapshotID || len(status.GitDifferences) != 0 {
		t.Fatalf("status recovery fields = %#v", status)
	}
}

func TestSyncPullRebuildsTasksFromCanonicalCheckpoints(t *testing.T) {
	sourceStore := testStore{root: t.TempDir()}
	source := New(sourceStore, Config{Client: "ctx.cli", DeviceID: "test-device"})
	source.now = func() time.Time { return time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC) }
	ids := []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW"}
	source.nextID = func(time.Time) (string, error) { id := ids[0]; ids = ids[1:]; return id, nil }
	source.observe = func(string) (gitobs.Observation, error) { return cleanObservation(), nil }
	task, err := source.CreateTask(context.Background(), t.TempDir(), "Synced task", []string{"sync"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: captureInput(t, "repo-test")}); err != nil {
		t.Fatal(err)
	}
	remote := t.TempDir()
	if err := source.Sync(context.Background(), t.TempDir(), remote, "push"); err != nil {
		t.Fatal(err)
	}
	sourceTasks, err := source.ListTasks(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(sourceTasks) != 1 || sourceTasks[0].Title != "Synced task" || !sameAliases(sourceTasks[0].Aliases, []string{"sync"}) {
		t.Fatalf("source identity changed after sync: %#v", sourceTasks)
	}

	destinationStore := testStore{root: t.TempDir()}
	destination := New(destinationStore, Config{Client: "ctx.cli", DeviceID: "other-device"})
	destination.now = source.now
	destination.observe = source.observe
	if err := destination.Sync(context.Background(), t.TempDir(), remote, "pull"); err != nil {
		t.Fatal(err)
	}
	tasks, err := destination.ListTasks(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].TaskID != task.TaskID || tasks[0].Title != "Synced task" || !sameAliases(tasks[0].Aliases, []string{"sync"}) {
		t.Fatalf("rebuilt tasks = %#v", tasks)
	}
	if _, err := destination.Resume(context.Background(), t.TempDir(), task.TaskID, "", 0); err != nil {
		t.Fatalf("resume synced task explicitly: %v", err)
	}
}

func TestHandoffTargetChangeIsNotDeduplicated(t *testing.T) {
	sourceStore := testStore{root: t.TempDir()}
	source := newFakeService(sourceStore, []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW",
		"01ARZ3NDEKTSV4RRFFQ69G5FAX", "01ARZ3NDEKTSV4RRFFQ69G5FAY",
		"01ARZ3NDEKTSV4RRFFQ69G5FAZ", "01ARZ3NDEKTSV4RRFFQ69G5FB0",
		"01ARZ3NDEKTSV4RRFFQ69G5FB1", "01ARZ3NDEKTSV4RRFFQ69G5FB2",
	})
	task, err := source.CreateTask(context.Background(), t.TempDir(), "Targeted handoff", nil)
	if err != nil {
		t.Fatal(err)
	}
	base, err := source.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), TaskID: task.TaskID, Input: captureInput(t, "repo-test")})
	if err != nil {
		t.Fatal(err)
	}
	firstTarget := Record{"system": "com.openai.codex", "interface": "desktop"}
	firstHandoff, err := source.Handoff(context.Background(), CheckpointRequest{CWD: t.TempDir(), TaskID: task.TaskID, Parents: []string{base.CheckpointID}, Input: captureInput(t, "repo-test")}, firstTarget)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := source.Handoff(context.Background(), CheckpointRequest{CWD: t.TempDir(), TaskID: task.TaskID, Parents: []string{base.CheckpointID}, Input: captureInput(t, "repo-test")}, firstTarget)
	if err != nil {
		t.Fatal(err)
	}
	if !retry.Deduplicated || retry.CheckpointID != firstHandoff.CheckpointID {
		t.Fatalf("same-target retry = %#v, want checkpoint %s deduplicated", retry, firstHandoff.CheckpointID)
	}
	target := Record{"system": "com.openai.codex", "interface": "cli"}
	handoff, err := source.Handoff(context.Background(), CheckpointRequest{CWD: t.TempDir(), TaskID: task.TaskID, Parents: []string{firstHandoff.CheckpointID}, Input: captureInput(t, "repo-test")}, target)
	if err != nil {
		t.Fatal(err)
	}
	if handoff.CheckpointID == firstHandoff.CheckpointID {
		t.Fatal("handoff target change was incorrectly deduplicated")
	}
	checkpoint, err := sourceStore.ReadJSON(sourceStore.CheckpointPath("repo-test", task.TaskID, handoff.CheckpointID))
	if err != nil {
		t.Fatal(err)
	}
	if got := handoffTarget(checkpoint); got["system"] != target["system"] || got["interface"] != target["interface"] {
		t.Fatalf("immutable handoff target = %#v, want %#v", got, target)
	}
}

func TestHandoffTargetSurvivesPointerRegenerationAndSync(t *testing.T) {
	sourceStore := testStore{root: t.TempDir()}
	source := newFakeService(sourceStore, []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW",
		"01ARZ3NDEKTSV4RRFFQ69G5FAX", "01ARZ3NDEKTSV4RRFFQ69G5FAY",
		"01ARZ3NDEKTSV4RRFFQ69G5FAZ", "01ARZ3NDEKTSV4RRFFQ69G5FB0",
	})
	task, err := source.CreateTask(context.Background(), t.TempDir(), "Targeted handoff", nil)
	if err != nil {
		t.Fatal(err)
	}
	firstTarget := Record{"system": "com.openai.codex", "interface": "desktop"}
	if _, err := source.Handoff(context.Background(), CheckpointRequest{CWD: t.TempDir(), TaskID: task.TaskID, Input: captureInput(t, "repo-test")}, firstTarget); err != nil {
		t.Fatal(err)
	}
	target := Record{"system": "com.openai.codex", "interface": "cli"}
	_, err = source.Handoff(context.Background(), CheckpointRequest{CWD: t.TempDir(), TaskID: task.TaskID, Input: captureInput(t, "repo-test")}, target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourceStore.HandoffPath("repo-test", task.TaskID), []byte("corrupt"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Resume(context.Background(), t.TempDir(), task.TaskID, "", 0); err != nil {
		t.Fatal(err)
	}
	if got := handoffTargetFromFile(t, sourceStore.HandoffPath("repo-test", task.TaskID)); got["system"] != target["system"] || got["interface"] != target["interface"] {
		t.Fatalf("regenerated handoff target = %#v, want %#v", got, target)
	}

	remote := t.TempDir()
	if err := source.Sync(context.Background(), t.TempDir(), remote, "push"); err != nil {
		t.Fatal(err)
	}
	destinationStore := testStore{root: t.TempDir()}
	destination := New(destinationStore, Config{Client: "new.client", DeviceID: "new-device"})
	destination.observe = source.observe
	destination.now = source.now
	if err := destination.Sync(context.Background(), t.TempDir(), remote, "pull"); err != nil {
		t.Fatal(err)
	}
	if got := handoffTargetFromFile(t, destinationStore.HandoffPath("repo-test", task.TaskID)); got["system"] != target["system"] || got["interface"] != target["interface"] {
		t.Fatalf("synced handoff target = %#v, want %#v", got, target)
	}
}

func TestSyncRejectsMalformedRecognizedHandoffTarget(t *testing.T) {
	sourceStore := testStore{root: t.TempDir()}
	source := newFakeService(sourceStore, []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW",
		"01ARZ3NDEKTSV4RRFFQ69G5FAX", "01ARZ3NDEKTSV4RRFFQ69G5FAY",
	})
	task, err := source.CreateTask(context.Background(), t.TempDir(), "Malformed target", nil)
	if err != nil {
		t.Fatal(err)
	}
	handoff, err := source.Handoff(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: captureInput(t, "repo-test")}, Record{"system": "com.openai.codex", "interface": "desktop"})
	if err != nil {
		t.Fatal(err)
	}
	remote := t.TempDir()
	if err := source.Sync(context.Background(), t.TempDir(), remote, "push"); err != nil {
		t.Fatal(err)
	}
	remoteCheckpoint := filepath.Join(remote, "repos", "repo-test", "tasks", task.TaskID, "checkpoints", handoff.CheckpointID+".json")
	data, err := os.ReadFile(remoteCheckpoint)
	if err != nil {
		t.Fatal(err)
	}
	record, err := schema.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	extensions := record["extensions"].(map[string]any)
	namespace := extensions["io.github.robinjoon.ctx"].(map[string]any)
	namespace["handoff_target"] = map[string]any{"interface": "terminal"}
	digest, err := canonical.ContentDigest(record)
	if err != nil {
		t.Fatal(err)
	}
	record["content_digest"] = digest
	data, err = canonical.JSON(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(remoteCheckpoint, data, 0600); err != nil {
		t.Fatal(err)
	}

	destinationStore := testStore{root: t.TempDir()}
	destination := New(destinationStore, Config{Client: "new.client", DeviceID: "new-device"})
	destination.observe = source.observe
	destination.now = source.now
	if err := destination.Sync(context.Background(), t.TempDir(), remote, "pull"); !errors.Is(err, ErrSync) || !strings.Contains(err.Error(), "handoff_target") {
		t.Fatalf("malformed recognized target sync error = %v", err)
	}
	if _, err := os.Stat(destinationStore.CheckpointPath("repo-test", task.TaskID, handoff.CheckpointID)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("malformed target checkpoint was imported: %v", err)
	}
}

func handoffTargetFromFile(t *testing.T, path string) Record {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(data), "---\n", 3)
	if len(parts) != 3 {
		t.Fatalf("invalid handoff document %q", path)
	}
	var handoff Record
	if err := yaml.Unmarshal([]byte(parts[1]), &handoff); err != nil {
		t.Fatal(err)
	}
	return asRecord(handoff["target"])
}

func TestSyncRejectsConflictingTaskIdentity(t *testing.T) {
	sourceStore := testStore{root: t.TempDir()}
	source := newFakeService(sourceStore, []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW"})
	task, err := source.CreateTask(context.Background(), t.TempDir(), "Canonical title", []string{"canonical"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: captureInput(t, "repo-test")}); err != nil {
		t.Fatal(err)
	}
	remote := t.TempDir()
	if err := source.Sync(context.Background(), t.TempDir(), remote, "push"); err != nil {
		t.Fatal(err)
	}

	destinationStore := testStore{root: t.TempDir()}
	destination := newFakeService(destinationStore, nil)
	conflict := model.TaskManifest{SchemaVersion: model.SchemaVersion, TaskID: task.TaskID, Title: "Different title", Aliases: []string{"canonical"}, CreatedAt: source.now()}
	if err := destinationStore.WriteYAML(destinationStore.ManifestPath("repo-test", task.TaskID), conflict); err != nil {
		t.Fatal(err)
	}
	if err := destination.Sync(context.Background(), t.TempDir(), remote, "pull"); !errors.Is(err, ErrSync) {
		t.Fatalf("sync error = %v, want identity conflict", err)
	}
}

func TestServiceReportsGitBaselineMismatchWithoutChangingGit(t *testing.T) {
	store := testStore{root: t.TempDir()}
	service := newFakeService(store, []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW",
	})
	task, err := service.CreateTask(context.Background(), t.TempDir(), "Mismatch", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: captureInput(t, "repo-test")}); err != nil {
		t.Fatal(err)
	}
	service.observe = func(string) (gitobs.Observation, error) {
		observation := cleanObservation()
		observation.Snapshot.Head.OID = "1111111111111111111111111111111111111111"
		observation.Snapshot.Worktree = gitobs.Worktree{State: "dirty", Fingerprint: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Changes: gitobs.Changes{Complete: true, UntrackedIncluded: true, IgnoredIncluded: false, Entries: []gitobs.Change{{Path: "changed.txt", IndexStatus: "unmodified", WorktreeStatus: "modified"}}}}
		return observation, nil
	}
	resume, err := service.Resume(context.Background(), t.TempDir(), task.TaskID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"HEAD or branch differs", "Worktree differs"} {
		if !strings.Contains(string(resume.Content), want) {
			t.Fatalf("resume misses %q:\n%s", want, resume.Content)
		}
	}
	status, err := service.Status(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(status.GitDifferences) != 2 {
		t.Fatalf("Git differences = %#v", status.GitDifferences)
	}
}

func TestServiceRejectsAmbiguousHeadsUntilMergeCheckpoint(t *testing.T) {
	store := testStore{root: t.TempDir()}
	service := newFakeService(store, []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW", "01ARZ3NDEKTSV4RRFFQ69G5FAX", "01ARZ3NDEKTSV4RRFFQ69G5FAY", "01ARZ3NDEKTSV4RRFFQ69G5FAZ",
	})
	task, err := service.CreateTask(context.Background(), t.TempDir(), "Branches", nil)
	if err != nil {
		t.Fatal(err)
	}
	baseInput := captureInput(t, "repo-test")
	base, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: baseInput})
	if err != nil {
		t.Fatal(err)
	}
	leftInput := captureInput(t, "repo-test")
	left, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Parents: []string{base.CheckpointID}, Input: leftInput})
	if err != nil {
		t.Fatal(err)
	}
	rightInput := captureInput(t, "repo-test")
	rightInput["context"].(map[string]any)["summary"] = "A distinct concurrent branch."
	right, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Parents: []string{base.CheckpointID}, Input: rightInput})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resume(context.Background(), t.TempDir(), task.TaskID, "", 0); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("resume ambiguity = %v", err)
	}
	merged, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Purpose: "merge", Parents: []string{left.CheckpointID, right.CheckpointID}, Input: captureInput(t, "repo-test")})
	if err != nil {
		t.Fatal(err)
	}
	resume, err := service.Resume(context.Background(), t.TempDir(), task.TaskID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if resume.CheckpointID != merged.CheckpointID {
		t.Fatalf("resume selected %s, want merge %s", resume.CheckpointID, merged.CheckpointID)
	}
}

func TestServiceNeverMutatesObservedGitWorkingCopy(t *testing.T) {
	repository := t.TempDir()
	runGit(t, repository, "init", "-q")
	runGit(t, repository, "config", "user.email", "ctx@example.test")
	runGit(t, repository, "config", "user.name", "ctx test")
	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("one\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", "tracked.txt")
	runGit(t, repository, "commit", "-qm", "initial")
	beforeHead := gitOutput(t, repository, "rev-parse", "HEAD")
	beforeStatus := gitOutput(t, repository, "status", "--porcelain=v2", "-z")

	sidecar := testStore{root: t.TempDir()}
	service := New(sidecar, Config{Client: "ctx.cli", DeviceID: "test-device"})
	service.now = func() time.Time { return time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC) }
	ids := []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW"}
	service.nextID = func(time.Time) (string, error) { id := ids[0]; ids = ids[1:]; return id, nil }
	resolved, err := service.Resolve(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateTask(context.Background(), repository, "Read-only Git", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: repository, Input: captureInput(t, resolved.RepoID)}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Status(context.Background(), repository); err != nil {
		t.Fatal(err)
	}
	if after := gitOutput(t, repository, "rev-parse", "HEAD"); after != beforeHead {
		t.Fatalf("ctx changed HEAD: %q -> %q", beforeHead, after)
	}
	if after := gitOutput(t, repository, "status", "--porcelain=v2", "-z"); after != beforeStatus {
		t.Fatalf("ctx changed worktree: %q -> %q", beforeStatus, after)
	}
}

func TestServiceRejectsIncompleteHandoffWithoutWritingCheckpoint(t *testing.T) {
	store := testStore{root: t.TempDir()}
	service := New(store, Config{Client: "ctx.cli", DeviceID: "test-device"})
	service.now = func() time.Time { return time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC) }
	service.nextID = func(time.Time) (string, error) { return "01ARZ3NDEKTSV4RRFFQ69G5FAV", nil }
	service.observe = func(string) (gitobs.Observation, error) { return cleanObservation(), nil }
	if _, err := service.CreateTask(context.Background(), t.TempDir(), "Implement ctx", nil); err != nil {
		t.Fatal(err)
	}
	input := captureInput(t, "repo-test")
	capture := input["capture"].(map[string]any)
	capture["completeness"] = "partial"
	capture["warnings"] = []any{"missing a section"}
	capture["omitted_sections"] = []any{"context.findings"}
	if _, err := service.Handoff(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: input}, nil); !errors.Is(err, ErrValidation) {
		t.Fatalf("handoff error = %v, want validation", err)
	}
	records, err := service.listCheckpoints("repo-test", "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("incomplete handoff wrote checkpoints: %#v", records)
	}
}

func captureInput(t *testing.T, repoID string) Record {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "schemas", "v1", "examples", "capture-input.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	input, err := schema.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	resources := input["context"].(map[string]any)["relevant_resources"].([]any)
	resources[0].(map[string]any)["locator"].(map[string]any)["repo_id"] = repoID
	return input
}

func cleanObservation() gitobs.Observation {
	return gitobs.Observation{
		Repository: gitobs.Repository{Root: "/tmp/repo", RepoID: "repo-test", WorkingCopyID: "workcopy-test"},
		Snapshot: gitobs.Snapshot{
			ObjectFormat: "sha1",
			Head:         gitobs.Head{State: "attached", SymbolicRef: "refs/heads/main", OID: "0123456789012345678901234567890123456789"},
			Operation:    gitobs.Operation{Kind: "none"},
			Worktree:     gitobs.Worktree{State: "clean", Fingerprint: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Changes: gitobs.Changes{Complete: true, UntrackedIncluded: true, IgnoredIncluded: false}},
		},
	}
}

func newFakeService(sidecar testStore, ids []string) *Service {
	service := New(sidecar, Config{Client: "ctx.cli", DeviceID: "test-device", Version: "test"})
	service.now = func() time.Time { return time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC) }
	service.nextID = func(time.Time) (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	}
	service.observe = func(string) (gitobs.Observation, error) { return cleanObservation(), nil }
	return service
}

func runGit(t *testing.T, cwd string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = cwd
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
}

func gitOutput(t *testing.T, cwd string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = cwd
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(arguments, " "), err)
	}
	return string(output)
}

type testStore struct{ root string }

func (s testStore) Root() string                 { return s.root }
func (s testStore) RepoDir(repoID string) string { return filepath.Join(s.root, "repos", repoID) }
func (s testStore) TaskDir(repoID, taskID string) string {
	return filepath.Join(s.RepoDir(repoID), "tasks", taskID)
}
func (s testStore) CheckpointPath(repoID, taskID, id string) string {
	return filepath.Join(s.TaskDir(repoID, taskID), "checkpoints", id+".json")
}
func (s testStore) SnapshotPath(repoID, taskID, id string) string {
	return filepath.Join(s.TaskDir(repoID, taskID), "runtime", "snapshots", id+".json")
}
func (s testStore) HandoffPath(repoID, taskID string) string {
	return filepath.Join(s.TaskDir(repoID, taskID), "handoff.md")
}
func (s testStore) ManifestPath(repoID, taskID string) string {
	return filepath.Join(s.TaskDir(repoID, taskID), "manifest.yaml")
}
func (s testStore) BindingPath(repoID, taskID, client string) string {
	return filepath.Join(s.TaskDir(repoID, taskID), "runtime", "bindings", client+".yaml")
}
func (s testStore) AtomicWrite(path string, data []byte, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, perm)
}
func (s testStore) WriteImmutableJSON(path string, record any) (bool, error) {
	data, err := json.Marshal(record)
	if err != nil {
		return false, err
	}
	if existing, err := os.ReadFile(path); err == nil {
		return string(existing) == string(data), nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, data, 0644)
}
func (s testStore) ReadJSON(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return schema.Decode(data)
}
func (s testStore) ListJSON(dir string) ([]map[string]any, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var records []map[string]any
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			record, err := s.ReadJSON(filepath.Join(dir, entry.Name()))
			if err != nil {
				return nil, err
			}
			records = append(records, record)
		}
	}
	return records, nil
}
func (s testStore) WriteYAML(path string, value any) error {
	data, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
func (s testStore) ReadYAML(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, value)
}
func (s testStore) WithRepositoryLock(_ string, fn func() error) error { return fn() }
func (s testStore) WithRepositoryTransitionLock(_ string, fn func() error) error {
	return fn()
}
func (s testStore) WithTaskLock(_ string, _ string, fn func() error) error { return fn() }
