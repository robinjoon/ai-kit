package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robinjoon/ai-kit/cli/internal/app"
	"github.com/robinjoon/ai-kit/cli/internal/model"
	"github.com/robinjoon/ai-kit/cli/internal/schema"
	"github.com/robinjoon/ai-kit/cli/internal/store"
)

func TestRootVersion(t *testing.T) {
	var output bytes.Buffer
	root := NewRoot("test-version")
	root.SetOut(&output)
	root.SetArgs([]string{"--version"})

	if err := root.Execute(); err != nil {
		t.Fatalf("execute --version: %v", err)
	}
	if got, want := output.String(), "ctx test-version\n"; got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
}

func TestExitCodeMapsTypedFailures(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{app.ErrNotFound, ExitNotFound},
		{app.ErrAmbiguous, ExitAmbiguous},
		{app.ErrValidation, ExitValidation},
		{app.ErrGit, ExitGit},
		{app.ErrStore, ExitStore},
		{app.ErrSync, ExitSync},
		{errors.New("unknown command \"wat\" for \"ctx\""), ExitUsage},
	}
	for _, test := range cases {
		if got := ExitCode(test.err); got != test.want {
			t.Fatalf("ExitCode(%v) = %d, want %d", test.err, got, test.want)
		}
	}
}

func TestArgumentFailureUsesUsageExitCode(t *testing.T) {
	root := NewRoot("test-version")
	root.SetArgs([]string{"task", "switch"})
	err := root.Execute()
	if err == nil {
		t.Fatal("task switch without a selector succeeded")
	}
	if got := ExitCode(err); got != ExitUsage {
		t.Fatalf("ExitCode(%q) = %d, want %d", err, got, ExitUsage)
	}
}

func TestTypedFlagFailureUsesUsageExitCode(t *testing.T) {
	root := NewRoot("test-version")
	root.SetArgs([]string{"resume", "--max-bytes", "nope"})
	err := root.Execute()
	if err == nil {
		t.Fatal("invalid integer flag succeeded")
	}
	if got := ExitCode(err); got != ExitUsage {
		t.Fatalf("ExitCode(%T: %q) = %d, want %d", err, err, got, ExitUsage)
	}
}

func TestCommandsRejectUnexpectedPositionalArguments(t *testing.T) {
	cases := [][]string{
		{"repo", "link", "extra", "--from", "local-example"},
		{"task", "create", "extra", "--title", "title"},
		{"task", "list", "extra"},
		{"checkpoint", "extra"},
		{"handoff", "extra"},
		{"resume", "extra"},
		{"snapshot", "extra"},
		{"sync", "extra"},
	}
	for _, args := range cases {
		root := NewRoot("test-version")
		root.SetArgs(args)
		err := root.Execute()
		if err == nil {
			t.Fatalf("ctx %s accepted an unexpected positional argument", strings.Join(args, " "))
		}
		if got := ExitCode(err); got != ExitUsage {
			t.Fatalf("ctx %s exit = %d (%v), want %d", strings.Join(args, " "), got, err, ExitUsage)
		}
	}
}

