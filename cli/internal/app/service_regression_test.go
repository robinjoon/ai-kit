package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/robinjoon/ai-kit/cli/internal/gitobs"
	"github.com/robinjoon/ai-kit/cli/internal/model"
	ctxstore "github.com/robinjoon/ai-kit/cli/internal/store"
)

func TestResumeIgnoresStaleOrBranchHandoffPointer(t *testing.T) {
	sidecar := testStore{root: t.TempDir()}
	service := newFakeService(sidecar, []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW",
		"01ARZ3NDEKTSV4RRFFQ69G5FAX", "01ARZ3NDEKTSV4RRFFQ69G5FAY",
		"01ARZ3NDEKTSV4RRFFQ69G5FAZ",
	})
	task, err := service.CreateTask(context.Background(), t.TempDir(), "Handoff head", nil)
	if err != nil {
		t.Fatal(err)
	}
	handoff, err := service.Handoff(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: captureInput(t, "repo-test")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	newerInput := captureInput(t, "repo-test")
	newerInput["context"].(map[string]any)["summary"] = "Newer stable work after handoff."
	newer, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: newerInput})
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := service.Resume(context.Background(), t.TempDir(), task.TaskID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.CheckpointID != newer.CheckpointID {
		t.Fatalf("stale handoff selected %s, want current stable head %s", resumed.CheckpointID, newer.CheckpointID)
	}

	branchInput := captureInput(t, "repo-test")
	branchInput["context"].(map[string]any)["summary"] = "Concurrent stable branch."
	if _, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Parents: []string{handoff.CheckpointID}, Input: branchInput}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resume(context.Background(), t.TempDir(), task.TaskID, "", 0); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("branched resume error = %v, want ambiguity", err)
	}
}

func TestUnbornRepositoryHandoffResumeHasNoFalseGitDifference(t *testing.T) {
	repository := t.TempDir()
	runGit(t, repository, "init", "-q")
	sidecar := testStore{root: t.TempDir()}
	service := newFakeService(sidecar, []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW", "01ARZ3NDEKTSV4RRFFQ69G5FAX",
	})
	service.observe = gitobs.Observe
	resolved, err := service.Resolve(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateTask(context.Background(), repository, "Unborn repository", nil); err != nil {
		t.Fatal(err)
	}
	handoff, err := service.Handoff(context.Background(), CheckpointRequest{CWD: repository, Input: captureInput(t, resolved.RepoID)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	newClient := New(sidecar, Config{Client: "new.client", DeviceID: "new-device"})
	newClient.now = service.now
	newClient.observe = gitobs.Observe
	resumed, err := newClient.Resume(context.Background(), repository, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.CheckpointID != handoff.CheckpointID {
		t.Fatalf("resumed checkpoint = %s, want handoff %s", resumed.CheckpointID, handoff.CheckpointID)
	}
	if strings.Contains(string(resumed.Content), "differs from the checkpoint baseline") || !strings.Contains(string(resumed.Content), "matches the checkpoint baseline") {
		t.Fatalf("unborn repository reported a false Git difference:\n%s", resumed.Content)
	}
}

func TestResumeRejectsLocallyTamperedCheckpoint(t *testing.T) {
	sidecar := testStore{root: t.TempDir()}
	service := newFakeService(sidecar, []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW"})
	task, err := service.CreateTask(context.Background(), t.TempDir(), "Tamper detection", nil)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: captureInput(t, "repo-test")})
	if err != nil {
		t.Fatal(err)
	}
	path := sidecar.CheckpointPath("repo-test", task.TaskID, checkpoint.CheckpointID)
	record, err := sidecar.ReadJSON(path)
	if err != nil {
		t.Fatal(err)
	}
	record["context"].(map[string]any)["title"] = "Tampered title"
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resume(context.Background(), t.TempDir(), task.TaskID, "", 0); !errors.Is(err, ErrValidation) {
		t.Fatalf("tampered checkpoint resume error = %v, want validation", err)
	}
}

func TestBindingIsSharedByClientAcrossSessions(t *testing.T) {
	sidecar := testStore{root: t.TempDir()}
	service := newFakeService(sidecar, []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW",
		"01ARZ3NDEKTSV4RRFFQ69G5FAX", "01ARZ3NDEKTSV4RRFFQ69G5FAY",
	})
	service.sessionID = "session-one"
	now := service.now()
	service.now = func() time.Time { return now }
	first, err := service.CreateTask(context.Background(), t.TempDir(), "First", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), TaskID: first.TaskID, Input: captureInput(t, "repo-test")}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	second, err := service.CreateTask(context.Background(), t.TempDir(), "Second", nil)
	if err != nil {
		t.Fatal(err)
	}
	secondInput := captureInput(t, "repo-test")
	secondInput["context"].(map[string]any)["summary"] = "Second task checkpoint."
	if _, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), TaskID: second.TaskID, Input: secondInput}); err != nil {
		t.Fatal(err)
	}

	newSession := New(sidecar, Config{Client: "ctx.cli", SessionID: "session-two", DeviceID: "new-device"})
	newSession.observe = service.observe
	newSession.now = service.now
	resolved, err := newSession.Resolve(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if resolved.TaskID != second.TaskID {
		t.Fatalf("new session active task = %s, want %s", resolved.TaskID, second.TaskID)
	}
	resumed, err := newSession.Resume(context.Background(), t.TempDir(), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.TaskID != second.TaskID {
		t.Fatalf("new session resumed task = %s, want %s", resumed.TaskID, second.TaskID)
	}
	bindingData, err := os.ReadFile(sidecar.BindingPath("repo-test", second.TaskID, "ctx.cli"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bindingData), "session_id") {
		t.Fatalf("app binding persisted a session ID:\n%s", bindingData)
	}
}

func TestCheckpointRejectsHandoffPurposeWithoutMutation(t *testing.T) {
	sidecar := testStore{root: t.TempDir()}
	service := newFakeService(sidecar, []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV"})
	task, err := service.CreateTask(context.Background(), t.TempDir(), "Handoff purpose", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Purpose: "handoff", Input: captureInput(t, "repo-test")}); !errors.Is(err, ErrValidation) {
		t.Fatalf("checkpoint handoff purpose error = %v, want validation", err)
	}
	records, err := service.listCheckpoints("repo-test", task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("rejected handoff purpose wrote checkpoints: %#v", records)
	}
	if _, err := os.Stat(sidecar.HandoffPath("repo-test", task.TaskID)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("rejected handoff purpose wrote pointer: %v", err)
	}
}

func TestInvalidHandoffPointerIsRegeneratedFromCurrentHandoffCheckpoint(t *testing.T) {
	sidecar := testStore{root: t.TempDir()}
	service := newFakeService(sidecar, []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW",
		"01ARZ3NDEKTSV4RRFFQ69G5FAX", "01ARZ3NDEKTSV4RRFFQ69G5FAY",
	})
	task, err := service.CreateTask(context.Background(), t.TempDir(), "Regenerate handoff", nil)
	if err != nil {
		t.Fatal(err)
	}
	handoff, err := service.Handoff(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: captureInput(t, "repo-test")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(handoff.Path, []byte("---\ninvalid: true\n---\n\ncorrupt body\n"), 0644); err != nil {
		t.Fatal(err)
	}
	resumed, err := service.Resume(context.Background(), t.TempDir(), task.TaskID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.CheckpointID != handoff.CheckpointID {
		t.Fatalf("regenerated resume = %s, want %s", resumed.CheckpointID, handoff.CheckpointID)
	}
	records, err := service.listCheckpoints("repo-test", task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := service.validHandoff("repo-test", task.TaskID, records); !ok {
		t.Fatal("regenerated handoff pointer is not valid")
	}
}

func TestSyncPullRegeneratesHandoffAndPrefersItsTask(t *testing.T) {
	sourceStore := testStore{root: t.TempDir()}
	source := newFakeService(sourceStore, []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW", "01ARZ3NDEKTSV4RRFFQ69G5FAX",
		"01ARZ3NDEKTSV4RRFFQ69G5FAY", "01ARZ3NDEKTSV4RRFFQ69G5FAZ",
	})
	handoffTask, err := source.CreateTask(context.Background(), t.TempDir(), "Handed off", nil)
	if err != nil {
		t.Fatal(err)
	}
	handoff, err := source.Handoff(context.Background(), CheckpointRequest{CWD: t.TempDir(), TaskID: handoffTask.TaskID, Input: captureInput(t, "repo-test")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	otherTask, err := source.CreateTask(context.Background(), t.TempDir(), "Other stable task", nil)
	if err != nil {
		t.Fatal(err)
	}
	otherInput := captureInput(t, "repo-test")
	otherInput["context"].(map[string]any)["summary"] = "Other stable task."
	if _, err := source.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), TaskID: otherTask.TaskID, Input: otherInput}); err != nil {
		t.Fatal(err)
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
	if _, err := os.Stat(destinationStore.HandoffPath("repo-test", handoffTask.TaskID)); err != nil {
		t.Fatalf("pulled handoff was not regenerated: %v", err)
	}
	resumed, err := destination.Resume(context.Background(), t.TempDir(), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.TaskID != handoffTask.TaskID || resumed.CheckpointID != handoff.CheckpointID {
		t.Fatalf("new device resumed task/checkpoint %s/%s, want %s/%s", resumed.TaskID, resumed.CheckpointID, handoffTask.TaskID, handoff.CheckpointID)
	}
}

func TestCompareWorkspaceTreatsOmittedOptionalFieldsAsEmpty(t *testing.T) {
	checkpoint := Record{"workspace": Record{"repositories": []any{Record{
		"repo_id": "repo-test",
		"head": Record{
			"state": "detached",
			"oid":   "0123456789012345678901234567890123456789",
		},
		"operation": Record{"kind": "none"},
		"worktree":  Record{"state": "unknown"},
	}}}}
	observation := gitobs.Observation{
		Repository: gitobs.Repository{RepoID: "repo-test"},
		Snapshot: gitobs.Snapshot{
			Head:      gitobs.Head{State: "detached", OID: "0123456789012345678901234567890123456789"},
			Operation: gitobs.Operation{Kind: "none"},
			Worktree:  gitobs.Worktree{State: "unknown"},
		},
	}
	if differences := compareWorkspace(checkpoint, observation); len(differences) != 0 {
		t.Fatalf("optional empty fields reported differences: %#v", differences)
	}
}

func TestStableCheckpointRemainsResumableBehindDraft(t *testing.T) {
	sidecar := testStore{root: t.TempDir()}
	service := newFakeService(sidecar, []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW",
		"01ARZ3NDEKTSV4RRFFQ69G5FAX", "01ARZ3NDEKTSV4RRFFQ69G5FAY",
	})
	task, err := service.CreateTask(context.Background(), t.TempDir(), "Stable behind draft", nil)
	if err != nil {
		t.Fatal(err)
	}
	stable, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: captureInput(t, "repo-test")})
	if err != nil {
		t.Fatal(err)
	}
	partial := captureInput(t, "repo-test")
	partial["context"].(map[string]any)["summary"] = "Only a partial continuation is available."
	capture := partial["capture"].(map[string]any)
	capture["completeness"] = "partial"
	capture["warnings"] = []any{"agent output ended early"}
	capture["omitted_sections"] = []any{"context.findings"}
	draft, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: partial})
	if err != nil {
		t.Fatal(err)
	}
	if draft.Stability != "draft" {
		t.Fatalf("draft stability = %q", draft.Stability)
	}
	resumed, err := service.Resume(context.Background(), t.TempDir(), task.TaskID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.CheckpointID != stable.CheckpointID {
		t.Fatalf("implicit resume = %s, want stable %s", resumed.CheckpointID, stable.CheckpointID)
	}
	snapshot, err := service.Snapshot(context.Background(), t.TempDir(), "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	snapshotRecord, err := sidecar.ReadJSON(sidecar.SnapshotPath("repo-test", task.TaskID, snapshot.SnapshotID))
	if err != nil {
		t.Fatal(err)
	}
	if snapshotRecord["active_checkpoint_id"] != stable.CheckpointID {
		t.Fatalf("snapshot active checkpoint = %v, want %s", snapshotRecord["active_checkpoint_id"], stable.CheckpointID)
	}
	explicit, err := service.Resume(context.Background(), t.TempDir(), task.TaskID, draft.CheckpointID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"WARNING", "draft checkpoint", "partial capture", "agent output ended early", "context.findings"} {
		if !strings.Contains(string(explicit.Content), want) {
			t.Fatalf("draft resume misses %q:\n%s", want, explicit.Content)
		}
	}
}

func TestManifestStatusTracksStableHeadsAndSyncRebuild(t *testing.T) {
	sidecar := testStore{root: t.TempDir()}
	service := newFakeService(sidecar, []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW",
		"01ARZ3NDEKTSV4RRFFQ69G5FAX", "01ARZ3NDEKTSV4RRFFQ69G5FAY",
	})
	task, err := service.CreateTask(context.Background(), t.TempDir(), "Head statuses", nil)
	if err != nil {
		t.Fatal(err)
	}
	base, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: captureInput(t, "repo-test")})
	if err != nil {
		t.Fatal(err)
	}
	leftInput := captureInput(t, "repo-test")
	leftInput["context"].(map[string]any)["summary"] = "Active branch."
	left, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Parents: []string{base.CheckpointID}, Input: leftInput})
	if err != nil {
		t.Fatal(err)
	}
	right, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Parents: []string{base.CheckpointID}, Input: blockedCaptureInput(t)})
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := service.ListTasks(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	assertAmbiguousHeadStatuses(t, tasks[0], left.CheckpointID, right.CheckpointID)
	status, err := service.Status(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status.GitComparisonNote, "omitted") || !strings.Contains(status.GitComparisonNote, "stable checkpoint heads") {
		t.Fatalf("Git comparison note = %q", status.GitComparisonNote)
	}
	if len(status.CheckpointGraph.Nodes) != 3 || len(status.CheckpointGraph.Edges) != 2 {
		t.Fatalf("checkpoint graph = %#v", status.CheckpointGraph)
	}
	for index := 1; index < len(status.CheckpointGraph.Nodes); index++ {
		if status.CheckpointGraph.Nodes[index-1].CheckpointID >= status.CheckpointGraph.Nodes[index].CheckpointID {
			t.Fatalf("checkpoint graph nodes are not deterministic: %#v", status.CheckpointGraph.Nodes)
		}
	}
	if status.CheckpointGraph.Edges[0].ParentID != base.CheckpointID || status.CheckpointGraph.Edges[0].ChildID != left.CheckpointID || status.CheckpointGraph.Edges[1].ParentID != base.CheckpointID || status.CheckpointGraph.Edges[1].ChildID != right.CheckpointID {
		t.Fatalf("checkpoint graph edges = %#v", status.CheckpointGraph.Edges)
	}

	remote := t.TempDir()
	if err := service.Sync(context.Background(), t.TempDir(), remote, "push"); err != nil {
		t.Fatal(err)
	}
	destinationStore := testStore{root: t.TempDir()}
	destination := New(destinationStore, Config{Client: "other.client", DeviceID: "other-device"})
	destination.now = service.now
	destination.observe = service.observe
	if err := destination.Sync(context.Background(), t.TempDir(), remote, "pull"); err != nil {
		t.Fatal(err)
	}
	synced, err := destination.ListTasks(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(synced) != 1 || synced[0].TaskID != task.TaskID {
		t.Fatalf("synced tasks = %#v", synced)
	}
	assertAmbiguousHeadStatuses(t, synced[0], left.CheckpointID, right.CheckpointID)
}

func TestUniqueStableHeadUpdatesTaskWorkStatus(t *testing.T) {
	sidecar := testStore{root: t.TempDir()}
	service := newFakeService(sidecar, []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW"})
	if _, err := service.CreateTask(context.Background(), t.TempDir(), "Blocked task", nil); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: blockedCaptureInput(t)})
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := service.ListTasks(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != "blocked" || tasks[0].HeadStatuses[checkpoint.CheckpointID] != "blocked" {
		t.Fatalf("task status = %#v", tasks)
	}
}

func TestImmediateCheckpointRetryDeduplicatesOriginalParents(t *testing.T) {
	sidecar := testStore{root: t.TempDir()}
	service := newFakeService(sidecar, []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW", "01ARZ3NDEKTSV4RRFFQ69G5FAX",
	})
	task, err := service.CreateTask(context.Background(), t.TempDir(), "Dedupe", nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: captureInput(t, "repo-test")})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: captureInput(t, "repo-test")})
	if err != nil {
		t.Fatal(err)
	}
	if !retry.Deduplicated || retry.CheckpointID != first.CheckpointID {
		t.Fatalf("retry = %#v, first = %#v", retry, first)
	}
	records, err := service.listCheckpoints("repo-test", task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("checkpoint count = %d, want 1", len(records))
	}
}

