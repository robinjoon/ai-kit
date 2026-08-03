// Package store manages ctx's local, file-based sidecar store.
package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/robinjoon/ai-kit/cli/internal/canonical"
	"go.yaml.in/yaml/v3"
	"golang.org/x/sys/unix"
)

var (
	ErrNotFound          = errors.New("ctx record not found")
	ErrImmutableConflict = errors.New("immutable record conflict")
	ErrLockTimeout       = errors.New("timed out waiting for ctx lock")
)

// Store is rooted at the ctx sidecar directory. Its zero value is not usable;
// create it with New or DefaultRoot.
type Store struct{ root string }

func New(root string) Store { return Store{root: filepath.Clean(root)} }

// DefaultRoot returns the platform user configuration directory specified by
// ctx v1 on macOS (~/Library/Application Support/ctx).
func DefaultRoot() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ctx"), nil
}

func (s Store) Root() string { return s.root }

func (s Store) ConfigPath() string { return filepath.Join(s.root, "config.yaml") }

func (s Store) ReposDir() string { return filepath.Join(s.root, "repos") }

func (s Store) RepoDir(repoID string) string {
	return filepath.Join(s.ReposDir(), component(repoID))
}

func (s Store) RepoPath(repoID string) string {
	return filepath.Join(s.RepoDir(repoID), "repo.yaml")
}

func (s Store) TasksDir(repoID string) string {
	return filepath.Join(s.RepoDir(repoID), "tasks")
}

func (s Store) TaskDir(repoID, taskID string) string {
	return filepath.Join(s.TasksDir(repoID), component(taskID))
}

func (s Store) ManifestPath(repoID, taskID string) string {
	return filepath.Join(s.TaskDir(repoID, taskID), "manifest.yaml")
}

func (s Store) HandoffPath(repoID, taskID string) string {
	return filepath.Join(s.TaskDir(repoID, taskID), "handoff.md")
}

func (s Store) CheckpointsDir(repoID, taskID string) string {
	return filepath.Join(s.TaskDir(repoID, taskID), "checkpoints")
}

func (s Store) CheckpointPath(repoID, taskID, checkpointID string) string {
	return filepath.Join(s.CheckpointsDir(repoID, taskID), component(checkpointID)+".json")
}

func (s Store) SnapshotsDir(repoID, taskID string) string {
	return filepath.Join(s.TaskDir(repoID, taskID), "runtime", "snapshots")
}

func (s Store) SnapshotPath(repoID, taskID, snapshotID string) string {
	return filepath.Join(s.SnapshotsDir(repoID, taskID), component(snapshotID)+".json")
}

func (s Store) BindingsDir(repoID, taskID string) string {
	return filepath.Join(s.TaskDir(repoID, taskID), "runtime", "bindings")
}

func (s Store) BindingPath(repoID, taskID, client string) string {
	return filepath.Join(s.BindingsDir(repoID, taskID), bindingName(client)+".yaml")
}

func (s Store) RepositoryLockPath(repositoryKey string) string {
	return filepath.Join(s.root, "locks", "repositories", component(repositoryKey)+".lock")
}

// AtomicWrite replaces a derived file only after a complete sibling temporary
// file has been synced and closed.
func AtomicWrite(path string, data []byte, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(perm); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

// AtomicWrite is the Store form of AtomicWrite for application-layer storage
// interfaces. The path must be one of this Store's derived-file paths.
func (s Store) AtomicWrite(path string, data []byte, perm fs.FileMode) error {
	return AtomicWrite(path, data, perm)
}

// WriteImmutableJSON writes RFC 8785 JSON exactly once. Repeating the same
// write succeeds without replacing the file; a different body for the same
// path is an explicit store-corruption error.
func (s Store) WriteImmutableJSON(path string, record any) (bool, error) {
	data, err := canonical.JSON(record)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return false, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return false, err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return false, err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return false, err
	}
	if err := temporary.Close(); err != nil {
		return false, err
	}
	if err := os.Link(temporaryPath, path); err == nil {
		return true, nil
	} else if !errors.Is(err, fs.ErrExist) {
		return false, err
	}
	existing, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	if bytes.Equal(existing, data) {
		return false, nil
	}
	return false, fmt.Errorf("%w: %s", ErrImmutableConflict, path)
}

// ReadJSON decodes one canonical record object.
func (s Store) ReadJSON(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return nil, err
	}
	return canonical.DecodeObject(data)
}

// ListJSON loads regular .json files in lexical file-name order.
func (s Store) ListJSON(directory string) ([]map[string]any, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, directory)
		}
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".json") {
			paths = append(paths, filepath.Join(directory, entry.Name()))
		}
	}
	sort.Strings(paths)
	records := make([]map[string]any, 0, len(paths))
	for _, path := range paths {
		record, err := s.ReadJSON(path)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func (s Store) WriteYAML(path string, value any) error {
	data, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	return AtomicWrite(path, data, 0o600)
}

func (s Store) ReadYAML(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return err
	}
	return yaml.Unmarshal(data, value)
}

// WithTaskLock serializes updates to one task's derived records. The immutable
// writer is independently safe for a same-file race.
func (s Store) WithTaskLock(repoID, taskID string, fn func() error) error {
	directory := s.TaskDir(repoID, taskID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	return withFileLock(filepath.Join(directory, ".lock"), unix.LOCK_EX, fn)
}

// WithRepositoryLock holds a shared lock for one repository identity key.
func (s Store) WithRepositoryLock(repositoryKey string, fn func() error) error {
	return withFileLock(s.RepositoryLockPath(repositoryKey), unix.LOCK_SH, fn)
}

func (s Store) WithRepositoryTransitionLock(repositoryKey string, fn func() error) error {
	return withFileLock(s.RepositoryLockPath(repositoryKey), unix.LOCK_EX, fn)
}

func withFileLock(lockPath string, operation int, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := unix.Flock(int(file.Fd()), operation|unix.LOCK_NB)
		if err == nil {
			defer unix.Flock(int(file.Fd()), unix.LOCK_UN)
			return fn()
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return err
		}
		if time.Now().After(deadline) {
			return ErrLockTimeout
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func component(value string) string {
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, `/\\`) {
		panic("ctx store path component is invalid")
	}
	return value
}

func bindingName(client string) string {
	sum := sha256.Sum256([]byte(client))
	return "binding-" + hex.EncodeToString(sum[:16])
}