func TestRepoLinkJSONWithRealGitRepository(t *testing.T) {
	repository := t.TempDir()
	command := exec.Command("git", "init", "-q")
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	storeRoot := t.TempDir()
	sidecar := store.New(storeRoot)
	service := app.New(sidecar, app.Config{Client: "ctx.cli", DeviceID: "test-device"})
	before, err := service.Resolve(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(before.RepoID, "local-") {
		t.Fatalf("initial repository ID = %q, want local-*", before.RepoID)
	}
	task, err := service.CreateTask(context.Background(), repository, "CLI repository link", nil)
	if err != nil {
		t.Fatal(err)
	}
	command = exec.Command("git", "remote", "add", "origin", "https://github.com/example/ctx-cli-link.git")
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v: %s", err, output)
	}

	var output bytes.Buffer
	root := NewRoot("test-version")
	root.SetOut(&output)
	root.SetArgs([]string{"--cwd", repository, "--store", storeRoot, "--json", "repo", "link", "--from", before.RepoID})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		OutputVersion int    `json:"output_version"`
		Command       string `json:"command"`
		Data          struct {
			RepoID     string   `json:"repo_id"`
			LinkedFrom string   `json:"linked_from"`
			TaskIDs    []string `json:"task_ids"`
		} `json:"data"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatalf("decode repo link JSON %q: %v", output.String(), err)
	}
	if envelope.OutputVersion != 1 || envelope.Command != "repo.link" || envelope.Data.LinkedFrom != before.RepoID {
		t.Fatalf("repo link envelope = %#v", envelope)
	}
	if !strings.HasPrefix(envelope.Data.RepoID, "repo-") || len(envelope.Data.TaskIDs) != 1 || envelope.Data.TaskIDs[0] != task.TaskID {
		t.Fatalf("repo link data = %#v", envelope.Data)
	}
	after, err := service.Resolve(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if after.RepoID != envelope.Data.RepoID || after.TaskID != task.TaskID {
		t.Fatalf("post-link resolution = %#v", after)
	}
}

func TestRepoLinkCLIRecoversCheckpointBackedMissingManifest(t *testing.T) {
	repository := t.TempDir()
	command := exec.Command("git", "init", "-q")
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	storeRoot := t.TempDir()
	sidecar := store.New(storeRoot)
	service := app.New(sidecar, app.Config{Client: "ctx.cli", DeviceID: "test-device"})
	local, err := service.Resolve(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), repository, "Lost durable title", []string{"lost-alias"})
	if err != nil {
		t.Fatal(err)
	}
	input := commandCaptureInput(t, local.RepoID)
	input["context"].(map[string]any)["title"] = "CLI recovered title"
	checkpoint, err := service.Checkpoint(context.Background(), app.CheckpointRequest{CWD: repository, Input: input})
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
	command = exec.Command("git", "remote", "add", "origin", "https://github.com/example/ctx-cli-recover-link.git")
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v: %s", err, output)
	}

	var linkOutput bytes.Buffer
	root := NewRoot("test-version")
	root.SetOut(&linkOutput)
	root.SetArgs([]string{"--cwd", repository, "--store", storeRoot, "--json", "repo", "link", "--from", local.RepoID})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var linkEnvelope struct {
		Data struct {
			RepoID  string   `json:"repo_id"`
			TaskIDs []string `json:"task_ids"`
		} `json:"data"`
	}
	if err := json.Unmarshal(linkOutput.Bytes(), &linkEnvelope); err != nil {
		t.Fatal(err)
	}
	if len(linkEnvelope.Data.TaskIDs) != 1 || linkEnvelope.Data.TaskIDs[0] != task.TaskID {
		t.Fatalf("repo link output = %#v", linkEnvelope)
	}

	for name, args := range map[string][]string{
		"task list": {"--json", "task", "list"},
		"status":    {"--json", "status"},
		"resume":    {"--json", "resume", "--task", task.TaskID},
	} {
		var output bytes.Buffer
		root := NewRoot("test-version")
		root.SetOut(&output)
		root.SetArgs(append([]string{"--cwd", repository, "--store", storeRoot}, args...))
		if err := root.Execute(); err != nil {
			t.Fatalf("ctx %s: %v", name, err)
		}
		if !strings.Contains(output.String(), task.TaskID) {
			t.Fatalf("ctx %s output does not contain recovered task %q: %s", name, task.TaskID, output.String())
		}
	}
	destinationCheckpoint := sidecar.CheckpointPath(linkEnvelope.Data.RepoID, task.TaskID, checkpoint.CheckpointID)
	linkedBytes, err := os.ReadFile(destinationCheckpoint)
	if err != nil || !bytes.Equal(linkedBytes, checkpointBytes) {
		t.Fatalf("repo link changed checkpoint bytes: err=%v", err)
	}
	var recovered model.TaskManifest
	if err := sidecar.ReadYAML(sidecar.ManifestPath(linkEnvelope.Data.RepoID, task.TaskID), &recovered); err != nil {
		t.Fatal(err)
	}
	if !recovered.IdentityRecovered || recovered.Title != "CLI recovered title" || len(recovered.Aliases) != 0 {
		t.Fatalf("recovered CLI manifest = %#v", recovered)
	}
}

func TestRepoLinkRejectsMalformedLocalIDWithoutPanic(t *testing.T) {
	repository := t.TempDir()
	command := exec.Command("git", "init", "-q")
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	command = exec.Command("git", "remote", "add", "origin", "https://github.com/example/ctx-malformed-link.git")
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v: %s", err, output)
	}
	storeRoot := filepath.Join(t.TempDir(), "store")
	root := NewRoot("test-version")
	root.SetArgs([]string{"--cwd", repository, "--store", storeRoot, "repo", "link", "--from", "local-../bad"})
	err := root.Execute()
	if err == nil || !errors.Is(err, app.ErrValidation) || ExitCode(err) != ExitValidation {
		t.Fatalf("malformed --from error = %v, exit %d", err, ExitCode(err))
	}
	if _, err := os.Stat(storeRoot); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("malformed --from mutated the store: %v", err)
	}
}

func TestRepoLinkCLIUnionsUnboundSnapshotsWithoutTaskIDs(t *testing.T) {
	remoteURL := "https://github.com/example/ctx-cli-unbound.git"
	portableWorkingCopy := t.TempDir()
	command := exec.Command("git", "init", "-q")
	command.Dir = portableWorkingCopy
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init portable: %v: %s", err, output)
	}
	command = exec.Command("git", "remote", "add", "origin", remoteURL)
	command.Dir = portableWorkingCopy
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git remote add portable: %v: %s", err, output)
	}
	localWorkingCopy := t.TempDir()
	command = exec.Command("git", "init", "-q")
	command.Dir = localWorkingCopy
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init local: %v: %s", err, output)
	}

	storeRoot := t.TempDir()
	sidecar := store.New(storeRoot)
	service := app.New(sidecar, app.Config{Client: "ctx.cli", DeviceID: "test-device"})
	portable, err := service.Resolve(context.Background(), portableWorkingCopy)
	if err != nil {
		t.Fatal(err)
	}
	destinationSnapshot, err := service.Snapshot(context.Background(), portableWorkingCopy, "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	local, err := service.Resolve(context.Background(), localWorkingCopy)
	if err != nil {
		t.Fatal(err)
	}
	sourceSnapshot, err := service.Snapshot(context.Background(), localWorkingCopy, "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	command = exec.Command("git", "remote", "add", "origin", remoteURL)
	command.Dir = localWorkingCopy
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git remote add local: %v: %s", err, output)
	}

	var output bytes.Buffer
	root := NewRoot("test-version")
	root.SetOut(&output)
	root.SetArgs([]string{"--cwd", localWorkingCopy, "--store", storeRoot, "--json", "repo", "link", "--from", local.RepoID})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Command string `json:"command"`
		Data    struct {
			RepoID  string   `json:"repo_id"`
			TaskIDs []string `json:"task_ids"`
		} `json:"data"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Command != "repo.link" || envelope.Data.RepoID != portable.RepoID || len(envelope.Data.TaskIDs) != 0 {
		t.Fatalf("repo link unbound JSON = %#v", envelope)
	}
	for _, snapshotID := range []string{destinationSnapshot.SnapshotID, sourceSnapshot.SnapshotID} {
		if _, err := os.Stat(sidecar.SnapshotPath(portable.RepoID, "unbound", snapshotID)); err != nil {
			t.Fatalf("portable unbound snapshot %s missing: %v", snapshotID, err)
		}
	}
}