func TestHandoffPointerFailureDoesNotExposeCheckpointOrManifest(t *testing.T) {
	sidecar := testStore{root: t.TempDir()}
	service := newFakeService(sidecar, []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW", "01ARZ3NDEKTSV4RRFFQ69G5FAX",
	})
	task, err := service.CreateTask(context.Background(), t.TempDir(), "Transactional handoff", nil)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := sidecar.ManifestPath("repo-test", task.TaskID)
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	service.atomicWrite = func(path string, data []byte, perm fs.FileMode) error {
		if path == sidecar.HandoffPath("repo-test", task.TaskID) {
			return errors.New("injected handoff rename failure")
		}
		return sidecar.AtomicWrite(path, data, perm)
	}
	if _, err := service.Handoff(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: captureInput(t, "repo-test")}, nil); !errors.Is(err, ErrStore) {
		t.Fatalf("handoff error = %v, want store failure", err)
	}
	after, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("manifest changed after failed handoff:\n%s", after)
	}
	records, err := service.listCheckpoints("repo-test", task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("failed handoff exposed checkpoints: %#v", records)
	}
}

func TestNewClientAutoResumesUniqueTaskAndBinds(t *testing.T) {
	sidecar := testStore{root: t.TempDir()}
	owner := newFakeService(sidecar, []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW",
		"01ARZ3NDEKTSV4RRFFQ69G5FAX", "01ARZ3NDEKTSV4RRFFQ69G5FAY",
	})
	firstTask, err := owner.CreateTask(context.Background(), t.TempDir(), "First", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: captureInput(t, "repo-test")}); err != nil {
		t.Fatal(err)
	}
	newClient := New(sidecar, Config{Client: "new.client", DeviceID: "new-device"})
	newClient.now = owner.now
	newClient.observe = owner.observe
	resumed, err := newClient.Resume(context.Background(), t.TempDir(), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.TaskID != firstTask.TaskID {
		t.Fatalf("auto-resumed task = %s, want %s", resumed.TaskID, firstTask.TaskID)
	}
	resolved, err := newClient.Resolve(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if resolved.TaskID != firstTask.TaskID {
		t.Fatalf("new client binding = %s, want %s", resolved.TaskID, firstTask.TaskID)
	}

	secondTask, err := owner.CreateTask(context.Background(), t.TempDir(), "Second", nil)
	if err != nil {
		t.Fatal(err)
	}
	secondInput := captureInput(t, "repo-test")
	secondInput["context"].(map[string]any)["summary"] = "Second task checkpoint."
	if _, err := owner.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), TaskID: secondTask.TaskID, Input: secondInput}); err != nil {
		t.Fatal(err)
	}
	thirdClient := New(sidecar, Config{Client: "third.client", DeviceID: "third-device"})
	thirdClient.now = owner.now
	thirdClient.observe = owner.observe
	if _, err := thirdClient.Resume(context.Background(), t.TempDir(), "", "", 0); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("unbound multi-task resume error = %v, want ambiguity", err)
	}
}

