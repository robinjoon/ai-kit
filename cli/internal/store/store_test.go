package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestWriteImmutableJSONIsIdempotentAndDetectsConflict(t *testing.T) {
	store := New(t.TempDir())
	path := store.CheckpointPath("repo-one", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW")
	record := map[string]any{"record_type": "ctx.checkpoint", "value": "first"}

	created, err := store.WriteImmutableJSON(path, record)
	if err != nil || !created {
		t.Fatalf("first immutable write = (%v, %v), want (true, nil)", created, err)
	}
	created, err = store.WriteImmutableJSON(path, record)
	if err != nil || created {
		t.Fatalf("same immutable write = (%v, %v), want (false, nil)", created, err)
	}
	created, err = store.WriteImmutableJSON(path, map[string]any{"record_type": "ctx.checkpoint", "value": "second"})
	if created || !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("conflicting immutable write = (%v, %v), want conflict", created, err)
	}

	stored, err := store.ReadJSON(path)
	if err != nil {
		t.Fatalf("read immutable JSON: %v", err)
	}
	if got, want := stored["value"], "first"; got != want {
		t.Fatalf("stored value = %q, want %q", got, want)
	}
}

func TestAtomicWriteReplacesDerivedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "manifest.yaml")
	if err := AtomicWrite(path, []byte("first\n"), 0o600); err != nil {
		t.Fatalf("first atomic write: %v", err)
	}
	if err := AtomicWrite(path, []byte("second\n"), 0o600); err != nil {
		t.Fatalf("second atomic write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read derived file: %v", err)
	}
	if got, want := string(data), "second\n"; got != want {
		t.Fatalf("derived file = %q, want %q", got, want)
	}
}

func TestReadAndListJSONRejectDuplicateKeys(t *testing.T) {
	store := New(t.TempDir())
	directory := store.CheckpointsDir("repo-one", "task-one")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("make checkpoint directory: %v", err)
	}
	path := filepath.Join(directory, "duplicate.json")
	if err := os.WriteFile(path, []byte(`{"context":{"title":"first","title":"second"}}`), 0o600); err != nil {
		t.Fatalf("write duplicate JSON fixture: %v", err)
	}
	if _, err := store.ReadJSON(path); err == nil {
		t.Fatal("expected ReadJSON to reject duplicate key")
	}
	if _, err := store.ListJSON(directory); err == nil {
		t.Fatal("expected ListJSON to reject duplicate key")
	}
}

func TestWithTaskLockSerializesAccess(t *testing.T) {
	store := New(t.TempDir())
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- store.WithTaskLock("repo-one", "task-one", func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	secondEntered := make(chan struct{})
	secondFinished := make(chan error, 1)
	go func() {
		secondFinished <- store.WithTaskLock("repo-one", "task-one", func() error {
			close(secondEntered)
			return nil
		})
	}()
	select {
	case <-secondEntered:
		t.Fatal("second lock entered before first released")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatalf("first lock: %v", err)
	}
	if err := <-secondFinished; err != nil {
		t.Fatalf("second lock: %v", err)
	}
	select {
	case <-secondEntered:
	case <-time.After(time.Second):
		t.Fatal("second lock did not enter")
	}
}

func TestWithTaskLockReentersAfterOwnerCloses(t *testing.T) {
	store := New(t.TempDir())
	directory := store.TaskDir("repo-one", "task-one")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("make task directory: %v", err)
	}
	owner, err := os.OpenFile(filepath.Join(directory, ".lock"), os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	if err := unix.Flock(int(owner.Fd()), unix.LOCK_EX); err != nil {
		t.Fatalf("lock owner: %v", err)
	}
	// Closing without an explicit unlock simulates a process exiting abruptly.
	if err := owner.Close(); err != nil {
		t.Fatalf("close lock owner: %v", err)
	}
	if err := store.WithTaskLock("repo-one", "task-one", func() error { return nil }); err != nil {
		t.Fatalf("re-enter after closed owner: %v", err)
	}
}

func TestRepositoryTransitionLockWaitsForSharedUseCase(t *testing.T) {
	store := New(t.TempDir())
	sharedEntered := make(chan struct{})
	releaseShared := make(chan struct{})
	sharedFinished := make(chan error, 1)
	go func() {
		sharedFinished <- store.WithRepositoryLock("local-11111111111111111111", func() error {
			close(sharedEntered)
			<-releaseShared
			return nil
		})
	}()
	<-sharedEntered

	transitionEntered := make(chan struct{})
	transitionFinished := make(chan error, 1)
	go func() {
		transitionFinished <- store.WithRepositoryTransitionLock("local-11111111111111111111", func() error {
			close(transitionEntered)
			return nil
		})
	}()
	select {
	case <-transitionEntered:
		t.Fatal("repository transition entered while a shared use-case was active")
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseShared)
	if err := <-sharedFinished; err != nil {
		t.Fatalf("shared repository lock: %v", err)
	}
	if err := <-transitionFinished; err != nil {
		t.Fatalf("repository transition lock: %v", err)
	}
	select {
	case <-transitionEntered:
	case <-time.After(time.Second):
		t.Fatal("repository transition did not enter after shared use-case released")
	}
}