func TestSyncCommandsRequireRemoteBeforeMutation(t *testing.T) {
	for _, args := range [][]string{{"sync"}, {"resume", "--sync"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			storeRoot := filepath.Join(t.TempDir(), "store")
			root := NewRoot("test-version")
			root.SetArgs(append([]string{"--cwd", t.TempDir(), "--store", storeRoot}, args...))
			err := root.Execute()
			if err == nil || ExitCode(err) != ExitUsage {
				t.Fatalf("ctx %s error = %v, exit %d", strings.Join(args, " "), err, ExitCode(err))
			}
			if _, err := os.Stat(storeRoot); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("ctx %s mutated the store before remote validation: %v", strings.Join(args, " "), err)
			}
		})
	}
}

func TestResumeRejectsNegativeOutputBudget(t *testing.T) {
	root := NewRoot("test-version")
	root.SetArgs([]string{"resume", "--max-bytes", "-1"})
	err := root.Execute()
	if err == nil {
		t.Fatal("negative --max-bytes succeeded")
	}
	if got := ExitCode(err); got != ExitUsage {
		t.Fatalf("negative --max-bytes exit = %d (%v), want %d", got, err, ExitUsage)
	}
}

func TestMissingRemotePullAndResumeSyncUseSyncExitCode(t *testing.T) {
	repository := t.TempDir()
	command := exec.Command("git", "init", "-q")
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	storeRoot := t.TempDir()
	missingRemote := filepath.Join(t.TempDir(), "missing-remote")
	cases := [][]string{
		{"--cwd", repository, "--store", storeRoot, "sync", "--remote", missingRemote, "--direction", "pull"},
		{"--cwd", repository, "--store", storeRoot, "resume", "--sync", "--remote", missingRemote},
	}
	for _, args := range cases {
		root := NewRoot("test-version")
		root.SetArgs(args)
		err := root.Execute()
		if err == nil {
			t.Fatalf("ctx %s succeeded with a missing remote", strings.Join(args, " "))
		}
		if got := ExitCode(err); got != ExitSync {
			t.Fatalf("ctx %s exit = %d (%v), want %d", strings.Join(args, " "), got, err, ExitSync)
		}
	}
}