func TestTaskAliasCannotConflictWithExistingTaskID(t *testing.T) {
	sidecar := testStore{root: t.TempDir()}
	service := newFakeService(sidecar, []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV"})
	first, err := service.CreateTask(context.Background(), t.TempDir(), "First", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateTask(context.Background(), t.TempDir(), "Second", []string{first.TaskID}); !errors.Is(err, ErrValidation) {
		t.Fatalf("alias collision error = %v, want validation", err)
	}
}

func TestGeneratedTaskIDCannotConflictWithExistingAlias(t *testing.T) {
	sidecar := testStore{root: t.TempDir()}
	alias := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	service := newFakeService(sidecar, []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV", alias})
	if _, err := service.CreateTask(context.Background(), t.TempDir(), "First", []string{alias}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateTask(context.Background(), t.TempDir(), "Second", nil); !errors.Is(err, ErrValidation) {
		t.Fatalf("task ID collision error = %v, want validation", err)
	}
}

func TestSyncPreflightRejectsWrongTaskDirectoryWithoutCopying(t *testing.T) {
	sourceStore := testStore{root: t.TempDir()}
	source := newFakeService(sourceStore, []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW"})
	task, err := source.CreateTask(context.Background(), t.TempDir(), "Malformed remote source", nil)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := source.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: captureInput(t, "repo-test")})
	if err != nil {
		t.Fatal(err)
	}
	remote := t.TempDir()
	if err := source.Sync(context.Background(), t.TempDir(), remote, "push"); err != nil {
		t.Fatal(err)
	}
	wrongTaskID := "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	remoteRepo := filepath.Join(remote, "repos", "repo-test")
	sourcePath := filepath.Join(remoteRepo, "tasks", task.TaskID, "checkpoints", checkpoint.CheckpointID+".json")
	badPath := filepath.Join(remoteRepo, "tasks", wrongTaskID, "checkpoints", checkpoint.CheckpointID+".json")
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(badPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	destinationStore := testStore{root: t.TempDir()}
	destination := New(destinationStore, Config{Client: "other.client", DeviceID: "other-device"})
	destination.now = source.now
	destination.observe = source.observe
	if err := destination.Sync(context.Background(), t.TempDir(), remote, "pull"); !errors.Is(err, ErrSync) {
		t.Fatalf("sync error = %v, want ErrSync", err)
	}
	state := destination.lastSync("repo-test")
	if state == nil || state.Status != "failed" {
		t.Fatalf("last sync state = %#v, want failed", state)
	}
	for _, path := range []string{
		destinationStore.CheckpointPath("repo-test", task.TaskID, checkpoint.CheckpointID),
		destinationStore.CheckpointPath("repo-test", wrongTaskID, checkpoint.CheckpointID),
	} {
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("preflight left checkpoint at %s: %v", path, err)
		}
	}
}

func TestSyncRejectsCrossStoreTaskSelectorConflictsWithoutCopying(t *testing.T) {
	const destinationTaskID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	tests := []struct {
		name               string
		sourceAliases      []string
		destinationAliases func(string) []string
	}{
		{
			name:          "same alias",
			sourceAliases: []string{"shared-sync-selector"},
			destinationAliases: func(string) []string {
				return []string{"shared-sync-selector"}
			},
		},
		{
			name:          "source task ID is destination alias",
			sourceAliases: []string{"source-sync-selector"},
			destinationAliases: func(sourceTaskID string) []string {
				return []string{sourceTaskID}
			},
		},
		{
			name:          "source alias is destination task ID",
			sourceAliases: []string{destinationTaskID},
			destinationAliases: func(string) []string {
				return []string{"destination-sync-selector"}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sourceStore := testStore{root: t.TempDir()}
			source := newFakeService(sourceStore, []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV"})
			sourceTask, err := source.CreateTask(context.Background(), t.TempDir(), "Source", test.sourceAliases)
			if err != nil {
				t.Fatal(err)
			}
			destinationStore := testStore{root: t.TempDir()}
			destination := newFakeService(destinationStore, []string{destinationTaskID})
			destinationTask, err := destination.CreateTask(context.Background(), t.TempDir(), "Destination", test.destinationAliases(sourceTask.TaskID))
			if err != nil {
				t.Fatal(err)
			}

			paths := []string{
				filepath.Join(sourceStore.RepoDir("repo-test"), "repo.yaml"),
				sourceStore.ManifestPath("repo-test", sourceTask.TaskID),
				filepath.Join(destinationStore.RepoDir("repo-test"), "repo.yaml"),
				destinationStore.ManifestPath("repo-test", destinationTask.TaskID),
			}
			before := make(map[string][]byte, len(paths))
			for _, path := range paths {
				before[path], err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
			}

			for _, direction := range []string{"push", "pull", "both"} {
				err = source.Sync(context.Background(), t.TempDir(), destinationStore.root, direction)
				if !errors.Is(err, ErrSync) || !strings.Contains(err.Error(), "task selector") {
					t.Fatalf("sync %s selector conflict = %v, want sync error", direction, err)
				}
				for _, path := range paths {
					after, readErr := os.ReadFile(path)
					if readErr != nil || !bytes.Equal(after, before[path]) {
						t.Fatalf("sync %s selector conflict changed %s: err=%v\nbefore=%s\nafter=%s", direction, path, readErr, before[path], after)
					}
				}
			}
			if _, err := os.Stat(destinationStore.TaskDir("repo-test", sourceTask.TaskID)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("sync selector conflict copied source task: %v", err)
			}
			if _, err := os.Stat(sourceStore.TaskDir("repo-test", destinationTask.TaskID)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("sync selector conflict copied destination task: %v", err)
			}
		})
	}
}

func TestSyncRejectsSelectorConflictWhenOneManifestMustBeDerived(t *testing.T) {
	for _, direction := range []string{"push", "pull", "both"} {
		t.Run(direction, func(t *testing.T) {
			sourceStore := testStore{root: t.TempDir()}
			source := newFakeService(sourceStore, []string{
				"01ARZ3NDEKTSV4RRFFQ69G5FAV",
				"01ARZ3NDEKTSV4RRFFQ69G5FAW",
			})
			sourceTask, err := source.CreateTask(context.Background(), t.TempDir(), "Source", nil)
			if err != nil {
				t.Fatal(err)
			}
			checkpoint, err := source.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: captureInput(t, "repo-test")})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(sourceStore.ManifestPath("repo-test", sourceTask.TaskID)); err != nil {
				t.Fatal(err)
			}

			destinationStore := testStore{root: t.TempDir()}
			destination := newFakeService(destinationStore, []string{"01ARZ3NDEKTSV4RRFFQ69G5FAX"})
			destinationTask, err := destination.CreateTask(context.Background(), t.TempDir(), "Destination", []string{sourceTask.TaskID})
			if err != nil {
				t.Fatal(err)
			}

			paths := []string{
				filepath.Join(sourceStore.RepoDir("repo-test"), "repo.yaml"),
				sourceStore.CheckpointPath("repo-test", sourceTask.TaskID, checkpoint.CheckpointID),
				filepath.Join(destinationStore.RepoDir("repo-test"), "repo.yaml"),
				destinationStore.ManifestPath("repo-test", destinationTask.TaskID),
			}
			before := make(map[string][]byte, len(paths))
			for _, path := range paths {
				before[path], err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
			}

			var syncErr error
			switch direction {
			case "pull":
				syncErr = destination.Sync(context.Background(), t.TempDir(), sourceStore.root, direction)
			default:
				syncErr = source.Sync(context.Background(), t.TempDir(), destinationStore.root, direction)
			}
			if !errors.Is(syncErr, ErrSync) || !strings.Contains(syncErr.Error(), "task selector") {
				t.Fatalf("sync %s selector conflict = %v, want sync error", direction, syncErr)
			}
			for _, path := range paths {
				after, readErr := os.ReadFile(path)
				if readErr != nil || !bytes.Equal(after, before[path]) {
					t.Fatalf("sync %s selector conflict changed %s: err=%v\nbefore=%s\nafter=%s", direction, path, readErr, before[path], after)
				}
			}
			if _, err := os.Stat(sourceStore.ManifestPath("repo-test", sourceTask.TaskID)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("sync %s regenerated source manifest before rejecting the conflict: %v", direction, err)
			}
			if _, err := os.Stat(destinationStore.TaskDir("repo-test", sourceTask.TaskID)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("sync %s copied source task before rejecting the conflict: %v", direction, err)
			}
			if _, err := os.Stat(sourceStore.TaskDir("repo-test", destinationTask.TaskID)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("sync %s copied destination task before rejecting the conflict: %v", direction, err)
			}
		})
	}
}

func TestSyncRecoveredIdentityYieldsToDurableManifest(t *testing.T) {
	durableStore := testStore{root: t.TempDir()}
	durable := newFakeService(durableStore, []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAV",
		"01ARZ3NDEKTSV4RRFFQ69G5FAW",
	})
	task, err := durable.CreateTask(context.Background(), t.TempDir(), "Durable title", []string{"durable-alias"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := durable.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: captureInput(t, "repo-test")}); err != nil {
		t.Fatal(err)
	}
	durableManifestPath := durableStore.ManifestPath("repo-test", task.TaskID)
	durableBytes, err := os.ReadFile(durableManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var original model.TaskManifest
	if err := durableStore.ReadYAML(durableManifestPath, &original); err != nil {
		t.Fatal(err)
	}

	remote := t.TempDir()
	if err := durable.Sync(context.Background(), t.TempDir(), remote, "push"); err != nil {
		t.Fatal(err)
	}
	remoteManifestPath := filepath.Join(remote, "repos", "repo-test", "tasks", task.TaskID, "manifest.yaml")
	if err := os.Remove(remoteManifestPath); err != nil {
		t.Fatal(err)
	}

	recoveredStore := testStore{root: t.TempDir()}
	recovered := newFakeService(recoveredStore, nil)
	if err := recovered.Sync(context.Background(), t.TempDir(), remote, "pull"); err != nil {
		t.Fatal(err)
	}
	var provisional model.TaskManifest
	if err := recoveredStore.ReadYAML(recoveredStore.ManifestPath("repo-test", task.TaskID), &provisional); err != nil {
		t.Fatal(err)
	}
	if !provisional.IdentityRecovered || provisional.TaskID != task.TaskID || len(provisional.Aliases) != 0 || provisional.Title == "" {
		t.Fatalf("recovered manifest = %#v", provisional)
	}
	if _, err := recovered.Resume(context.Background(), t.TempDir(), task.TaskID, "", 0); err != nil {
		t.Fatalf("resume with recovered identity: %v", err)
	}

	// A provisional source must never overwrite an existing durable identity.
	if err := recovered.Sync(context.Background(), t.TempDir(), durableStore.root, "push"); err != nil {
		t.Fatalf("push recovered identity to durable store: %v", err)
	}
	if after, err := os.ReadFile(durableManifestPath); err != nil || !bytes.Equal(after, durableBytes) {
		t.Fatalf("recovered push changed durable manifest: err=%v\nbefore=%s\nafter=%s", err, durableBytes, after)
	}

	// Two provisional manifests may differ in presentation fields without
	// becoming a permanent identity conflict.
	recoveredRemote := t.TempDir()
	if err := recovered.Sync(context.Background(), t.TempDir(), recoveredRemote, "push"); err != nil {
		t.Fatal(err)
	}
	otherRecoveredPath := filepath.Join(recoveredRemote, "repos", "repo-test", "tasks", task.TaskID, "manifest.yaml")
	var otherRecovered model.TaskManifest
	if err := recoveredStore.ReadYAML(otherRecoveredPath, &otherRecovered); err != nil {
		t.Fatal(err)
	}
	otherRecovered.Title = "Other checkpoint-derived title"
	if err := recoveredStore.WriteYAML(otherRecoveredPath, otherRecovered); err != nil {
		t.Fatal(err)
	}
	if err := recovered.Sync(context.Background(), t.TempDir(), recoveredRemote, "pull"); err != nil {
		t.Fatalf("pull between recovered manifests: %v", err)
	}

	// Once the durable source returns, it upgrades the provisional identity.
	if err := durable.Sync(context.Background(), t.TempDir(), remote, "push"); err != nil {
		t.Fatal(err)
	}
	if err := recovered.Sync(context.Background(), t.TempDir(), remote, "pull"); err != nil {
		t.Fatalf("rejoin durable manifest: %v", err)
	}
	var restored model.TaskManifest
	if err := recoveredStore.ReadYAML(recoveredStore.ManifestPath("repo-test", task.TaskID), &restored); err != nil {
		t.Fatal(err)
	}
	if restored.IdentityRecovered || restored.Title != original.Title || !sameAliases(restored.Aliases, original.Aliases) || !restored.CreatedAt.Equal(original.CreatedAt) {
		t.Fatalf("restored durable identity = %#v, want %#v", restored, original)
	}
}

func TestTaskManifestsDerivesOnlyValidCheckpointBackedIdentity(t *testing.T) {
	t.Run("runtime-only task directory is ignored", func(t *testing.T) {
		repository := t.TempDir()
		runtimeDirectory := filepath.Join(repository, "tasks", "runtime-only", "runtime", "snapshots")
		if err := os.MkdirAll(runtimeDirectory, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(runtimeDirectory, "snapshot.json"), []byte(`{}`), 0644); err != nil {
			t.Fatal(err)
		}
		manifests, err := taskManifestsFromRepository(repository)
		if err != nil {
			t.Fatal(err)
		}
		if len(manifests) != 0 {
			t.Fatalf("runtime-only manifests = %#v, want none", manifests)
		}
	})

	t.Run("mismatched checkpoint identity is rejected", func(t *testing.T) {
		sidecar := testStore{root: t.TempDir()}
		service := newFakeService(sidecar, []string{
			"01ARZ3NDEKTSV4RRFFQ69G5FAV",
			"01ARZ3NDEKTSV4RRFFQ69G5FAW",
		})
		task, err := service.CreateTask(context.Background(), t.TempDir(), "Source", nil)
		if err != nil {
			t.Fatal(err)
		}
		checkpoint, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: captureInput(t, "repo-test")})
		if err != nil {
			t.Fatal(err)
		}
		wrongTaskID := "01ARZ3NDEKTSV4RRFFQ69G5FAX"
		wrongPath := sidecar.CheckpointPath("repo-test", wrongTaskID, checkpoint.CheckpointID)
		data, err := os.ReadFile(sidecar.CheckpointPath("repo-test", task.TaskID, checkpoint.CheckpointID))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(wrongPath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(wrongPath, data, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := taskManifestsFromRepository(sidecar.RepoDir("repo-test")); err == nil || !strings.Contains(err.Error(), "does not match directory") {
			t.Fatalf("mismatched checkpoint identity error = %v", err)
		}
	})

	t.Run("invalid checkpoint graph is rejected", func(t *testing.T) {
		sidecar := testStore{root: t.TempDir()}
		service := newFakeService(sidecar, []string{
			"01ARZ3NDEKTSV4RRFFQ69G5FAV",
			"01ARZ3NDEKTSV4RRFFQ69G5FAW",
			"01ARZ3NDEKTSV4RRFFQ69G5FAX",
		})
		task, err := service.CreateTask(context.Background(), t.TempDir(), "Source", nil)
		if err != nil {
			t.Fatal(err)
		}
		parent, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: captureInput(t, "repo-test")})
		if err != nil {
			t.Fatal(err)
		}
		childInput := captureInput(t, "repo-test")
		childInput["context"].(map[string]any)["summary"] = "Child checkpoint"
		if _, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Parents: []string{parent.CheckpointID}, Input: childInput}); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(sidecar.ManifestPath("repo-test", task.TaskID)); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(sidecar.CheckpointPath("repo-test", task.TaskID, parent.CheckpointID)); err != nil {
			t.Fatal(err)
		}
		if _, err := taskManifestsFromRepository(sidecar.RepoDir("repo-test")); err == nil || !strings.Contains(err.Error(), "missing parent") {
			t.Fatalf("invalid checkpoint graph error = %v", err)
		}
	})
}

func TestExplicitPullMissingRemoteFailsButBothInitializesRemote(t *testing.T) {
	sidecar := testStore{root: t.TempDir()}
	service := newFakeService(sidecar, []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW"})
	task, err := service.CreateTask(context.Background(), t.TempDir(), "Initialize remote", nil)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: captureInput(t, "repo-test")})
	if err != nil {
		t.Fatal(err)
	}
	pushRemote := filepath.Join(t.TempDir(), "new-push-remote")
	if err := service.Sync(context.Background(), t.TempDir(), pushRemote, "push"); err != nil {
		t.Fatalf("initialize remote with push: %v", err)
	}
	pushCheckpoint := filepath.Join(pushRemote, "repos", "repo-test", "tasks", task.TaskID, "checkpoints", checkpoint.CheckpointID+".json")
	if _, err := os.Stat(pushCheckpoint); err != nil {
		t.Fatalf("push did not initialize remote checkpoint: %v", err)
	}
	remote := filepath.Join(t.TempDir(), "missing-remote")
	if err := service.Sync(context.Background(), t.TempDir(), remote, "pull"); !errors.Is(err, ErrSync) {
		t.Fatalf("missing remote pull error = %v, want ErrSync", err)
	}
	state := service.lastSync("repo-test")
	if state == nil || state.Status != "failed" || !strings.Contains(state.Message, "does not exist") {
		t.Fatalf("failed pull state = %#v", state)
	}
	if err := service.Sync(context.Background(), t.TempDir(), remote, "both"); err != nil {
		t.Fatalf("initialize remote with both: %v", err)
	}
	remoteCheckpoint := filepath.Join(remote, "repos", "repo-test", "tasks", task.TaskID, "checkpoints", checkpoint.CheckpointID+".json")
	if _, err := os.Stat(remoteCheckpoint); err != nil {
		t.Fatalf("both did not initialize remote checkpoint: %v", err)
	}
	state = service.lastSync("repo-test")
	if state == nil || state.Status != "ok" {
		t.Fatalf("initialized sync state = %#v", state)
	}
}