func TestSyncPullCLIFailsWhenRemoteRepoYAMLIsMissingBeforeCanonicalMutation(t *testing.T) {
	repository := t.TempDir()
	command := exec.Command("git", "init", "-q")
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	storeRoot := t.TempDir()
	sidecar := store.New(storeRoot)
	service := app.New(sidecar, app.Config{Client: "ctx.cli", DeviceID: "test-device"})
	resolved, err := service.Resolve(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), repository, "Local canonical task", nil)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{
		filepath.Join(sidecar.RepoDir(resolved.RepoID), "repo.yaml"),
		sidecar.ManifestPath(resolved.RepoID, task.TaskID),
	}
	before := make(map[string][]byte, len(paths))
	for _, path := range paths {
		before[path], err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	remoteRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(remoteRoot, "repos", resolved.RepoID, "tasks"), 0755); err != nil {
		t.Fatal(err)
	}

	root := NewRoot("test-version")
	root.SetArgs([]string{"--cwd", repository, "--store", storeRoot, "sync", "--remote", remoteRoot, "--direction", "pull"})
	err = root.Execute()
	if err == nil || !errors.Is(err, app.ErrSync) || ExitCode(err) != ExitSync || !strings.Contains(err.Error(), "repo.yaml") {
		t.Fatalf("missing remote repo.yaml error = %v, exit %d", err, ExitCode(err))
	}
	for _, path := range paths {
		after, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(after, before[path]) {
			t.Fatalf("failed pull changed %s: err=%v\nbefore=%s\nafter=%s", path, readErr, before[path], after)
		}
	}
}