func TestExplicitPullRequiresRemoteRepositoryIdentityWithoutChangingCanonicalFiles(t *testing.T) {
	sidecar := testStore{root: t.TempDir()}
	service := newFakeService(sidecar, []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAV",
		"01ARZ3NDEKTSV4RRFFQ69G5FAW",
	})
	task, err := service.CreateTask(context.Background(), t.TempDir(), "Local task", nil)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: captureInput(t, "repo-test")})
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{
		filepath.Join(sidecar.RepoDir("repo-test"), "repo.yaml"),
		sidecar.ManifestPath("repo-test", task.TaskID),
		sidecar.CheckpointPath("repo-test", task.TaskID, checkpoint.CheckpointID),
	}
	before := make(map[string][]byte, len(paths))
	for _, path := range paths {
		before[path], err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}

	remote := t.TempDir()
	if err := os.MkdirAll(filepath.Join(remote, "repos", "repo-test", "tasks"), 0755); err != nil {
		t.Fatal(err)
	}
	err = service.Sync(context.Background(), t.TempDir(), remote, "pull")
	if !errors.Is(err, ErrSync) || !strings.Contains(err.Error(), "repo.yaml") {
		t.Fatalf("missing remote identity pull error = %v, want sync error", err)
	}
	state := service.lastSync("repo-test")
	if state == nil || state.Status != "failed" || !strings.Contains(state.Message, "repo.yaml") {
		t.Fatalf("missing remote identity sync state = %#v", state)
	}
	for _, path := range paths {
		after, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(after, before[path]) {
			t.Fatalf("failed pull changed %s: err=%v\nbefore=%s\nafter=%s", path, readErr, before[path], after)
		}
	}
	if err := sidecar.WriteYAML(filepath.Join(remote, "repos", "repo-test", "repo.yaml"), model.Repository{RepoID: "repo-test"}); err != nil {
		t.Fatal(err)
	}
	invalidRemotePath := filepath.Join(remote, "repos", "repo-test", "repo.yaml")
	invalidRemoteBytes, err := os.ReadFile(invalidRemotePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, direction := range []string{"pull", "push", "both"} {
		err = service.Sync(context.Background(), t.TempDir(), remote, direction)
		if !errors.Is(err, ErrSync) || !strings.Contains(err.Error(), "schema_version") {
			t.Fatalf("invalid remote identity %s error = %v, want sync error", direction, err)
		}
		for _, path := range paths {
			after, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(after, before[path]) {
				t.Fatalf("invalid %s changed %s: err=%v\nbefore=%s\nafter=%s", direction, path, readErr, before[path], after)
			}
		}
		if after, readErr := os.ReadFile(invalidRemotePath); readErr != nil || !bytes.Equal(after, invalidRemoteBytes) {
			t.Fatalf("invalid %s rewrote remote identity: err=%v\nbefore=%s\nafter=%s", direction, readErr, invalidRemoteBytes, after)
		}
	}
	orphaned := t.TempDir()
	orphanPath := filepath.Join(orphaned, "repos", "repo-test", "tasks", "orphan", "checkpoints", "orphan.json")
	if err := os.MkdirAll(filepath.Dir(orphanPath), 0755); err != nil {
		t.Fatal(err)
	}
	orphanBytes := []byte(`{"orphan":true}`)
	if err := os.WriteFile(orphanPath, orphanBytes, 0644); err != nil {
		t.Fatal(err)
	}
	for _, direction := range []string{"push", "both"} {
		err = service.Sync(context.Background(), t.TempDir(), orphaned, direction)
		if !errors.Is(err, ErrSync) || !strings.Contains(err.Error(), "has no repo.yaml") {
			t.Fatalf("orphaned remote %s error = %v, want sync error", direction, err)
		}
		if after, readErr := os.ReadFile(orphanPath); readErr != nil || !bytes.Equal(after, orphanBytes) {
			t.Fatalf("orphaned remote %s changed data: err=%v, data=%s", direction, readErr, after)
		}
	}

	initializable := t.TempDir()
	if err := os.MkdirAll(filepath.Join(initializable, "repos", "repo-test", "tasks"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := service.Sync(context.Background(), t.TempDir(), initializable, "both"); err != nil {
		t.Fatalf("both did not initialize an empty remote repository: %v", err)
	}
	if _, err := os.Stat(filepath.Join(initializable, "repos", "repo-test", "repo.yaml")); err != nil {
		t.Fatalf("both did not publish remote repo.yaml: %v", err)
	}
}

func TestUnsupportedLocalRepositoryMetadataIsRejectedWithoutRewrite(t *testing.T) {
	sidecar := testStore{root: t.TempDir()}
	service := newFakeService(sidecar, nil)
	if _, err := service.Resolve(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	repositoryPath := filepath.Join(sidecar.RepoDir("repo-test"), "repo.yaml")
	unsupported := []byte("schema_version: 2\nrepo_id: repo-test\nfuture_identity_field: preserve-me\nworking_copies:\n  workcopy-test: /tmp/repo\n")
	if err := os.WriteFile(repositoryPath, unsupported, 0644); err != nil {
		t.Fatal(err)
	}

	remote := filepath.Join(t.TempDir(), "remote")
	err := service.Sync(context.Background(), t.TempDir(), remote, "push")
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "schema_version 2") {
		t.Fatalf("unsupported local metadata sync error = %v, want validation", err)
	}
	after, err := os.ReadFile(repositoryPath)
	if err != nil || !bytes.Equal(after, unsupported) {
		t.Fatalf("unsupported local metadata was rewritten: err=%v\nbefore=%s\nafter=%s", err, unsupported, after)
	}
	if _, err := os.Stat(remote); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("unsupported local metadata created remote state: %v", err)
	}
	if _, err := service.readRepository("repo-test"); !errors.Is(err, ErrValidation) {
		t.Fatalf("read unsupported local metadata error = %v, want validation", err)
	}
	after, err = os.ReadFile(repositoryPath)
	if err != nil || !bytes.Equal(after, unsupported) {
		t.Fatalf("readRepository rewrote unsupported metadata: err=%v\nbefore=%s\nafter=%s", err, unsupported, after)
	}
}

func TestCreateTaskSerializesAliasPublicationAcrossWorkingCopies(t *testing.T) {
	firstRepository := "/tmp/first-working-copy"
	secondRepository := "/tmp/second-working-copy"
	observation := func(root, localRepoID, workingCopyID string) gitobs.Observation {
		result := cleanObservation()
		result.Repository.Root = root
		result.Repository.RepoID = "repo-shared"
		result.Repository.LocalRepoID = localRepoID
		result.Repository.WorkingCopyID = workingCopyID
		result.Repository.CanonicalRemote = "github.com/example/shared-portable-repository"
		return result
	}
	firstObservation := observation(firstRepository, "local-first", "workcopy-first")
	secondObservation := observation(secondRepository, "local-second", "workcopy-second")

	sidecar := ctxstore.New(t.TempDir())
	first := New(sidecar, Config{Client: "first.client", DeviceID: "first-device"})
	second := New(sidecar, Config{Client: "second.client", DeviceID: "second-device"})
	first.observe = func(string) (gitobs.Observation, error) { return firstObservation, nil }
	second.observe = func(string) (gitobs.Observation, error) { return secondObservation, nil }
	fixedNow := func() time.Time { return time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC) }
	first.now = fixedNow
	second.now = fixedNow
	first.nextID = func(time.Time) (string, error) { return "01ARZ3NDEKTSV4RRFFQ69G5FAV", nil }
	second.nextID = func(time.Time) (string, error) { return "01ARZ3NDEKTSV4RRFFQ69G5FAW", nil }
	if _, err := first.Resolve(context.Background(), firstRepository); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Resolve(context.Background(), secondRepository); err != nil {
		t.Fatal(err)
	}

	firstAtPreflight := make(chan struct{})
	releaseFirst := make(chan struct{})
	first.afterTaskSelectorPreflight = func() {
		close(firstAtPreflight)
		<-releaseFirst
	}
	secondAtPreflight := make(chan struct{})
	second.afterTaskSelectorPreflight = func() { close(secondAtPreflight) }
	type createResult struct {
		task TaskSummary
		err  error
	}
	firstFinished := make(chan createResult, 1)
	go func() {
		task, err := first.CreateTask(context.Background(), firstRepository, "First", []string{"shared-alias"})
		firstFinished <- createResult{task: task, err: err}
	}()
	<-firstAtPreflight
	secondFinished := make(chan createResult, 1)
	go func() {
		task, err := second.CreateTask(context.Background(), secondRepository, "Second", []string{"shared-alias"})
		secondFinished <- createResult{task: task, err: err}
	}()
	secondReachedBeforeRelease := false
	select {
	case <-secondAtPreflight:
		secondReachedBeforeRelease = true
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseFirst)
	firstResult := <-firstFinished
	secondResult := <-secondFinished
	if secondReachedBeforeRelease {
		t.Fatal("second working copy passed selector preflight while the first publication was in flight")
	}
	if firstResult.err != nil {
		t.Fatalf("first create: %v", firstResult.err)
	}
	if !errors.Is(secondResult.err, ErrValidation) {
		t.Fatalf("second create error = %v, want validation", secondResult.err)
	}
	manifests, err := first.listManifests(firstObservation.Repository.RepoID)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 || !contains(manifests[0].Aliases, "shared-alias") {
		t.Fatalf("published manifests = %#v", manifests)
	}
}

func TestRepositoryLockKeysAreSortedAndDeduplicated(t *testing.T) {
	tests := []struct {
		name        string
		observation gitobs.Observation
		want        []string
	}{
		{
			name: "sorted local and portable keys",
			observation: gitobs.Observation{Repository: gitobs.Repository{
				LocalRepoID: "z-local",
				RepoID:      "a-portable",
			}},
			want: []string{"a-portable", "z-local"},
		},
		{
			name: "deduplicated local-only key",
			observation: gitobs.Observation{Repository: gitobs.Repository{
				LocalRepoID: "local-only",
				RepoID:      "local-only",
			}},
			want: []string{"local-only"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := repositoryLockKeys(test.observation)
			if fmt.Sprint(got) != fmt.Sprint(test.want) {
				t.Fatalf("repositoryLockKeys() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestConcurrentPushesSerializeSelectorPreflightAndPublicationAtRemote(t *testing.T) {
	observation := func(root, localRepoID, workingCopyID string) gitobs.Observation {
		result := cleanObservation()
		result.Repository.Root = root
		result.Repository.LocalRepoID = localRepoID
		result.Repository.WorkingCopyID = workingCopyID
		return result
	}
	firstStore := ctxstore.New(t.TempDir())
	secondStore := ctxstore.New(t.TempDir())
	first := New(firstStore, Config{Client: "first.client", DeviceID: "first-device"})
	second := New(secondStore, Config{Client: "second.client", DeviceID: "second-device"})
	first.observe = func(string) (gitobs.Observation, error) {
		return observation("/tmp/first-producer", "local-first", "workcopy-first"), nil
	}
	second.observe = func(string) (gitobs.Observation, error) {
		return observation("/tmp/second-producer", "local-second", "workcopy-second"), nil
	}
	fixedNow := func() time.Time { return time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC) }
	first.now = fixedNow
	second.now = fixedNow
	first.nextID = func(time.Time) (string, error) { return "01ARZ3NDEKTSV4RRFFQ69G5FAV", nil }
	second.nextID = func(time.Time) (string, error) { return "01ARZ3NDEKTSV4RRFFQ69G5FAW", nil }
	if _, err := first.CreateTask(context.Background(), "/tmp/first-producer", "First", []string{"shared-remote-alias"}); err != nil {
		t.Fatal(err)
	}
	if _, err := second.CreateTask(context.Background(), "/tmp/second-producer", "Second", []string{"shared-remote-alias"}); err != nil {
		t.Fatal(err)
	}

	firstAtPreflight := make(chan struct{})
	releaseFirst := make(chan struct{})
	first.afterSyncPreflight = func() {
		close(firstAtPreflight)
		<-releaseFirst
	}
	secondAtPreflight := make(chan struct{})
	second.afterSyncPreflight = func() { close(secondAtPreflight) }
	remote := t.TempDir()
	firstFinished := make(chan error, 1)
	go func() { firstFinished <- first.Sync(context.Background(), "/tmp/first-producer", remote, "push") }()
	<-firstAtPreflight
	secondFinished := make(chan error, 1)
	go func() { secondFinished <- second.Sync(context.Background(), "/tmp/second-producer", remote, "push") }()
	secondReachedBeforeRelease := false
	select {
	case <-secondAtPreflight:
		secondReachedBeforeRelease = true
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	firstErr := <-firstFinished
	secondErr := <-secondFinished
	if secondReachedBeforeRelease {
		t.Fatal("second producer passed remote preflight while the first publication was in flight")
	}
	if firstErr != nil {
		t.Fatalf("first push: %v", firstErr)
	}
	if !errors.Is(secondErr, ErrSync) || !strings.Contains(secondErr.Error(), "selector") {
		t.Fatalf("second push error = %v, want sync selector conflict", secondErr)
	}
	remoteRepo := filepath.Join(remote, "repos", "repo-test")
	manifests, err := taskManifestsFromRepository(remoteRepo)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 || !contains(manifests[0].Aliases, "shared-remote-alias") {
		t.Fatalf("remote manifests = %#v", manifests)
	}
	if err := validateTaskSelectorUnion(manifests); err != nil {
		t.Fatalf("remote selector ambiguity: %v", err)
	}
	if _, err := os.Stat(filepath.Join(remote, "locks", "repositories", "repo-test.lock")); err != nil {
		t.Fatalf("remote repository lock missing: %v", err)
	}
}

func TestExistingRepositoryIdentityMismatchIsRejectedWithoutRewriteOrRemoteInitialization(t *testing.T) {
	for _, test := range []struct {
		name       string
		repository string
		want       string
	}{
		{
			name:       "repo ID",
			repository: "schema_version: 1\nrepo_id: repo-other\ncanonical_remote: github.com/example/current\nfuture_identity_field: preserve-me\n",
			want:       "repo_id",
		},
		{
			name:       "canonical remote",
			repository: "schema_version: 1\nrepo_id: repo-test\ncanonical_remote: github.com/example/other\nfuture_identity_field: preserve-me\n",
			want:       "canonical_remote",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			sidecar := testStore{root: t.TempDir()}
			service := New(sidecar, Config{Client: "ctx.cli", DeviceID: "test-device"})
			service.observe = func(string) (gitobs.Observation, error) {
				observation := cleanObservation()
				observation.Repository.LocalRepoID = "local-test"
				observation.Repository.CanonicalRemote = "github.com/example/current"
				return observation, nil
			}
			path := filepath.Join(sidecar.RepoDir("repo-test"), "repo.yaml")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			before := []byte(test.repository)
			if err := os.WriteFile(path, before, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := service.Resolve(context.Background(), t.TempDir()); !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("resolve error = %v, want validation mentioning %s", err, test.want)
			}
			remote := t.TempDir()
			if err := service.Sync(context.Background(), t.TempDir(), remote, "push"); !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("sync error = %v, want validation mentioning %s", err, test.want)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, before) {
				t.Fatalf("repository identity changed: err=%v\nbefore=%s\nafter=%s", err, before, after)
			}
			if _, err := os.Stat(filepath.Join(remote, "repos", "repo-test")); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("remote repository was initialized: %v", err)
			}
		})
	}
}

func TestRepositoryMetadataAllowsRetainedCanonicalRemoteWhenGitRemoteIsAbsent(t *testing.T) {
	repository := model.Repository{SchemaVersion: model.SchemaVersion, RepoID: "local-test", CanonicalRemote: "github.com/example/previous"}
	resolved := Resolved{RepoID: "local-test"}
	if err := validateRepositoryMetadataForResolved(repository, resolved, "/store/repos/local-test/repo.yaml"); err != nil {
		t.Fatalf("retained canonical remote rejected: %v", err)
	}
}

func TestUnsupportedTaskManifestIsRejectedWithoutRewriteOrRemoteInitialization(t *testing.T) {
	sidecar := testStore{root: t.TempDir()}
	service := newFakeService(sidecar, []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV"})
	task, err := service.CreateTask(context.Background(), t.TempDir(), "Future task", []string{"future-task"})
	if err != nil {
		t.Fatal(err)
	}
	path := sidecar.ManifestPath("repo-test", task.TaskID)
	before := []byte("schema_version: 2\ntask_id: " + task.TaskID + "\ntitle: Future task\nstatus: active\naliases:\n  - future-task\nfuture_task_field: preserve-me\n")
	if err := os.WriteFile(path, before, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.readManifest("repo-test", task.TaskID); !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "schema_version 2") {
		t.Fatalf("readManifest error = %v, want schema validation", err)
	}
	remote := t.TempDir()
	if err := service.Sync(context.Background(), t.TempDir(), remote, "push"); (!errors.Is(err, ErrValidation) && !errors.Is(err, ErrSync)) || !strings.Contains(err.Error(), "schema_version 2") {
		t.Fatalf("sync error = %v, want validation or sync schema failure", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("task manifest changed: err=%v\nbefore=%s\nafter=%s", err, before, after)
	}
	if _, err := os.Stat(filepath.Join(remote, "repos", "repo-test")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("remote repository was initialized: %v", err)
	}
}

func TestSyncPreflightValidatesCheckpointFilenameAndGraph(t *testing.T) {
	t.Run("filename", func(t *testing.T) {
		sidecar := testStore{root: t.TempDir()}
		service := newFakeService(sidecar, []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW"})
		task, err := service.CreateTask(context.Background(), t.TempDir(), "Filename", nil)
		if err != nil {
			t.Fatal(err)
		}
		checkpoint, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: captureInput(t, "repo-test")})
		if err != nil {
			t.Fatal(err)
		}
		path := sidecar.CheckpointPath("repo-test", task.TaskID, checkpoint.CheckpointID)
		if err := os.Rename(path, filepath.Join(filepath.Dir(path), "wrong-name.json")); err != nil {
			t.Fatal(err)
		}
		if err := preflightSyncRepository(sidecar.RepoDir("repo-test"), filepath.Join(t.TempDir(), "repo-test")); err == nil || !strings.Contains(err.Error(), "filename") {
			t.Fatalf("filename preflight error = %v", err)
		}
	})

	t.Run("missing parent", func(t *testing.T) {
		sidecar := testStore{root: t.TempDir()}
		service := newFakeService(sidecar, []string{
			"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW", "01ARZ3NDEKTSV4RRFFQ69G5FAX",
		})
		task, err := service.CreateTask(context.Background(), t.TempDir(), "Graph", nil)
		if err != nil {
			t.Fatal(err)
		}
		parent, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: captureInput(t, "repo-test")})
		if err != nil {
			t.Fatal(err)
		}
		childInput := captureInput(t, "repo-test")
		childInput["context"].(map[string]any)["summary"] = "Child checkpoint."
		if _, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: t.TempDir(), Input: childInput}); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(sidecar.CheckpointPath("repo-test", task.TaskID, parent.CheckpointID)); err != nil {
			t.Fatal(err)
		}
		if err := preflightSyncRepository(sidecar.RepoDir("repo-test"), filepath.Join(t.TempDir(), "repo-test")); err == nil || !strings.Contains(err.Error(), "missing parent") {
			t.Fatalf("graph preflight error = %v", err)
		}
	})
}