func TestSyncCLIRecoveredManifestRejoinsDurableIdentity(t *testing.T) {
	repository := t.TempDir()
	command := exec.Command("git", "init", "-q")
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	durableRoot := t.TempDir()
	durableStore := store.New(durableRoot)
	durableService := app.New(durableStore, app.Config{Client: "ctx.cli", DeviceID: "durable-device"})
	resolved, err := durableService.Resolve(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	task, err := durableService.CreateTask(context.Background(), repository, "Durable CLI title", []string{"durable-cli-alias"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := durableService.Checkpoint(context.Background(), app.CheckpointRequest{CWD: repository, Input: commandCaptureInput(t, resolved.RepoID)}); err != nil {
		t.Fatal(err)
	}
	remoteRoot := t.TempDir()
	runSync := func(storeRoot, direction string) error {
		root := NewRoot("test-version")
		root.SetOut(&bytes.Buffer{})
		root.SetArgs([]string{"--cwd", repository, "--store", storeRoot, "sync", "--remote", remoteRoot, "--direction", direction})
		return root.Execute()
	}
	if err := runSync(durableRoot, "push"); err != nil {
		t.Fatal(err)
	}
	remoteManifest := filepath.Join(remoteRoot, "repos", resolved.RepoID, "tasks", task.TaskID, "manifest.yaml")
	if err := os.Remove(remoteManifest); err != nil {
		t.Fatal(err)
	}

	recoveredRoot := t.TempDir()
	if err := runSync(recoveredRoot, "pull"); err != nil {
		t.Fatal(err)
	}
	recoveredStore := store.New(recoveredRoot)
	recoveredPath := recoveredStore.ManifestPath(resolved.RepoID, task.TaskID)
	var provisional model.TaskManifest
	if err := recoveredStore.ReadYAML(recoveredPath, &provisional); err != nil {
		t.Fatal(err)
	}
	if !provisional.IdentityRecovered || len(provisional.Aliases) != 0 {
		t.Fatalf("provisional CLI manifest = %#v", provisional)
	}

	if err := runSync(durableRoot, "push"); err != nil {
		t.Fatal(err)
	}
	if err := runSync(recoveredRoot, "pull"); err != nil {
		t.Fatalf("rejoin durable identity: %v", err)
	}
	var restored model.TaskManifest
	if err := recoveredStore.ReadYAML(recoveredPath, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.IdentityRecovered || restored.Title != "Durable CLI title" || len(restored.Aliases) != 1 || restored.Aliases[0] != "durable-cli-alias" {
		t.Fatalf("restored CLI manifest = %#v", restored)
	}
	root := NewRoot("test-version")
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"--cwd", repository, "--store", recoveredRoot, "resume", "--task", "durable-cli-alias"})
	if err := root.Execute(); err != nil {
		t.Fatalf("resume restored durable alias: %v", err)
	}
}

func TestSyncPullRejectsMissingManifestSelectorConflictBeforeCopy(t *testing.T) {
	repository := t.TempDir()
	command := exec.Command("git", "init", "-q")
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}

	remoteRoot := t.TempDir()
	remoteStore := store.New(remoteRoot)
	remoteService := app.New(remoteStore, app.Config{Client: "ctx.cli", DeviceID: "remote-device"})
	remoteResolved, err := remoteService.Resolve(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	remoteTask, err := remoteService.CreateTask(context.Background(), repository, "Remote source", nil)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := remoteService.Checkpoint(context.Background(), app.CheckpointRequest{
		CWD:   repository,
		Input: commandCaptureInput(t, remoteResolved.RepoID),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(remoteStore.ManifestPath(remoteResolved.RepoID, remoteTask.TaskID)); err != nil {
		t.Fatal(err)
	}

	localRoot := t.TempDir()
	localStore := store.New(localRoot)
	localService := app.New(localStore, app.Config{Client: "ctx.cli", DeviceID: "local-device"})
	localTask, err := localService.CreateTask(context.Background(), repository, "Local destination", []string{remoteTask.TaskID})
	if err != nil {
		t.Fatal(err)
	}

	paths := []string{
		filepath.Join(remoteStore.RepoDir(remoteResolved.RepoID), "repo.yaml"),
		remoteStore.CheckpointPath(remoteResolved.RepoID, remoteTask.TaskID, checkpoint.CheckpointID),
		filepath.Join(localStore.RepoDir(remoteResolved.RepoID), "repo.yaml"),
		localStore.ManifestPath(remoteResolved.RepoID, localTask.TaskID),
	}
	before := make(map[string][]byte, len(paths))
	for _, path := range paths {
		before[path], err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}

	root := NewRoot("test-version")
	root.SetArgs([]string{"--cwd", repository, "--store", localRoot, "sync", "--remote", remoteRoot, "--direction", "pull"})
	err = root.Execute()
	if err == nil || !errors.Is(err, app.ErrSync) || ExitCode(err) != ExitSync || !strings.Contains(err.Error(), "task selector") {
		t.Fatalf("ctx sync pull error = %v, exit %d", err, ExitCode(err))
	}
	for _, path := range paths {
		after, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(after, before[path]) {
			t.Fatalf("sync pull changed %s: err=%v\nbefore=%s\nafter=%s", path, readErr, before[path], after)
		}
	}
	if _, err := os.Stat(localStore.TaskDir(remoteResolved.RepoID, remoteTask.TaskID)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("sync pull copied remote task before rejecting selector conflict: %v", err)
	}
	if _, err := os.Stat(remoteStore.ManifestPath(remoteResolved.RepoID, remoteTask.TaskID)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("sync pull regenerated remote manifest before rejecting selector conflict: %v", err)
	}
}

func TestHandoffSyncWithoutRemoteFailsBeforeMutation(t *testing.T) {
	repository := t.TempDir()
	command := exec.Command("git", "init", "-q")
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	storeRoot := t.TempDir()
	create := NewRoot("test-version")
	create.SetOut(&bytes.Buffer{})
	create.SetArgs([]string{"--cwd", repository, "--store", storeRoot, "task", "create", "--title", "No partial handoff"})
	if err := create.Execute(); err != nil {
		t.Fatal(err)
	}
	sidecar := store.New(storeRoot)
	service := app.New(sidecar, app.Config{Client: "ctx.cli", DeviceID: "test-device"})
	resolved, err := service.Resolve(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := sidecar.ManifestPath(resolved.RepoID, resolved.TaskID)
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	handoff := NewRoot("test-version")
	handoff.SetArgs([]string{"--cwd", repository, "--store", storeRoot, "handoff", "--sync", "--input", filepath.Join(t.TempDir(), "unused.json")})
	err = handoff.Execute()
	if err == nil || ExitCode(err) != ExitUsage {
		t.Fatalf("handoff --sync without remote error = %v, exit %d", err, ExitCode(err))
	}
	after, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("manifest changed after preflight failure:\n%s", after)
	}
	if entries, err := os.ReadDir(filepath.Dir(sidecar.CheckpointPath(resolved.RepoID, resolved.TaskID, "placeholder"))); err == nil && len(entries) != 0 {
		t.Fatalf("preflight failure wrote checkpoints: %#v", entries)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := os.Stat(sidecar.HandoffPath(resolved.RepoID, resolved.TaskID)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("preflight failure wrote handoff pointer: %v", err)
	}
}

func TestHandoffMalformedRemoteUsesSyncExitAfterLocalCreation(t *testing.T) {
	repository := t.TempDir()
	command := exec.Command("git", "init", "-q")
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	storeRoot := t.TempDir()
	sidecar := store.New(storeRoot)
	service := app.New(sidecar, app.Config{Client: "ctx.cli", DeviceID: "test-device"})
	resolved, err := service.Resolve(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), repository, "Split-state handoff", nil)
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(commandCaptureInput(t, resolved.RepoID))
	if err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(t.TempDir(), "capture.json")
	if err := os.WriteFile(inputPath, input, 0600); err != nil {
		t.Fatal(err)
	}
	remoteFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(remoteFile, []byte("not a ctx store"), 0600); err != nil {
		t.Fatal(err)
	}

	handoff := NewRoot("test-version")
	handoff.SetOut(&bytes.Buffer{})
	handoff.SetArgs([]string{
		"--cwd", repository,
		"--store", storeRoot,
		"handoff",
		"--sync",
		"--remote", remoteFile,
		"--input", inputPath,
	})
	err = handoff.Execute()
	if err == nil || !errors.Is(err, app.ErrSync) || ExitCode(err) != ExitSync {
		t.Fatalf("malformed remote handoff error = %v, exit %d; want sync failure", err, ExitCode(err))
	}
	records, err := sidecar.ListJSON(filepath.Dir(sidecar.CheckpointPath(resolved.RepoID, task.TaskID, "placeholder")))
	if err != nil || len(records) != 1 {
		t.Fatalf("local handoff checkpoints = %#v, err=%v", records, err)
	}
	if _, err := os.Stat(sidecar.HandoffPath(resolved.RepoID, task.TaskID)); err != nil {
		t.Fatalf("local handoff pointer was not created: %v", err)
	}
}

func TestRootHelp(t *testing.T) {
	var output bytes.Buffer
	root := NewRoot("test-version")
	root.SetOut(&output)
	root.SetArgs([]string{"--help"})

	if err := root.Execute(); err != nil {
		t.Fatalf("execute --help: %v", err)
	}
	if !strings.Contains(output.String(), "Carry development context across coding agents") {
		t.Fatalf("help output does not contain the command summary: %q", output.String())
	}
	if strings.Contains(output.String(), "completion") {
		t.Fatalf("help exposes Cobra's unused completion command: %q", output.String())
	}
	if !strings.Contains(output.String(), "repo") {
		t.Fatalf("help does not expose repository identity commands: %q", output.String())
	}

	output.Reset()
	root = NewRoot("test-version")
	root.SetOut(&output)
	root.SetArgs([]string{"repo", "link", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute repo link --help: %v", err)
	}
	if !strings.Contains(output.String(), "--from") {
		t.Fatalf("repo link help does not document --from: %q", output.String())
	}
}

func commandCaptureInput(t *testing.T, repoID string) app.Record {
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