func TestRepositoryLinkPreservesTasksBindingsAndPortableAliases(t *testing.T) {
	repository := t.TempDir()
	runGit(t, repository, "init", "-q")
	sidecar := testStore{root: t.TempDir()}
	service := newFakeService(sidecar, []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW",
		"01ARZ3NDEKTSV4RRFFQ69G5FAX", "01ARZ3NDEKTSV4RRFFQ69G5FAY",
	})
	service.observe = gitobs.Observe
	beforeRemote, err := service.Resolve(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(beforeRemote.RepoID, "local-") {
		t.Fatalf("initial repository ID = %q, want local-*", beforeRemote.RepoID)
	}
	task, err := service.CreateTask(context.Background(), repository, "Portable link", nil)
	if err != nil {
		t.Fatal(err)
	}
	handoff, err := service.Handoff(context.Background(), CheckpointRequest{CWD: repository, Input: captureInput(t, beforeRemote.RepoID)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Snapshot(context.Background(), repository, "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	oldCheckpointPath := sidecar.CheckpointPath(beforeRemote.RepoID, task.TaskID, handoff.CheckpointID)
	checkpointBytes, err := os.ReadFile(oldCheckpointPath)
	if err != nil {
		t.Fatal(err)
	}

	runGit(t, repository, "remote", "add", "origin", "https://github.com/example/ctx-link.git")
	raw, err := gitobs.Observe(repository)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw.Repository.RepoID, "repo-") || raw.Repository.RepoID == beforeRemote.RepoID {
		t.Fatalf("portable repository ID = %q", raw.Repository.RepoID)
	}
	continuous, err := service.Resolve(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if continuous.RepoID != beforeRemote.RepoID || continuous.TaskID != task.TaskID {
		t.Fatalf("pre-link continuity = %#v", continuous)
	}
	visible, err := service.ListTasks(context.Background(), repository)
	if err != nil || len(visible) != 1 || visible[0].TaskID != task.TaskID {
		t.Fatalf("pre-link visible tasks = %#v, err=%v", visible, err)
	}
	preLinkResume, err := service.Resume(context.Background(), repository, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if preLinkResume.TaskID != task.TaskID || strings.Contains(string(preLinkResume.Content), "no baseline for this repository") {
		t.Fatalf("pre-link resume lost local repository continuity:\n%s", preLinkResume.Content)
	}
	preLinkStatus, err := service.Status(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if contains(preLinkStatus.GitDifferences, "The checkpoint has no baseline for this repository.") {
		t.Fatalf("pre-link status lost local repository continuity: %#v", preLinkStatus.GitDifferences)
	}

	linked, err := service.LinkRepository(context.Background(), repository, beforeRemote.RepoID)
	if err != nil {
		t.Fatal(err)
	}
	if linked.RepoID != raw.Repository.RepoID || linked.LinkedFrom != beforeRemote.RepoID || len(linked.TaskIDs) != 1 || linked.TaskIDs[0] != task.TaskID {
		t.Fatalf("link result = %#v", linked)
	}
	afterLink, err := service.Resolve(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if afterLink.RepoID != raw.Repository.RepoID || afterLink.TaskID != task.TaskID {
		t.Fatalf("post-link resolution = %#v", afterLink)
	}
	newCheckpointPath := sidecar.CheckpointPath(raw.Repository.RepoID, task.TaskID, handoff.CheckpointID)
	migratedCheckpoint, err := os.ReadFile(newCheckpointPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(checkpointBytes, migratedCheckpoint) {
		t.Fatal("repository link rewrote immutable checkpoint bytes")
	}
	for _, path := range []string{
		sidecar.HandoffPath(raw.Repository.RepoID, task.TaskID),
		sidecar.SnapshotPath(raw.Repository.RepoID, task.TaskID, snapshot.SnapshotID),
		sidecar.BindingPath(raw.Repository.RepoID, task.TaskID, "ctx.cli"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("linked task did not preserve %s: %v", path, err)
		}
	}
	if _, err := os.Stat(sidecar.TaskDir(beforeRemote.RepoID, task.TaskID)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("source task directory still exists after migration: %v", err)
	}
	repositoryMetadata, err := service.readRepository(raw.Repository.RepoID)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(repositoryMetadata.Aliases, beforeRemote.RepoID) || repositoryMetadata.WorkingCopies[raw.Repository.WorkingCopyID] != raw.Repository.Root {
		t.Fatalf("linked repository metadata = %#v", repositoryMetadata)
	}
	resumed, err := service.Resume(context.Background(), repository, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.TaskID != task.TaskID || strings.Contains(string(resumed.Content), "no baseline for this repository") {
		t.Fatalf("linked resume lost repository equivalence:\n%s", resumed.Content)
	}

	remoteStore := t.TempDir()
	if err := service.Sync(context.Background(), repository, remoteStore, "push"); err != nil {
		t.Fatal(err)
	}
	remoteMetadata, exists, err := readRepositoryFile(filepath.Join(remoteStore, "repos", raw.Repository.RepoID, "repo.yaml"))
	if err != nil || !exists {
		t.Fatalf("read portable remote metadata: exists=%v err=%v", exists, err)
	}
	if !contains(remoteMetadata.Aliases, beforeRemote.RepoID) || len(remoteMetadata.WorkingCopies) != 0 {
		t.Fatalf("remote metadata leaked or lost fields: %#v", remoteMetadata)
	}
	remoteBytes, err := os.ReadFile(filepath.Join(remoteStore, "repos", raw.Repository.RepoID, "repo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(remoteBytes, []byte(raw.Repository.Root)) {
		t.Fatalf("remote metadata contains absolute working-copy path:\n%s", remoteBytes)
	}

	newStore := testStore{root: t.TempDir()}
	newDevice := New(newStore, Config{Client: "new.client", DeviceID: "new-device"})
	newDevice.observe = gitobs.Observe
	newDevice.now = service.now
	if err := newDevice.Sync(context.Background(), repository, remoteStore, "pull"); err != nil {
		t.Fatal(err)
	}
	newMetadata, err := newDevice.readRepository(raw.Repository.RepoID)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(newMetadata.Aliases, beforeRemote.RepoID) || newMetadata.WorkingCopies[raw.Repository.WorkingCopyID] != raw.Repository.Root {
		t.Fatalf("new-device repository metadata = %#v", newMetadata)
	}
	newResume, err := newDevice.Resume(context.Background(), repository, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if newResume.TaskID != task.TaskID || strings.Contains(string(newResume.Content), "no baseline for this repository") {
		t.Fatalf("new-device alias comparison failed:\n%s", newResume.Content)
	}
}

func TestRepositoryLinkRebuildsMissingManifestFromCheckpoints(t *testing.T) {
	repository := t.TempDir()
	runGit(t, repository, "init", "-q")
	sidecar := testStore{root: t.TempDir()}
	service := newFakeService(sidecar, []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAV",
		"01ARZ3NDEKTSV4RRFFQ69G5FAW",
	})
	service.observe = gitobs.Observe
	local, err := service.Resolve(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), repository, "Original title is unavailable", []string{"lost-alias"})
	if err != nil {
		t.Fatal(err)
	}
	input := captureInput(t, local.RepoID)
	input["context"].(map[string]any)["title"] = "Recovered checkpoint title"
	checkpoint, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: repository, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	sourceCheckpoint := sidecar.CheckpointPath(local.RepoID, task.TaskID, checkpoint.CheckpointID)
	checkpointBytes, err := os.ReadFile(sourceCheckpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sidecar.ManifestPath(local.RepoID, task.TaskID)); err != nil {
		t.Fatal(err)
	}

	runGit(t, repository, "remote", "add", "origin", "https://github.com/example/ctx-recover-manifest.git")
	portable, err := gitobs.Observe(repository)
	if err != nil {
		t.Fatal(err)
	}
	linked, err := service.LinkRepository(context.Background(), repository, local.RepoID)
	if err != nil {
		t.Fatal(err)
	}
	if linked.RepoID != portable.Repository.RepoID || len(linked.TaskIDs) != 1 || linked.TaskIDs[0] != task.TaskID {
		t.Fatalf("link result = %#v", linked)
	}

	tasks, err := service.ListTasks(context.Background(), repository)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("linked tasks = %#v, err=%v", tasks, err)
	}
	if tasks[0].TaskID != task.TaskID || tasks[0].Title != "Recovered checkpoint title" || len(tasks[0].Aliases) != 0 {
		t.Fatalf("recovered task identity = %#v", tasks[0])
	}
	if len(tasks[0].HeadIDs) != 1 || tasks[0].HeadIDs[0] != checkpoint.CheckpointID || tasks[0].StableHeadID != checkpoint.CheckpointID || tasks[0].Status != "active" || tasks[0].LastUsedAt == "" {
		t.Fatalf("recovered checkpoint metadata = %#v", tasks[0])
	}
	resolved, err := service.Resolve(context.Background(), repository)
	if err != nil || resolved.RepoID != portable.Repository.RepoID || resolved.TaskID != task.TaskID {
		t.Fatalf("linked resolution = %#v, err=%v", resolved, err)
	}
	status, err := service.Status(context.Background(), repository)
	if err != nil || status.Task == nil || status.Task.TaskID != task.TaskID || len(status.Heads) != 1 || status.Heads[0] != checkpoint.CheckpointID {
		t.Fatalf("linked status = %#v, err=%v", status, err)
	}
	resumed, err := service.Resume(context.Background(), repository, task.TaskID, "", 0)
	if err != nil || resumed.TaskID != task.TaskID || resumed.CheckpointID != checkpoint.CheckpointID {
		t.Fatalf("linked resume = %#v, err=%v", resumed, err)
	}

	destinationCheckpoint := sidecar.CheckpointPath(portable.Repository.RepoID, task.TaskID, checkpoint.CheckpointID)
	migratedBytes, err := os.ReadFile(destinationCheckpoint)
	if err != nil || !bytes.Equal(migratedBytes, checkpointBytes) {
		t.Fatalf("linked checkpoint bytes changed: err=%v", err)
	}
	if _, err := os.Stat(sourceCheckpoint); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("source checkpoint still exists after task move: %v", err)
	}
}

func TestRepositoryLinkWaitsForInFlightSnapshotAndMovesItsWrite(t *testing.T) {
	repository := t.TempDir()
	runGit(t, repository, "init", "-q")
	sidecar := ctxstore.New(t.TempDir())
	setup := New(sidecar, Config{Client: "ctx.cli", DeviceID: "setup-device"})
	local, err := setup.Resolve(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	task, err := setup.CreateTask(context.Background(), repository, "Snapshot race", nil)
	if err != nil {
		t.Fatal(err)
	}

	taskLockHeld := make(chan struct{})
	releaseTaskLock := make(chan struct{})
	taskLockFinished := make(chan error, 1)
	go func() {
		taskLockFinished <- sidecar.WithTaskLock(local.RepoID, task.TaskID, func() error {
			close(taskLockHeld)
			<-releaseTaskLock
			return nil
		})
	}()
	<-taskLockHeld

	snapshotService := New(sidecar, Config{Client: "ctx.cli", DeviceID: "snapshot-device"})
	repositoryLockHeld := make(chan struct{})
	snapshotService.afterRepositoryLock = func() { close(repositoryLockHeld) }
	snapshotFinished := make(chan struct {
		result SnapshotResult
		err    error
	}, 1)
	go func() {
		result, err := snapshotService.Snapshot(context.Background(), repository, "lifecycle-hook", "session-stop")
		snapshotFinished <- struct {
			result SnapshotResult
			err    error
		}{result: result, err: err}
	}()
	<-repositoryLockHeld

	runGit(t, repository, "remote", "add", "origin", "https://github.com/example/ctx-snapshot-link-race.git")
	portable, err := gitobs.Observe(repository)
	if err != nil {
		t.Fatal(err)
	}
	linkService := New(sidecar, Config{Client: "ctx.cli", DeviceID: "link-device"})
	linkFinished := make(chan struct {
		result RepoLinkResult
		err    error
	}, 1)
	go func() {
		result, err := linkService.LinkRepository(context.Background(), repository, local.RepoID)
		linkFinished <- struct {
			result RepoLinkResult
			err    error
		}{result: result, err: err}
	}()
	select {
	case result := <-linkFinished:
		t.Fatalf("repository link passed an in-flight shared use-case: %#v", result)
	case <-time.After(30 * time.Millisecond):
	}

	close(releaseTaskLock)
	if err := <-taskLockFinished; err != nil {
		t.Fatal(err)
	}
	snapshot := <-snapshotFinished
	if snapshot.err != nil {
		t.Fatalf("snapshot: %v", snapshot.err)
	}
	linked := <-linkFinished
	if linked.err != nil {
		t.Fatalf("link: %v", linked.err)
	}
	if linked.result.RepoID != portable.Repository.RepoID {
		t.Fatalf("link result = %#v", linked.result)
	}
	if _, err := os.Stat(sidecar.SnapshotPath(portable.Repository.RepoID, task.TaskID, snapshot.result.SnapshotID)); err != nil {
		t.Fatalf("portable snapshot missing after link: %v", err)
	}
	if _, err := os.Stat(sidecar.TaskDir(local.RepoID, task.TaskID)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("snapshot recreated stale local task after link: %v", err)
	}
}

func TestFirstTasklessSnapshotPreservesLocalIdentityThroughRepositoryLink(t *testing.T) {
	repository := t.TempDir()
	runGit(t, repository, "init", "-q")
	sidecar := ctxstore.New(t.TempDir())
	service := New(sidecar, Config{Client: "ctx.cli", DeviceID: "test-device"})
	localObservation, err := gitobs.Observe(repository)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Snapshot(context.Background(), repository, "lifecycle-hook", "session-start")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.TaskID != "" {
		t.Fatalf("first taskless snapshot = %#v", snapshot)
	}
	sourceSnapshot := sidecar.SnapshotPath(localObservation.Repository.RepoID, unboundRuntimeID, snapshot.SnapshotID)
	snapshotBytes, err := os.ReadFile(sourceSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(sidecar.RepoDir(localObservation.Repository.RepoID), "repo.yaml")); err != nil {
		t.Fatalf("first snapshot did not establish local repo metadata: %v", err)
	}

	runGit(t, repository, "remote", "add", "origin", "https://github.com/example/ctx-first-snapshot.git")
	portable, err := gitobs.Observe(repository)
	if err != nil {
		t.Fatal(err)
	}
	continuous, err := service.Resolve(context.Background(), repository)
	if err != nil || continuous.RepoID != localObservation.Repository.RepoID {
		t.Fatalf("first snapshot local continuity = %#v, err=%v", continuous, err)
	}
	linked, err := service.LinkRepository(context.Background(), repository, localObservation.Repository.RepoID)
	if err != nil {
		t.Fatal(err)
	}
	if linked.RepoID != portable.Repository.RepoID || len(linked.TaskIDs) != 0 {
		t.Fatalf("taskless snapshot link result = %#v", linked)
	}
	destinationSnapshot := sidecar.SnapshotPath(portable.Repository.RepoID, unboundRuntimeID, snapshot.SnapshotID)
	if copied, err := os.ReadFile(destinationSnapshot); err != nil || !bytes.Equal(copied, snapshotBytes) {
		t.Fatalf("linked unbound snapshot = %q, err=%v; want %q", copied, err, snapshotBytes)
	}
	if source, err := os.ReadFile(sourceSnapshot); err != nil || !bytes.Equal(source, snapshotBytes) {
		t.Fatalf("link did not preserve source unbound snapshot = %q, err=%v", source, err)
	}
}

func TestCheckpointAndHandoffAfterRepositoryLinkWriteOnlyPortableIdentity(t *testing.T) {
	for _, useCase := range []string{"checkpoint", "handoff"} {
		t.Run(useCase, func(t *testing.T) {
			repository := t.TempDir()
			runGit(t, repository, "init", "-q")
			sidecar := ctxstore.New(t.TempDir())
			service := New(sidecar, Config{Client: "ctx.cli", DeviceID: "test-device"})
			local, err := service.Resolve(context.Background(), repository)
			if err != nil {
				t.Fatal(err)
			}
			task, err := service.CreateTask(context.Background(), repository, "Post-link write", nil)
			if err != nil {
				t.Fatal(err)
			}
			runGit(t, repository, "remote", "add", "origin", "https://github.com/example/ctx-post-link-"+useCase+".git")
			linked, err := service.LinkRepository(context.Background(), repository, local.RepoID)
			if err != nil {
				t.Fatal(err)
			}
			var checkpointID string
			switch useCase {
			case "checkpoint":
				checkpoint, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: repository, Input: captureInput(t, linked.RepoID)})
				if err != nil {
					t.Fatal(err)
				}
				checkpointID = checkpoint.CheckpointID
			case "handoff":
				handoff, err := service.Handoff(context.Background(), CheckpointRequest{CWD: repository, Input: captureInput(t, linked.RepoID)}, nil)
				if err != nil {
					t.Fatal(err)
				}
				checkpointID = handoff.CheckpointID
			}
			if _, err := os.Stat(sidecar.CheckpointPath(linked.RepoID, task.TaskID, checkpointID)); err != nil {
				t.Fatalf("portable checkpoint missing: %v", err)
			}
			if _, err := os.Stat(sidecar.TaskDir(local.RepoID, task.TaskID)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("post-link %s recreated local task: %v", useCase, err)
			}
		})
	}
}

func TestRepositoryLinkRejectsMissingManifestSelectorConflictWithoutMutation(t *testing.T) {
	repository := t.TempDir()
	runGit(t, repository, "init", "-q")
	sidecar := testStore{root: t.TempDir()}
	service := newFakeService(sidecar, []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAV",
		"01ARZ3NDEKTSV4RRFFQ69G5FAW",
	})
	service.observe = gitobs.Observe
	local, err := service.Resolve(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	sourceTask, err := service.CreateTask(context.Background(), repository, "Source", nil)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: repository, Input: captureInput(t, local.RepoID)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sidecar.ManifestPath(local.RepoID, sourceTask.TaskID)); err != nil {
		t.Fatal(err)
	}

	runGit(t, repository, "remote", "add", "origin", "https://github.com/example/ctx-recover-selector.git")
	portable, err := gitobs.Observe(repository)
	if err != nil {
		t.Fatal(err)
	}
	destinationTaskID := "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	destinationRepository := model.Repository{
		SchemaVersion:   model.SchemaVersion,
		RepoID:          portable.Repository.RepoID,
		CanonicalRemote: portable.Repository.CanonicalRemote,
	}
	if err := sidecar.WriteYAML(filepath.Join(sidecar.RepoDir(portable.Repository.RepoID), "repo.yaml"), destinationRepository); err != nil {
		t.Fatal(err)
	}
	if err := sidecar.WriteYAML(sidecar.ManifestPath(portable.Repository.RepoID, destinationTaskID), model.TaskManifest{
		SchemaVersion: model.SchemaVersion,
		TaskID:        destinationTaskID,
		Title:         "Destination",
		Status:        "active",
		Aliases:       []string{sourceTask.TaskID},
		CreatedAt:     time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	paths := []string{
		filepath.Join(sidecar.RepoDir(local.RepoID), "repo.yaml"),
		sidecar.CheckpointPath(local.RepoID, sourceTask.TaskID, checkpoint.CheckpointID),
		filepath.Join(sidecar.RepoDir(portable.Repository.RepoID), "repo.yaml"),
		sidecar.ManifestPath(portable.Repository.RepoID, destinationTaskID),
	}
	before := make(map[string][]byte, len(paths))
	for _, path := range paths {
		before[path], err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}

	_, err = service.LinkRepository(context.Background(), repository, local.RepoID)
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "task selector") {
		t.Fatalf("missing-manifest selector conflict = %v, want validation", err)
	}
	for _, path := range paths {
		after, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(after, before[path]) {
			t.Fatalf("selector conflict changed %s: err=%v\nbefore=%s\nafter=%s", path, readErr, before[path], after)
		}
	}
	if _, err := os.Stat(sidecar.TaskDir(portable.Repository.RepoID, sourceTask.TaskID)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("selector conflict moved source task: %v", err)
	}
	if _, err := os.Stat(sidecar.ManifestPath(local.RepoID, sourceTask.TaskID)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("selector conflict rebuilt source manifest: %v", err)
	}
}

func TestRepositoryLinkRejectsInvalidCheckpointBackedIdentityBeforeMove(t *testing.T) {
	tests := []struct {
		name   string
		breaks func(*testing.T, testStore, *Service, string, string, TaskSummary, CheckpointResult) string
		want   string
	}{
		{
			name: "task identity mismatch",
			breaks: func(t *testing.T, sidecar testStore, _ *Service, repoID, _ string, task TaskSummary, checkpoint CheckpointResult) string {
				wrongTaskID := "01ARZ3NDEKTSV4RRFFQ69G5FAX"
				if err := os.Rename(sidecar.TaskDir(repoID, task.TaskID), sidecar.TaskDir(repoID, wrongTaskID)); err != nil {
					t.Fatal(err)
				}
				return sidecar.CheckpointPath(repoID, wrongTaskID, checkpoint.CheckpointID)
			},
			want: "does not match directory",
		},
		{
			name: "checkpoint graph missing parent",
			breaks: func(t *testing.T, sidecar testStore, service *Service, repoID, cwd string, task TaskSummary, checkpoint CheckpointResult) string {
				childInput := captureInput(t, repoID)
				childInput["context"].(map[string]any)["summary"] = "Child checkpoint"
				child, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: cwd, Parents: []string{checkpoint.CheckpointID}, Input: childInput})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(sidecar.CheckpointPath(repoID, task.TaskID, checkpoint.CheckpointID)); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(sidecar.ManifestPath(repoID, task.TaskID)); err != nil {
					t.Fatal(err)
				}
				return sidecar.CheckpointPath(repoID, task.TaskID, child.CheckpointID)
			},
			want: "missing parent",
		},
		{
			name: "durable manifest checkpoint graph missing parent",
			breaks: func(t *testing.T, sidecar testStore, service *Service, repoID, cwd string, task TaskSummary, checkpoint CheckpointResult) string {
				childInput := captureInput(t, repoID)
				childInput["context"].(map[string]any)["summary"] = "Durable child checkpoint"
				child, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: cwd, Parents: []string{checkpoint.CheckpointID}, Input: childInput})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(sidecar.CheckpointPath(repoID, task.TaskID, checkpoint.CheckpointID)); err != nil {
					t.Fatal(err)
				}
				return sidecar.CheckpointPath(repoID, task.TaskID, child.CheckpointID)
			},
			want: "missing parent",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := t.TempDir()
			runGit(t, repository, "init", "-q")
			sidecar := testStore{root: t.TempDir()}
			service := newFakeService(sidecar, []string{
				"01ARZ3NDEKTSV4RRFFQ69G5FAV",
				"01ARZ3NDEKTSV4RRFFQ69G5FAW",
				"01ARZ3NDEKTSV4RRFFQ69G5FAX",
			})
			service.observe = gitobs.Observe
			local, err := service.Resolve(context.Background(), repository)
			if err != nil {
				t.Fatal(err)
			}
			task, err := service.CreateTask(context.Background(), repository, "Source", nil)
			if err != nil {
				t.Fatal(err)
			}
			checkpoint, err := service.Checkpoint(context.Background(), CheckpointRequest{CWD: repository, Input: captureInput(t, local.RepoID)})
			if err != nil {
				t.Fatal(err)
			}
			if test.name == "task identity mismatch" {
				if err := os.Remove(sidecar.ManifestPath(local.RepoID, task.TaskID)); err != nil {
					t.Fatal(err)
				}
			}
			sourceCheckpoint := test.breaks(t, sidecar, service, local.RepoID, repository, task, checkpoint)
			checkpointBytes, err := os.ReadFile(sourceCheckpoint)
			if err != nil {
				t.Fatal(err)
			}
			sourceRepoPath := filepath.Join(sidecar.RepoDir(local.RepoID), "repo.yaml")
			repoBytes, err := os.ReadFile(sourceRepoPath)
			if err != nil {
				t.Fatal(err)
			}

			runGit(t, repository, "remote", "add", "origin", "https://github.com/example/ctx-invalid-link.git")
			portable, err := gitobs.Observe(repository)
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.LinkRepository(context.Background(), repository, local.RepoID)
			if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("invalid checkpoint-backed link error = %v, want validation containing %q", err, test.want)
			}
			if after, readErr := os.ReadFile(sourceRepoPath); readErr != nil || !bytes.Equal(after, repoBytes) {
				t.Fatalf("invalid link changed source repo metadata: err=%v", readErr)
			}
			if after, readErr := os.ReadFile(sourceCheckpoint); readErr != nil || !bytes.Equal(after, checkpointBytes) {
				t.Fatalf("invalid link moved or changed checkpoint: err=%v", readErr)
			}
			if _, err := os.Stat(sidecar.RepoDir(portable.Repository.RepoID)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("invalid link published destination repository: %v", err)
			}
		})
	}
}

func TestObservationErrorPreservesRepositoryLookupFailures(t *testing.T) {
	for _, expected := range []error{ErrStore, ErrAmbiguous, ErrValidation, ErrNotFound} {
		if err := observationError(fmt.Errorf("wrapped: %w", expected)); !errors.Is(err, expected) || errors.Is(err, ErrGit) {
			t.Fatalf("observationError(%v) = %v", expected, err)
		}
	}
	raw := errors.New("git command failed")
	if err := observationError(raw); !errors.Is(err, ErrGit) {
		t.Fatalf("raw observation error = %v, want ErrGit", err)
	}
}

func TestRepositoryLinkRejectsTaskCollisionWithoutHidingSource(t *testing.T) {
	repository := t.TempDir()
	runGit(t, repository, "init", "-q")
	sidecar := testStore{root: t.TempDir()}
	service := newFakeService(sidecar, []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV"})
	service.observe = gitobs.Observe
	local, err := service.Resolve(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), repository, "Collision", nil)
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "remote", "add", "origin", "https://github.com/example/ctx-collision.git")
	raw, err := gitobs.Observe(repository)
	if err != nil {
		t.Fatal(err)
	}
	destinationTask := sidecar.TaskDir(raw.Repository.RepoID, task.TaskID)
	if err := os.MkdirAll(destinationTask, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(destinationTask, "existing")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.LinkRepository(context.Background(), repository, local.RepoID); !errors.Is(err, ErrValidation) {
		t.Fatalf("link collision error = %v, want validation", err)
	}
	if _, err := os.Stat(sidecar.ManifestPath(local.RepoID, task.TaskID)); err != nil {
		t.Fatalf("source task was moved despite collision: %v", err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
		t.Fatalf("destination collision was overwritten: %q, %v", data, err)
	}
	continuous, err := service.Resolve(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if continuous.RepoID != local.RepoID || continuous.TaskID != task.TaskID {
		t.Fatalf("failed link hid source task: %#v", continuous)
	}
}

func TestRepositoryLinkRejectsCrossRepositoryTaskSelectorConflictsWithoutMutation(t *testing.T) {
	const destinationTaskID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	tests := []struct {
		name               string
		sourceAliases      []string
		destinationAliases func(string) []string
	}{
		{
			name:          "same alias",
			sourceAliases: []string{"shared-selector"},
			destinationAliases: func(string) []string {
				return []string{"shared-selector"}
			},
		},
		{
			name:          "source task ID is destination alias",
			sourceAliases: []string{"source-selector"},
			destinationAliases: func(sourceTaskID string) []string {
				return []string{sourceTaskID}
			},
		},
		{
			name:          "source alias is destination task ID",
			sourceAliases: []string{destinationTaskID},
			destinationAliases: func(string) []string {
				return []string{"destination-selector"}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := t.TempDir()
			runGit(t, repository, "init", "-q")
			sidecar := testStore{root: t.TempDir()}
			service := newFakeService(sidecar, []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV"})
			service.observe = gitobs.Observe
			local, err := service.Resolve(context.Background(), repository)
			if err != nil {
				t.Fatal(err)
			}
			sourceTask, err := service.CreateTask(context.Background(), repository, "Source", test.sourceAliases)
			if err != nil {
				t.Fatal(err)
			}
			runGit(t, repository, "remote", "add", "origin", "https://github.com/example/ctx-selector-conflict.git")
			raw, err := gitobs.Observe(repository)
			if err != nil {
				t.Fatal(err)
			}
			destinationRepository := model.Repository{
				SchemaVersion:   model.SchemaVersion,
				RepoID:          raw.Repository.RepoID,
				CanonicalRemote: raw.Repository.CanonicalRemote,
				Aliases:         []string{"existing-repository-alias"},
				WorkingCopies:   map[string]string{"destination-copy": "/sentinel/destination"},
			}
			if err := sidecar.WriteYAML(filepath.Join(sidecar.RepoDir(raw.Repository.RepoID), "repo.yaml"), destinationRepository); err != nil {
				t.Fatal(err)
			}
			destinationManifest := model.TaskManifest{
				SchemaVersion: model.SchemaVersion,
				TaskID:        destinationTaskID,
				Title:         "Destination",
				Status:        "active",
				Aliases:       test.destinationAliases(sourceTask.TaskID),
				CreatedAt:     time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC),
			}
			if err := sidecar.WriteYAML(sidecar.ManifestPath(raw.Repository.RepoID, destinationTaskID), destinationManifest); err != nil {
				t.Fatal(err)
			}

			paths := []string{
				filepath.Join(sidecar.RepoDir(local.RepoID), "repo.yaml"),
				sidecar.ManifestPath(local.RepoID, sourceTask.TaskID),
				filepath.Join(sidecar.RepoDir(raw.Repository.RepoID), "repo.yaml"),
				sidecar.ManifestPath(raw.Repository.RepoID, destinationTaskID),
			}
			before := make(map[string][]byte, len(paths))
			for _, path := range paths {
				before[path], err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
			}

			if _, err := service.LinkRepository(context.Background(), repository, local.RepoID); !errors.Is(err, ErrValidation) {
				t.Fatalf("selector conflict error = %v, want validation", err)
			}
			for _, path := range paths {
				after, readErr := os.ReadFile(path)
				if readErr != nil || !bytes.Equal(after, before[path]) {
					t.Fatalf("selector conflict changed %s: err=%v\nbefore=%s\nafter=%s", path, readErr, before[path], after)
				}
			}
			if _, err := os.Stat(sidecar.TaskDir(raw.Repository.RepoID, sourceTask.TaskID)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("selector conflict created destination source task: %v", err)
			}
		})
	}
}

func TestRepositoryLinkKeepsLocalTaskUntilExplicitLinkWhenPortableMetadataExists(t *testing.T) {
	repository := t.TempDir()
	runGit(t, repository, "init", "-q")
	sidecar := testStore{root: t.TempDir()}
	service := newFakeService(sidecar, []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV"})
	service.observe = gitobs.Observe
	local, err := service.Resolve(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), repository, "Local continuity", nil)
	if err != nil {
		t.Fatal(err)
	}

	remoteURL := "https://github.com/example/ctx-existing-portable.git"
	runGit(t, repository, "remote", "add", "origin", remoteURL)
	portable, err := gitobs.Observe(repository)
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "remote", "remove", "origin")
	if err := sidecar.WriteYAML(filepath.Join(sidecar.RepoDir(portable.Repository.RepoID), "repo.yaml"), model.Repository{
		SchemaVersion:   model.SchemaVersion,
		RepoID:          portable.Repository.RepoID,
		CanonicalRemote: portable.Repository.CanonicalRemote,
		Aliases:         []string{"existing-portable-alias"},
	}); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "remote", "add", "origin", remoteURL)

	beforeLink, err := service.Resolve(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if beforeLink.RepoID != local.RepoID || beforeLink.TaskID != task.TaskID {
		t.Fatalf("portable metadata hid the unlinked local task: %#v", beforeLink)
	}
	if _, err := service.LinkRepository(context.Background(), repository, local.RepoID); err != nil {
		t.Fatal(err)
	}
	afterLink, err := service.Resolve(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if afterLink.RepoID != portable.Repository.RepoID || afterLink.TaskID != task.TaskID {
		t.Fatalf("explicit link did not select the portable task: %#v", afterLink)
	}
}

func TestMovedRepositoryKeepsLocalTaskWhenRemoteIsAddedBeforeNextCtxCall(t *testing.T) {
	parent := t.TempDir()
	original := filepath.Join(parent, "original")
	if err := os.Mkdir(original, 0700); err != nil {
		t.Fatal(err)
	}
	runGit(t, original, "init", "-q")
	sidecar := testStore{root: t.TempDir()}
	service := newFakeService(sidecar, []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV"})
	service.observe = gitobs.Observe
	local, err := service.Resolve(context.Background(), original)
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), original, "Moved repository", nil)
	if err != nil {
		t.Fatal(err)
	}

	moved := filepath.Join(parent, "moved")
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	runGit(t, moved, "remote", "add", "origin", "https://github.com/example/ctx-moved.git")
	beforeLink, err := service.Resolve(context.Background(), moved)
	if err != nil {
		t.Fatal(err)
	}
	if beforeLink.RepoID != local.RepoID || beforeLink.TaskID != task.TaskID {
		t.Fatalf("moved repository lost local task continuity: %#v", beforeLink)
	}
	metadata, err := service.readRepository(local.RepoID)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.WorkingCopies[beforeLink.WorkingCopyID] != beforeLink.Root {
		t.Fatalf("moved working copy mapping not refreshed: %#v", metadata.WorkingCopies)
	}
	linked, err := service.LinkRepository(context.Background(), moved, local.RepoID)
	if err != nil {
		t.Fatal(err)
	}
	afterLink, err := service.Resolve(context.Background(), moved)
	if err != nil {
		t.Fatal(err)
	}
	if afterLink.RepoID != linked.RepoID || afterLink.TaskID != task.TaskID || !strings.HasPrefix(afterLink.RepoID, "repo-") {
		t.Fatalf("moved repository link result = %#v, resolved %#v", linked, afterLink)
	}
}

func TestPortableResolveIsolatesUnrelatedCorruptLocalMetadata(t *testing.T) {
	repository := t.TempDir()
	runGit(t, repository, "init", "-q")
	runGit(t, repository, "remote", "add", "origin", "https://github.com/example/ctx-corrupt-isolation.git")
	sidecar := testStore{root: t.TempDir()}
	unrelatedPath := filepath.Join(sidecar.RepoDir("local-11111111111111111111"), "repo.yaml")
	if err := os.MkdirAll(filepath.Dir(unrelatedPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unrelatedPath, []byte("working_copies: [\n"), 0600); err != nil {
		t.Fatal(err)
	}
	service := New(sidecar, Config{Client: "ctx.cli", DeviceID: "test-device"})
	resolved, err := service.Resolve(context.Background(), repository)
	if err != nil {
		t.Fatalf("unrelated corrupt local metadata broke portable resolve: %v", err)
	}
	if !strings.HasPrefix(resolved.RepoID, "repo-") {
		t.Fatalf("portable resolution = %#v", resolved)
	}
}

func TestPortableResolveReportsRelatedCorruptLocalMetadataAsStoreError(t *testing.T) {
	repository := t.TempDir()
	runGit(t, repository, "init", "-q")
	runGit(t, repository, "remote", "add", "origin", "https://github.com/example/ctx-related-corrupt.git")
	observation, err := gitobs.Observe(repository)
	if err != nil {
		t.Fatal(err)
	}
	sidecar := testStore{root: t.TempDir()}
	relatedPath := filepath.Join(sidecar.RepoDir(observation.Repository.LocalRepoID), "repo.yaml")
	if err := os.MkdirAll(filepath.Dir(relatedPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(relatedPath, []byte("working_copies: [\n"), 0600); err != nil {
		t.Fatal(err)
	}
	service := New(sidecar, Config{Client: "ctx.cli", DeviceID: "test-device"})
	if _, err := service.Resolve(context.Background(), repository); !errors.Is(err, ErrStore) || errors.Is(err, ErrGit) {
		t.Fatalf("related corrupt local metadata error = %v, want store error", err)
	}
}

func TestRepositoryLinkUnionsUnboundSnapshotsWithoutCreatingTasks(t *testing.T) {
	remoteURL := "https://github.com/example/ctx-unbound-union.git"
	portableWorkingCopy := t.TempDir()
	runGit(t, portableWorkingCopy, "init", "-q")
	runGit(t, portableWorkingCopy, "remote", "add", "origin", remoteURL)
	localWorkingCopy := t.TempDir()
	runGit(t, localWorkingCopy, "init", "-q")

	sidecar := testStore{root: t.TempDir()}
	service := newFakeService(sidecar, []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAV",
		"01ARZ3NDEKTSV4RRFFQ69G5FAW",
		"01ARZ3NDEKTSV4RRFFQ69G5FAX",
	})
	service.observe = gitobs.Observe
	portable, err := service.Resolve(context.Background(), portableWorkingCopy)
	if err != nil {
		t.Fatal(err)
	}
	destinationOnly, err := service.Snapshot(context.Background(), portableWorkingCopy, "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	local, err := service.Resolve(context.Background(), localWorkingCopy)
	if err != nil {
		t.Fatal(err)
	}
	identical, err := service.Snapshot(context.Background(), localWorkingCopy, "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	sourceOnly, err := service.Snapshot(context.Background(), localWorkingCopy, "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	identicalSourcePath := sidecar.SnapshotPath(local.RepoID, "unbound", identical.SnapshotID)
	identicalBytes, err := os.ReadFile(identicalSourcePath)
	if err != nil {
		t.Fatal(err)
	}
	identicalDestinationPath := sidecar.SnapshotPath(portable.RepoID, "unbound", identical.SnapshotID)
	if err := os.MkdirAll(filepath.Dir(identicalDestinationPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identicalDestinationPath, identicalBytes, 0600); err != nil {
		t.Fatal(err)
	}
	destinationOnlyPath := sidecar.SnapshotPath(portable.RepoID, "unbound", destinationOnly.SnapshotID)
	destinationOnlyBytes, err := os.ReadFile(destinationOnlyPath)
	if err != nil {
		t.Fatal(err)
	}
	sourceOnlyPath := sidecar.SnapshotPath(local.RepoID, "unbound", sourceOnly.SnapshotID)
	sourceOnlyBytes, err := os.ReadFile(sourceOnlyPath)
	if err != nil {
		t.Fatal(err)
	}

	runGit(t, localWorkingCopy, "remote", "add", "origin", remoteURL)
	linked, err := service.LinkRepository(context.Background(), localWorkingCopy, local.RepoID)
	if err != nil {
		t.Fatal(err)
	}
	if linked.RepoID != portable.RepoID || len(linked.TaskIDs) != 0 {
		t.Fatalf("unbound snapshots appeared as tasks: %#v", linked)
	}
	for path, want := range map[string][]byte{
		destinationOnlyPath:      destinationOnlyBytes,
		identicalDestinationPath: identicalBytes,
		sidecar.SnapshotPath(portable.RepoID, "unbound", sourceOnly.SnapshotID): sourceOnlyBytes,
	} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("portable unbound snapshot %s = %q, %v; want %q", path, got, err, want)
		}
	}
	for _, path := range []string{identicalSourcePath, sourceOnlyPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("source unbound snapshot was not preserved: %s: %v", path, err)
		}
	}
	tasks, err := service.ListTasks(context.Background(), localWorkingCopy)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("unbound snapshots became selectable tasks: %#v, %v", tasks, err)
	}
}

func TestRepositoryLinkRejectsConflictingUnboundSnapshotWithoutMutation(t *testing.T) {
	remoteURL := "https://github.com/example/ctx-unbound-conflict.git"
	portableWorkingCopy := t.TempDir()
	runGit(t, portableWorkingCopy, "init", "-q")
	runGit(t, portableWorkingCopy, "remote", "add", "origin", remoteURL)
	localWorkingCopy := t.TempDir()
	runGit(t, localWorkingCopy, "init", "-q")

	sidecar := testStore{root: t.TempDir()}
	destination := newFakeService(sidecar, []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV"})
	destination.observe = gitobs.Observe
	portable, err := destination.Resolve(context.Background(), portableWorkingCopy)
	if err != nil {
		t.Fatal(err)
	}
	destinationSnapshot, err := destination.Snapshot(context.Background(), portableWorkingCopy, "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	source := newFakeService(sidecar, []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV"})
	source.observe = gitobs.Observe
	local, err := source.Resolve(context.Background(), localWorkingCopy)
	if err != nil {
		t.Fatal(err)
	}
	sourceSnapshot, err := source.Snapshot(context.Background(), localWorkingCopy, "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	if sourceSnapshot.SnapshotID != destinationSnapshot.SnapshotID {
		t.Fatalf("test setup snapshot IDs differ: %q, %q", sourceSnapshot.SnapshotID, destinationSnapshot.SnapshotID)
	}
	runGit(t, localWorkingCopy, "remote", "add", "origin", remoteURL)

	paths := []string{
		filepath.Join(sidecar.RepoDir(local.RepoID), "repo.yaml"),
		filepath.Join(sidecar.RepoDir(portable.RepoID), "repo.yaml"),
		sidecar.SnapshotPath(local.RepoID, "unbound", sourceSnapshot.SnapshotID),
		sidecar.SnapshotPath(portable.RepoID, "unbound", destinationSnapshot.SnapshotID),
	}
	before := make(map[string][]byte, len(paths))
	for _, path := range paths {
		before[path], err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = source.LinkRepository(context.Background(), localWorkingCopy, local.RepoID)
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "immutable unbound snapshot conflict") {
		t.Fatalf("unbound snapshot conflict = %v, want validation conflict", err)
	}
	for _, path := range paths {
		after, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(after, before[path]) {
			t.Fatalf("unbound conflict changed %s: err=%v\nbefore=%s\nafter=%s", path, readErr, before[path], after)
		}
	}
}

func TestUnboundRuntimeDirectoryIsNeverATaskSelector(t *testing.T) {
	sidecar := testStore{root: t.TempDir()}
	service := newFakeService(sidecar, nil)
	if _, err := service.Resolve(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := sidecar.WriteYAML(sidecar.ManifestPath("repo-test", unboundRuntimeID), model.TaskManifest{
		SchemaVersion: model.SchemaVersion,
		TaskID:        unboundRuntimeID,
		Title:         "not a task",
		Status:        "active",
		Aliases:       []string{"runtime-only"},
		CreatedAt:     time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	tasks, err := service.ListTasks(context.Background(), t.TempDir())
	if err != nil || len(tasks) != 0 {
		t.Fatalf("unbound runtime listed as a task: %#v, %v", tasks, err)
	}
	for _, selector := range []string{unboundRuntimeID, "runtime-only"} {
		if _, err := service.findTask("repo-test", selector); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unbound selector %q error = %v, want not found", selector, err)
		}
	}
	ids, err := service.repositoryTaskIDs("repo-test")
	if err != nil || len(ids) != 0 {
		t.Fatalf("unbound runtime returned as repository task IDs: %#v, %v", ids, err)
	}
}

func blockedCaptureInput(t *testing.T) Record {
	t.Helper()
	input := captureInput(t, "repo-test")
	input["work_status"] = "blocked"
	contextRecord := input["context"].(map[string]any)
	contextRecord["summary"] = "Work is blocked on an external dependency."
	contextRecord["blockers"] = []any{Record{
		"blocker_id":        "blocker-external-dependency",
		"description":       "The dependency is unavailable.",
		"impact":            "Integration tests cannot complete.",
		"unblock_condition": "The dependency becomes reachable.",
		"status":            "active",
		"evidence_refs":     []any{},
	}}
	return input
}

func assertAmbiguousHeadStatuses(t *testing.T, task TaskSummary, activeID, blockedID string) {
	t.Helper()
	if task.Status != "ambiguous" || task.HeadStatuses[activeID] != "active" || task.HeadStatuses[blockedID] != "blocked" {
		t.Fatalf("task head statuses = %#v", task)
	}
}
