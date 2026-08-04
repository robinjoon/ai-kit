package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/robinjoon/ai-kit/cli/internal/canonical"
	"github.com/robinjoon/ai-kit/cli/internal/gitobs"
	"github.com/robinjoon/ai-kit/cli/internal/model"
	"github.com/robinjoon/ai-kit/cli/internal/render"
	"github.com/robinjoon/ai-kit/cli/internal/schema"
	"github.com/robinjoon/ai-kit/cli/internal/store"
	"go.yaml.in/yaml/v3"
)

var (
	ErrStore = errors.New("ctx store failure")
	ErrGit   = errors.New("ctx Git observation failed")
	ErrSync  = errors.New("ctx sync failed")
)

// Storage is the small subset of the local sidecar store used by the
// application layer. The concrete filesystem store satisfies this interface;
// keeping it here makes the policy testable without a real home directory.
type Storage interface {
	Root() string
	RepoDir(repoID string) string
	TaskDir(repoID, taskID string) string
	CheckpointPath(repoID, taskID, id string) string
	SnapshotPath(repoID, taskID, id string) string
	HandoffPath(repoID, taskID string) string
	ManifestPath(repoID, taskID string) string
	BindingPath(repoID, taskID, client string) string
	WriteImmutableJSON(path string, record any) (bool, error)
	ReadJSON(path string) (map[string]any, error)
	ListJSON(dir string) ([]map[string]any, error)
	WriteYAML(path string, value any) error
	ReadYAML(path string, value any) error
	WithRepositoryLock(repositoryKey string, fn func() error) error
	WithRepositoryTransitionLock(repositoryKey string, fn func() error) error
	WithTaskLock(repoID, taskID string, fn func() error) error
}

// Service owns the command use-cases. It only observes Git; none of its
// methods invoke a Git mutation command.
type Service struct {
	store       Storage
	client      string
	sessionID   string
	deviceID    string
	version     string
	now         func() time.Time
	nextID      func(time.Time) (string, error)
	observe     func(string) (gitobs.Observation, error)
	atomicWrite func(string, []byte, fs.FileMode) error
	// afterRepositoryLock is a deterministic concurrency-test hook. Production
	// services leave it nil.
	afterRepositoryLock        func()
	afterTaskSelectorPreflight func()
	afterSyncPreflight         func()
}

type Config struct {
	Client    string
	SessionID string
	DeviceID  string
	Version   string
}

func New(sidecar Storage, config Config) *Service {
	client := config.Client
	if client == "" {
		client = "ctx.cli"
	}
	deviceID := config.DeviceID
	if deviceID == "" {
		deviceID, _ = os.Hostname()
		if deviceID == "" {
			deviceID = "local-device"
		}
	}
	return &Service{store: sidecar, client: client, sessionID: config.SessionID, deviceID: deviceID, version: config.Version, now: time.Now, nextID: newULID, observe: gitobs.Observe, atomicWrite: store.AtomicWrite}
}

type Resolved struct {
	RepoID          string `json:"repo_id"`
	WorkingCopyID   string `json:"working_copy_id"`
	Root            string `json:"root"`
	CanonicalRemote string `json:"canonical_remote,omitempty"`
	TaskID          string `json:"task_id,omitempty"`
	Client          string `json:"client"`
	SessionID       string `json:"session_id,omitempty"`
}

type RepoLinkResult struct {
	RepoID     string   `json:"repo_id"`
	LinkedFrom string   `json:"linked_from"`
	TaskIDs    []string `json:"task_ids"`
}

const unboundRuntimeID = "unbound"

type unboundSnapshotCopy struct {
	destination string
	data        []byte
}

type TaskSummary struct {
	TaskID        string            `json:"task_id"`
	Title         string            `json:"title"`
	Status        string            `json:"status"`
	Aliases       []string          `json:"aliases,omitempty"`
	HeadIDs       []string          `json:"head_ids,omitempty"`
	StableHeadIDs []string          `json:"stable_head_ids,omitempty"`
	StableHeadID  string            `json:"stable_head_id,omitempty"`
	HeadStatuses  map[string]string `json:"head_statuses,omitempty"`
	LastUsedAt    string            `json:"last_used_at,omitempty"`
}

type CheckpointRequest struct {
	CWD     string
	Purpose string
	Parents []string
	Input   Record
	TaskID  string
	// HandoffTarget is preserved in an immutable handoff checkpoint so a
	// derived handoff.md can be regenerated without losing its recipient.
	HandoffTarget Record
}

type CheckpointResult struct {
	CheckpointID  string `json:"checkpoint_id"`
	ContentDigest string `json:"content_digest"`
	Stability     string `json:"stability"`
	Deduplicated  bool   `json:"deduplicated"`
}

type SnapshotResult struct {
	SnapshotID string `json:"snapshot_id"`
	TaskID     string `json:"task_id,omitempty"`
}

type HandoffResult struct {
	CheckpointResult
	HandoffID string `json:"handoff_id"`
	Path      string `json:"path"`
}

type ResumeResult struct {
	TaskID       string `json:"task_id"`
	CheckpointID string `json:"checkpoint_id"`
	Content      []byte `json:"-"`
	Checkpoint   Record `json:"checkpoint,omitempty"`
}

// observeCurrent preserves a local repository's identity after a remote is
// first added. A portable repository alias records an explicit link and makes
// subsequent observations use the raw Git-derived portable ID.
func (s *Service) observeCurrent(cwd string) (gitobs.Observation, error) {
	observation, err := s.observe(cwd)
	if err != nil {
		return gitobs.Observation{}, err
	}
	if !strings.HasPrefix(observation.Repository.RepoID, "repo-") {
		return observation, nil
	}
	var portable model.Repository
	portablePath := filepath.Join(s.store.RepoDir(observation.Repository.RepoID), "repo.yaml")
	if err := s.store.ReadYAML(portablePath, &portable); err == nil {
		if err := validateRepositoryMetadataVersion(portable, portablePath); err != nil {
			return gitobs.Observation{}, err
		}
	} else if !isNotFound(err) {
		return gitobs.Observation{}, storeError(err)
	}
	if localRepoID := observation.Repository.LocalRepoID; localRepoID != "" {
		if contains(portable.Aliases, localRepoID) {
			return observation, nil
		}
		var local model.Repository
		localPath := filepath.Join(s.store.RepoDir(localRepoID), "repo.yaml")
		if err := s.store.ReadYAML(localPath, &local); err == nil {
			if err := validateRepositoryMetadataVersion(local, localPath); err != nil {
				return gitobs.Observation{}, err
			}
			observation.Repository.RepoID = localRepoID
			return observation, nil
		} else if isNotFound(err) {
			return observation, nil
		} else {
			return gitobs.Observation{}, storeError(err)
		}
	}
	repositoriesDir := filepath.Dir(s.store.RepoDir("placeholder"))
	entries, err := os.ReadDir(repositoriesDir)
	if errors.Is(err, fs.ErrNotExist) {
		return observation, nil
	}
	if err != nil {
		return gitobs.Observation{}, storeError(err)
	}
	var matches []string
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "local-") {
			continue
		}
		var repository model.Repository
		if err := s.store.ReadYAML(filepath.Join(s.store.RepoDir(entry.Name()), "repo.yaml"), &repository); err != nil {
			if isNotFound(err) {
				continue
			}
			return gitobs.Observation{}, storeError(err)
		}
		if err := validateRepositoryMetadataVersion(repository, filepath.Join(s.store.RepoDir(entry.Name()), "repo.yaml")); err != nil {
			return gitobs.Observation{}, err
		}
		if repository.WorkingCopies[observation.Repository.WorkingCopyID] == observation.Repository.Root || containsMapValue(repository.WorkingCopies, observation.Repository.Root) {
			matches = append(matches, entry.Name())
		}
	}
	sort.Strings(matches)
	unlinkedMatches := matches[:0]
	for _, match := range matches {
		if !contains(portable.Aliases, match) {
			unlinkedMatches = append(unlinkedMatches, match)
		}
	}
	switch len(unlinkedMatches) {
	case 0:
		return observation, nil
	case 1:
		observation.Repository.RepoID = unlinkedMatches[0]
		return observation, nil
	default:
		return gitobs.Observation{}, fmt.Errorf("%w: working copy is mapped to local repositories %v", ErrAmbiguous, unlinkedMatches)
	}
}

func stableLocalRepositoryID(observation gitobs.Observation) string {
	if observation.Repository.LocalRepoID != "" {
		return observation.Repository.LocalRepoID
	}
	return observation.Repository.RepoID
}

type repositoryIdentityLockOptions struct {
	exclusive      bool
	rawObservation bool
}

func repositoryLockKeys(observation gitobs.Observation) []string {
	keys := uniqueStrings([]string{stableLocalRepositoryID(observation), observation.Repository.RepoID})
	sort.Strings(keys)
	return keys
}

func (s *Service) withRepositoryLocks(keys []string, exclusive bool, fn func() error) error {
	if len(keys) == 0 {
		return fn()
	}
	locked := func() error { return s.withRepositoryLocks(keys[1:], exclusive, fn) }
	if exclusive {
		return s.store.WithRepositoryTransitionLock(keys[0], locked)
	}
	return s.store.WithRepositoryLock(keys[0], locked)
}

func (s *Service) withRepositoryIdentityLock(cwd string, fn func(gitobs.Observation) error) error {
	return s.withRepositoryIdentityLocks(cwd, repositoryIdentityLockOptions{}, fn)
}

func (s *Service) withRepositoryIdentityLocks(cwd string, options repositoryIdentityLockOptions, fn func(gitobs.Observation) error) error {
	initial, err := s.observe(cwd)
	if err != nil {
		return observationError(err)
	}
	lockKeys := repositoryLockKeys(initial)
	if len(lockKeys) == 0 {
		return fmt.Errorf("%w: Git observation has no stable local repository identity", ErrGit)
	}
	locked := func() error {
		if s.afterRepositoryLock != nil {
			s.afterRepositoryLock()
		}
		var current gitobs.Observation
		var err error
		if options.rawObservation {
			current, err = s.observe(cwd)
		} else {
			current, err = s.observeCurrent(cwd)
		}
		if err != nil {
			return observationError(err)
		}
		initialLocalRepoID := stableLocalRepositoryID(initial)
		if currentLocalRepoID := stableLocalRepositoryID(current); currentLocalRepoID != initialLocalRepoID {
			return fmt.Errorf("%w: repository identity changed from %q to %q while acquiring its lock", ErrGit, initialLocalRepoID, currentLocalRepoID)
		}
		return fn(current)
	}
	err = s.withRepositoryLocks(lockKeys, options.exclusive, locked)
	return storeError(err)
}

func (s *Service) resolveFromObservation(observation gitobs.Observation) (Resolved, error) {
	result := Resolved{RepoID: observation.Repository.RepoID, WorkingCopyID: observation.Repository.WorkingCopyID, Root: observation.Repository.Root, CanonicalRemote: observation.Repository.CanonicalRemote, Client: s.client, SessionID: s.sessionID}
	if err := s.ensureRepository(result); err != nil {
		return Resolved{}, err
	}
	binding, err := s.readBinding(result.RepoID)
	if err == nil {
		result.TaskID = binding.TaskID
	} else if !isNotFound(err) {
		return Resolved{}, err
	}
	return result, nil
}

func (s *Service) Resolve(_ context.Context, cwd string) (Resolved, error) {
	var result Resolved
	err := s.withRepositoryIdentityLock(cwd, func(observation gitobs.Observation) error {
		var err error
		result, err = s.resolveFromObservation(observation)
		return err
	})
	return result, err
}

// LinkRepository migrates one local repository identity to the current
// Git-remote-derived identity without rewriting any task record bytes.
func (s *Service) LinkRepository(_ context.Context, cwd, fromRepoID string) (RepoLinkResult, error) {
	if !validLocalRepositoryID(fromRepoID) {
		return RepoLinkResult{}, fmt.Errorf("%w: --from must be a local repository ID", ErrValidation)
	}
	var result RepoLinkResult
	err := s.withRepositoryIdentityLocks(cwd, repositoryIdentityLockOptions{exclusive: true, rawObservation: true}, func(observation gitobs.Observation) error {
		var err error
		result, err = s.linkRepositoryFromObservation(observation, fromRepoID)
		return err
	})
	return result, err
}

func (s *Service) linkRepositoryFromObservation(observation gitobs.Observation, fromRepoID string) (RepoLinkResult, error) {
	currentRepoID := observation.Repository.RepoID
	if observation.Repository.CanonicalRemote == "" || !strings.HasPrefix(currentRepoID, "repo-") {
		return RepoLinkResult{}, fmt.Errorf("%w: current repository has no portable Git remote identity", ErrValidation)
	}
	if currentRepoID == fromRepoID {
		return RepoLinkResult{}, fmt.Errorf("%w: source and destination repository IDs are identical", ErrValidation)
	}
	source, err := s.readRepository(fromRepoID)
	if err != nil {
		return RepoLinkResult{}, err
	}
	if source.RepoID != fromRepoID {
		return RepoLinkResult{}, fmt.Errorf("%w: source repository metadata does not match %q", ErrValidation, fromRepoID)
	}
	if fromRepoID != observation.Repository.LocalRepoID && source.WorkingCopies[observation.Repository.WorkingCopyID] != observation.Repository.Root && !containsMapValue(source.WorkingCopies, observation.Repository.Root) {
		return RepoLinkResult{}, fmt.Errorf("%w: source repository is not mapped to the current working copy", ErrValidation)
	}

	destination, err := s.readRepository(currentRepoID)
	if isNotFound(err) {
		destination = model.Repository{SchemaVersion: model.SchemaVersion, RepoID: currentRepoID}
	} else if err != nil {
		return RepoLinkResult{}, err
	}
	if destination.RepoID != "" && destination.RepoID != currentRepoID {
		return RepoLinkResult{}, fmt.Errorf("%w: destination repository metadata conflicts with %q", ErrValidation, currentRepoID)
	}
	if destination.CanonicalRemote != "" && destination.CanonicalRemote != observation.Repository.CanonicalRemote {
		return RepoLinkResult{}, fmt.Errorf("%w: destination canonical remote conflicts with the current repository", ErrValidation)
	}
	taskIDs, err := s.repositoryTaskIDs(fromRepoID)
	if err != nil {
		return RepoLinkResult{}, err
	}
	checkpointGraphs := make(map[string][]Record)
	for _, taskID := range taskIDs {
		records, exists, err := validatedCheckpointGraphFromTaskDirectory(s.store.TaskDir(fromRepoID, taskID), taskID)
		if err != nil {
			return RepoLinkResult{}, fmt.Errorf("%w: source task metadata: %v", ErrValidation, err)
		}
		if exists {
			checkpointGraphs[taskID] = records
		}
	}
	sourceManifests, err := taskManifestsFromRepository(s.store.RepoDir(fromRepoID))
	if err != nil {
		return RepoLinkResult{}, fmt.Errorf("%w: source task metadata: %v", ErrValidation, err)
	}
	destinationManifests, err := taskManifestsFromRepository(s.store.RepoDir(currentRepoID))
	if err != nil {
		return RepoLinkResult{}, fmt.Errorf("%w: destination task metadata: %v", ErrValidation, err)
	}
	if err := validateTaskSelectorUnion(sourceManifests, destinationManifests); err != nil {
		return RepoLinkResult{}, fmt.Errorf("%w: %v", ErrValidation, err)
	}
	recoveredManifests := make(map[string][]byte)
	for _, manifest := range sourceManifests {
		_, exists, err := readManifestFile(s.store.ManifestPath(fromRepoID, manifest.TaskID))
		if err != nil {
			return RepoLinkResult{}, storeError(err)
		}
		if exists {
			continue
		}
		records, exists := checkpointGraphs[manifest.TaskID]
		if !exists {
			continue
		}
		recovered := model.TaskManifest{SchemaVersion: model.SchemaVersion, TaskID: manifest.TaskID, IdentityRecovered: true}
		if err := applyRecoveredCheckpointMetadata(&recovered, records); err != nil {
			return RepoLinkResult{}, fmt.Errorf("%w: source task metadata: %v", ErrValidation, err)
		}
		data, err := yaml.Marshal(recovered)
		if err != nil {
			return RepoLinkResult{}, storeError(err)
		}
		recoveredManifests[manifest.TaskID] = data
	}
	unboundSnapshots, err := s.planUnboundSnapshotUnion(fromRepoID, currentRepoID)
	if err != nil {
		return RepoLinkResult{}, err
	}
	for _, taskID := range taskIDs {
		if _, err := os.Lstat(s.store.TaskDir(currentRepoID, taskID)); err == nil {
			return RepoLinkResult{}, fmt.Errorf("%w: destination already contains task %q", ErrValidation, taskID)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return RepoLinkResult{}, storeError(err)
		}
	}

	destination.SchemaVersion = model.SchemaVersion
	destination.RepoID = currentRepoID
	destination.CanonicalRemote = observation.Repository.CanonicalRemote
	if destination.WorkingCopies == nil {
		destination.WorkingCopies = map[string]string{}
	}
	for workingCopyID, root := range source.WorkingCopies {
		destination.WorkingCopies[workingCopyID] = root
	}
	destination.WorkingCopies[observation.Repository.WorkingCopyID] = observation.Repository.Root
	destination.Aliases = append(destination.Aliases, fromRepoID)
	destination.Aliases = append(destination.Aliases, source.Aliases...)
	destination.Aliases = sortedRepositoryAliases(destination.Aliases, currentRepoID)

	if err := os.MkdirAll(filepath.Dir(s.store.TaskDir(currentRepoID, "placeholder")), 0700); err != nil {
		return RepoLinkResult{}, storeError(err)
	}
	moved := make([]string, 0, len(taskIDs))
	createdSnapshots := make([]string, 0, len(unboundSnapshots))
	createdManifests := make([]string, 0, len(recoveredManifests))
	rollback := func() {
		for index := len(createdSnapshots) - 1; index >= 0; index-- {
			_ = os.Remove(createdSnapshots[index])
		}
		for index := len(createdManifests) - 1; index >= 0; index-- {
			_ = os.Remove(createdManifests[index])
		}
		for index := len(moved) - 1; index >= 0; index-- {
			taskID := moved[index]
			_ = os.Rename(s.store.TaskDir(currentRepoID, taskID), s.store.TaskDir(fromRepoID, taskID))
		}
	}
	for _, taskID := range taskIDs {
		if err := os.Rename(s.store.TaskDir(fromRepoID, taskID), s.store.TaskDir(currentRepoID, taskID)); err != nil {
			rollback()
			return RepoLinkResult{}, storeError(err)
		}
		moved = append(moved, taskID)
	}
	for taskID, data := range recoveredManifests {
		path := s.store.ManifestPath(currentRepoID, taskID)
		if err := s.atomicWrite(path, data, 0600); err != nil {
			rollback()
			return RepoLinkResult{}, storeError(err)
		}
		createdManifests = append(createdManifests, path)
	}
	for _, snapshot := range unboundSnapshots {
		created, err := writeImmutableBytes(snapshot.destination, snapshot.data, 0600)
		if err != nil {
			rollback()
			return RepoLinkResult{}, storeError(err)
		}
		if created {
			createdSnapshots = append(createdSnapshots, snapshot.destination)
		}
	}
	if err := s.store.WriteYAML(filepath.Join(s.store.RepoDir(currentRepoID), "repo.yaml"), destination); err != nil {
		rollback()
		return RepoLinkResult{}, storeError(err)
	}
	return RepoLinkResult{RepoID: currentRepoID, LinkedFrom: fromRepoID, TaskIDs: taskIDs}, nil
}

func (s *Service) planUnboundSnapshotUnion(sourceRepoID, destinationRepoID string) ([]unboundSnapshotCopy, error) {
	sourceDirectory := filepath.Dir(s.store.SnapshotPath(sourceRepoID, unboundRuntimeID, "placeholder"))
	entries, err := os.ReadDir(sourceDirectory)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, storeError(err)
	}
	destinationDirectory := filepath.Dir(s.store.SnapshotPath(destinationRepoID, unboundRuntimeID, "placeholder"))
	var copies []unboundSnapshotCopy
	for _, entry := range entries {
		if !entry.Type().IsRegular() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(sourceDirectory, entry.Name()))
		if err != nil {
			return nil, storeError(err)
		}
		destinationPath := filepath.Join(destinationDirectory, entry.Name())
		existing, err := os.ReadFile(destinationPath)
		if err == nil {
			if !bytes.Equal(existing, data) {
				return nil, fmt.Errorf("%w: immutable unbound snapshot conflict at %s", ErrValidation, destinationPath)
			}
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, storeError(err)
		}
		copies = append(copies, unboundSnapshotCopy{destination: destinationPath, data: data})
	}
	return copies, nil
}

func (s *Service) CreateTask(ctx context.Context, cwd, title string, aliases []string) (TaskSummary, error) {
	if strings.TrimSpace(title) == "" {
		return TaskSummary{}, fmt.Errorf("%w: --title is required", ErrValidation)
	}
	var result TaskSummary
	err := s.withRepositoryIdentityLocks(cwd, repositoryIdentityLockOptions{exclusive: true}, func(observation gitobs.Observation) error {
		resolved, err := s.resolveFromObservation(observation)
		if err != nil {
			return err
		}
		if err := s.ensureAliasesAvailable(resolved.RepoID, aliases, ""); err != nil {
			return err
		}
		id, err := s.nextID(s.now().UTC())
		if err != nil {
			return err
		}
		if err := s.ensureTaskIDAvailable(resolved.RepoID, id); err != nil {
			return err
		}
		if contains(aliases, id) {
			return fmt.Errorf("%w: task alias %q conflicts with the new task ID", ErrValidation, id)
		}
		if s.afterTaskSelectorPreflight != nil {
			s.afterTaskSelectorPreflight()
		}
		now := s.now().UTC()
		manifest := model.TaskManifest{SchemaVersion: model.SchemaVersion, TaskID: id, Title: title, Status: "active", Aliases: uniqueStrings(aliases), CreatedAt: now, LastUsedAt: now}
		if err := s.store.WriteYAML(s.store.ManifestPath(resolved.RepoID, id), manifest); err != nil {
			return storeError(err)
		}
		if err := s.writeBinding(resolved.RepoID, id); err != nil {
			return err
		}
		result = taskSummary(manifest)
		return nil
	})
	return result, err
}

func (s *Service) ListTasks(ctx context.Context, cwd string) ([]TaskSummary, error) {
	var result []TaskSummary
	err := s.withRepositoryIdentityLock(cwd, func(observation gitobs.Observation) error {
		resolved, err := s.resolveFromObservation(observation)
		if err != nil {
			return err
		}
		manifests, err := s.listManifests(resolved.RepoID)
		if err != nil {
			return err
		}
		result = make([]TaskSummary, 0, len(manifests))
		for _, manifest := range manifests {
			result = append(result, taskSummary(manifest))
		}
		sort.Slice(result, func(i, j int) bool { return result[i].LastUsedAt > result[j].LastUsedAt })
		return nil
	})
	return result, err
}

func (s *Service) SwitchTask(ctx context.Context, cwd, selector string) (TaskSummary, error) {
	if selector == "" {
		return TaskSummary{}, fmt.Errorf("%w: task ID or alias is required", ErrValidation)
	}
	var result TaskSummary
	err := s.withRepositoryIdentityLock(cwd, func(observation gitobs.Observation) error {
		resolved, err := s.resolveFromObservation(observation)
		if err != nil {
			return err
		}
		manifest, err := s.findTask(resolved.RepoID, selector)
		if err != nil {
			return err
		}
		manifest.LastUsedAt = s.now().UTC()
		if err := s.store.WriteYAML(s.store.ManifestPath(resolved.RepoID, manifest.TaskID), manifest); err != nil {
			return storeError(err)
		}
		if err := s.writeBinding(resolved.RepoID, manifest.TaskID); err != nil {
			return err
		}
		result = taskSummary(manifest)
		return nil
	})
	return result, err
}

func (s *Service) Snapshot(ctx context.Context, cwd, triggerKind, triggerName string) (SnapshotResult, error) {
	if triggerKind == "" {
		triggerKind = "manual"
	}
	if (triggerKind == "ctx-command" || triggerKind == "lifecycle-hook" || triggerKind == "other") && triggerName == "" {
		return SnapshotResult{}, fmt.Errorf("%w: --name is required for trigger %q", ErrValidation, triggerKind)
	}
	var result SnapshotResult
	err := s.withRepositoryIdentityLock(cwd, func(observation gitobs.Observation) error {
		resolved, err := s.resolveFromObservation(observation)
		if err != nil {
			return err
		}
		taskID := resolved.TaskID
		id, err := s.nextID(s.now().UTC())
		if err != nil {
			return err
		}
		record := s.snapshotRecord(id, taskID, triggerKind, triggerName, observation)
		if taskID != "" {
			if previous := s.latestSnapshot(observation.Repository.RepoID, taskID, observation.Repository.WorkingCopyID); len(previous) > 0 {
				if previousID, ok := previous["snapshot_id"].(string); ok {
					record["previous_snapshot_id"] = previousID
				}
			}
			if manifest, err := s.readManifest(observation.Repository.RepoID, taskID); err == nil && manifest.StableHeadID != "" {
				record["active_checkpoint_id"] = manifest.StableHeadID
			}
		}
		digest, err := canonical.ContentDigest(record)
		if err != nil {
			return err
		}
		record["content_digest"] = digest
		if err := schema.Validate(schema.RuntimeSnapshot, record); err != nil {
			return fmt.Errorf("%w: %v", ErrValidation, err)
		}
		if err := s.store.WithTaskLock(observation.Repository.RepoID, taskIDOrDevice(taskID), func() error {
			_, err := s.store.WriteImmutableJSON(s.store.SnapshotPath(observation.Repository.RepoID, taskIDOrDevice(taskID), id), record)
			return err
		}); err != nil {
			return storeError(err)
		}
		result = SnapshotResult{SnapshotID: id, TaskID: taskID}
		return nil
	})
	return result, err
}

func (s *Service) Checkpoint(ctx context.Context, request CheckpointRequest) (CheckpointResult, error) {
	if request.Purpose == "" {
		request.Purpose = "progress"
	}
	if !validPurpose(request.Purpose) {
		return CheckpointResult{}, fmt.Errorf("%w: unsupported checkpoint purpose %q", ErrValidation, request.Purpose)
	}
	if err := validateCaptureShape(request.Input); err != nil {
		return CheckpointResult{}, err
	}
	var result CheckpointResult
	err := s.withRepositoryIdentityLock(request.CWD, func(observation gitobs.Observation) error {
		taskID := request.TaskID
		var err error
		if taskID == "" {
			taskID, err = s.activeTaskID(observation.Repository.RepoID)
			if err != nil {
				return err
			}
		}
		result, err = s.checkpointFor(ctx, observation, taskID, request)
		return err
	})
	return result, err
}

func (s *Service) Handoff(ctx context.Context, request CheckpointRequest, target Record) (HandoffResult, error) {
	request.Purpose = "handoff"
	request.HandoffTarget = cloneRecord(target)
	if err := validateCaptureShape(request.Input); err != nil {
		return HandoffResult{}, err
	}
	if completeness(request.Input) != "complete" {
		return HandoffResult{}, fmt.Errorf("%w: handoff requires a complete capture", ErrValidation)
	}
	var result HandoffResult
	err := s.withRepositoryIdentityLock(request.CWD, func(observation gitobs.Observation) error {
		if observation.Snapshot.Worktree.State == "unknown" {
			return fmt.Errorf("%w: handoff requires a complete Git observation", ErrValidation)
		}
		taskID := request.TaskID
		var err error
		if taskID == "" {
			taskID, err = s.activeTaskID(observation.Repository.RepoID)
			if err != nil {
				return err
			}
		}
		return s.store.WithTaskLock(observation.Repository.RepoID, taskID, func() error {
			plan, err := s.prepareCheckpointLocked(observation, taskID, request)
			if err != nil {
				return err
			}
			stableHeads, err := StableCheckpointHeads(plan.records)
			if err != nil {
				return err
			}
			if len(stableHeads) != 1 {
				return fmt.Errorf("%w: handoff would leave multiple stable checkpoint heads; create a merge checkpoint first", ErrAmbiguous)
			}
			if stableHeads[0] != plan.result.CheckpointID {
				return fmt.Errorf("%w: handoff checkpoint would not be the current stable head; select a current head", ErrAmbiguous)
			}
			handoffID, content, err := s.prepareHandoffPointer(taskID, plan.result.CheckpointID, plan.result.ContentDigest, target)
			if err != nil {
				return err
			}
			handoffPath := s.store.HandoffPath(observation.Repository.RepoID, taskID)
			previous, previousErr := os.ReadFile(handoffPath)
			previousExists := previousErr == nil
			if previousErr != nil && !errors.Is(previousErr, fs.ErrNotExist) {
				return previousErr
			}
			// Publish the recoverable pointer first. Until the checkpoint exists it
			// is ignored by validHandoff; a pointer write failure therefore cannot
			// expose a new checkpoint or derived manifest state.
			if err := s.atomicWrite(handoffPath, content, 0644); err != nil {
				return err
			}
			if err := s.persistCheckpointPlan(observation.Repository.RepoID, taskID, plan); err != nil {
				rollbackErr := s.restoreHandoff(handoffPath, previous, previousExists)
				if rollbackErr != nil {
					return fmt.Errorf("persist handoff checkpoint: %w; restore handoff pointer: %v", err, rollbackErr)
				}
				return err
			}
			result = HandoffResult{CheckpointResult: plan.result, HandoffID: handoffID, Path: handoffPath}
			return nil
		})
	})
	return result, err
}

func (s *Service) checkpointFor(_ context.Context, observation gitobs.Observation, taskID string, request CheckpointRequest) (CheckpointResult, error) {
	var result CheckpointResult
	err := s.store.WithTaskLock(observation.Repository.RepoID, taskID, func() error {
		var err error
		result, err = s.checkpointLocked(observation, taskID, request)
		return err
	})
	if err != nil {
		return CheckpointResult{}, storeError(err)
	}
	return result, nil
}

type checkpointPlan struct {
	result   CheckpointResult
	manifest model.TaskManifest
	records  []Record
	record   Record
	new      bool
}

// checkpointLocked prepares and persists one checkpoint while its caller
// holds the task lock.
func (s *Service) checkpointLocked(observation gitobs.Observation, taskID string, request CheckpointRequest) (CheckpointResult, error) {
	plan, err := s.prepareCheckpointLocked(observation, taskID, request)
	if err != nil {
		return CheckpointResult{}, err
	}
	if err := s.persistCheckpointPlan(observation.Repository.RepoID, taskID, plan); err != nil {
		return CheckpointResult{}, err
	}
	return plan.result, nil
}

// prepareCheckpointLocked performs all validation and deduplication without
// mutating the sidecar. Handoff uses this to validate its pointer before any
// checkpoint or manifest becomes visible.
func (s *Service) prepareCheckpointLocked(observation gitobs.Observation, taskID string, request CheckpointRequest) (checkpointPlan, error) {
	manifest, err := s.readManifest(observation.Repository.RepoID, taskID)
	if err != nil {
		return checkpointPlan{}, err
	}
	records, err := s.listCheckpoints(observation.Repository.RepoID, taskID)
	if err != nil {
		return checkpointPlan{}, err
	}
	parents, err := DefaultParents(records, request.Purpose, request.Parents)
	if err != nil {
		return checkpointPlan{}, err
	}
	capture := cloneRecord(asRecord(request.Input["capture"]))
	if observation.Snapshot.Worktree.State == "unknown" {
		capture["completeness"] = "partial"
		capture["warnings"] = append(stringSliceAny(capture["warnings"]), observation.Snapshot.Worktree.Diagnostic)
	}
	stability := "stable"
	if completeness(Record{"capture": capture}) != "complete" {
		stability = "draft"
	}
	if request.Purpose == "handoff" && stability != "stable" {
		return checkpointPlan{}, fmt.Errorf("%w: handoff requires a stable checkpoint", ErrValidation)
	}
	id, err := s.nextID(s.now().UTC())
	if err != nil {
		return checkpointPlan{}, err
	}
	contextRecord := cloneRecord(asRecord(request.Input["context"]))
	contextDigest, err := canonical.Digest(contextRecord)
	if err != nil {
		return checkpointPlan{}, err
	}
	extensions := Record{}
	if request.Purpose == "handoff" && len(request.HandoffTarget) > 0 {
		extensions["io.github.robinjoon.ctx"] = Record{"handoff_target": cloneRecord(request.HandoffTarget)}
	}
	record := Record{
		"schema_version": model.SchemaVersion,
		"record_type":    "ctx.checkpoint",
		"checkpoint_id":  id,
		"task_id":        taskID,
		"parent_ids":     stringsAny(parents),
		"created_at":     timestamp(s.now()),
		"purpose":        request.Purpose,
		"stability":      stability,
		"work_status":    request.Input["work_status"],
		"capture":        capture,
		"producer":       s.producer(),
		"session_refs":   s.sessionRefs(),
		"context":        contextRecord,
		"context_digest": contextDigest,
		"workspace":      workspaceBaseline(observation, timestamp(s.now())),
		"extensions":     extensions,
	}
	contentDigest, err := canonical.ContentDigest(record)
	if err != nil {
		return checkpointPlan{}, err
	}
	record["content_digest"] = contentDigest
	if err := schema.Validate(schema.Checkpoint, record); err != nil {
		return checkpointPlan{}, fmt.Errorf("%w: %v", ErrValidation, err)
	}

	key, err := dedupeKey(record)
	if err != nil {
		return checkpointPlan{}, err
	}
	// An immediate implicit retry would otherwise make the just-created head
	// its parent and change the dedupe key. Compare the request against that
	// head using the head's original parents before creating another record.
	if len(request.Parents) == 0 && request.Purpose != "merge" {
		heads, headErr := CheckpointHeads(records)
		if headErr != nil {
			return checkpointPlan{}, headErr
		}
		if len(heads) == 1 {
			head, findErr := findCheckpoint(records, heads[0])
			if findErr != nil {
				return checkpointPlan{}, findErr
			}
			retryRecord := cloneRecord(record)
			retryRecord["parent_ids"] = stringsAny(stringSlice(head["parent_ids"]))
			retryKey, retryErr := dedupeKey(retryRecord)
			if retryErr != nil {
				return checkpointPlan{}, retryErr
			}
			headKey, retryErr := dedupeKey(head)
			if retryErr != nil {
				return checkpointPlan{}, retryErr
			}
			if retryKey == headKey {
				return checkpointPlan{result: checkpointResult(head), records: records}, nil
			}
		}
	}
	for _, existing := range records {
		existingKey, err := dedupeKey(existing)
		if err != nil {
			return checkpointPlan{}, err
		}
		if key == existingKey {
			return checkpointPlan{result: checkpointResult(existing), records: records}, nil
		}
	}
	return checkpointPlan{
		result:   CheckpointResult{CheckpointID: id, ContentDigest: contentDigest, Stability: stability},
		manifest: manifest,
		records:  append(records, record),
		record:   record,
		new:      true,
	}, nil
}

func checkpointResult(record Record) CheckpointResult {
	return CheckpointResult{CheckpointID: record["checkpoint_id"].(string), ContentDigest: record["content_digest"].(string), Stability: record["stability"].(string), Deduplicated: true}
}

func (s *Service) persistCheckpointPlan(repoID, taskID string, plan checkpointPlan) error {
	if !plan.new {
		return nil
	}
	path := s.store.CheckpointPath(repoID, taskID, plan.result.CheckpointID)
	created, err := s.store.WriteImmutableJSON(path, plan.record)
	if err != nil {
		return err
	}
	if !created {
		return fmt.Errorf("%w: checkpoint ID collision", ErrStore)
	}
	if err := s.updateManifest(repoID, plan.manifest, plan.records); err != nil {
		if rollbackErr := os.Remove(path); rollbackErr != nil && !errors.Is(rollbackErr, fs.ErrNotExist) {
			return fmt.Errorf("update manifest: %w; remove uncommitted checkpoint: %v", err, rollbackErr)
		}
		return err
	}
	return nil
}

func (s *Service) restoreHandoff(path string, previous []byte, existed bool) error {
	if existed {
		return s.atomicWrite(path, previous, 0644)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Service) prepareHandoffPointer(taskID, checkpointID, checkpointDigest string, target Record) (string, []byte, error) {
	handoffID, err := s.nextID(s.now().UTC())
	if err != nil {
		return "", nil, err
	}
	body := render.HandoffBody(taskID, checkpointID)
	record := Record{
		"schema_version":       model.SchemaVersion,
		"record_type":          "ctx.handoff",
		"handoff_id":           handoffID,
		"task_id":              taskID,
		"checkpoint_id":        checkpointID,
		"checkpoint_digest":    checkpointDigest,
		"generated_at":         timestamp(s.now()),
		"producer":             s.producer(),
		"render_profile":       "ctx-handoff-markdown-v1",
		"rendered_body_digest": render.Digest([]byte(body)),
		"extensions":           Record{},
	}
	if len(target) > 0 {
		record["target"] = target
	}
	if err := schema.Validate(schema.Handoff, record); err != nil {
		return "", nil, fmt.Errorf("%w: %v", ErrValidation, err)
	}
	content, err := render.RenderHandoff(record)
	if err != nil {
		return "", nil, err
	}
	return handoffID, content, nil
}

func (s *Service) Resume(ctx context.Context, cwd, taskSelector, checkpointID string, maxBytes int) (ResumeResult, error) {
	var result ResumeResult
	err := s.withRepositoryIdentityLock(cwd, func(observation gitobs.Observation) error {
		var err error
		result, err = s.resumeFromObservation(observation, taskSelector, checkpointID, maxBytes)
		return err
	})
	return result, err
}

func (s *Service) resumeFromObservation(observation gitobs.Observation, taskSelector, checkpointID string, maxBytes int) (ResumeResult, error) {
	resolved, err := s.resolveFromObservation(observation)
	if err != nil {
		return ResumeResult{}, err
	}
	taskID := taskSelector
	if taskID == "" {
		taskID = resolved.TaskID
	}
	if taskID == "" {
		taskID, err = s.findResumableTask(resolved.RepoID)
		if err != nil {
			return ResumeResult{}, err
		}
	}
	if taskSelector != "" {
		manifest, err := s.findTask(resolved.RepoID, taskSelector)
		if err != nil {
			return ResumeResult{}, err
		}
		taskID = manifest.TaskID
	}
	records, err := s.listCheckpoints(resolved.RepoID, taskID)
	if err != nil {
		return ResumeResult{}, err
	}
	var checkpoint Record
	if checkpointID != "" {
		checkpoint, err = findCheckpoint(records, checkpointID)
		if err != nil {
			return ResumeResult{}, err
		}
	} else {
		handoff, ok, handoffErr := s.ensureValidHandoff(resolved.RepoID, taskID, records)
		if handoffErr != nil {
			return ResumeResult{}, handoffErr
		}
		if ok {
			checkpoint, err = findCheckpoint(records, handoff)
		} else {
			checkpoint, err = selectStableHead(records)
		}
		if err != nil {
			return ResumeResult{}, err
		}
	}
	repositoryAliases, err := s.repositoryAliases(resolved.RepoID)
	if err != nil {
		return ResumeResult{}, err
	}
	content, err := render.Resume(checkpoint, compareWorkspace(checkpoint, observation, repositoryAliases...), maxBytes)
	if err != nil {
		return ResumeResult{}, err
	}
	if err := s.writeBinding(resolved.RepoID, taskID); err != nil {
		return ResumeResult{}, err
	}
	return ResumeResult{TaskID: taskID, CheckpointID: checkpoint["checkpoint_id"].(string), Content: content, Checkpoint: checkpoint}, nil
}

type StatusResult struct {
	Resolved              Resolved        `json:"resolved"`
	Task                  *TaskSummary    `json:"task,omitempty"`
	Heads                 []string        `json:"heads,omitempty"`
	CheckpointGraph       CheckpointGraph `json:"checkpoint_graph"`
	Git                   Record          `json:"git"`
	GitDifferences        []string        `json:"git_differences,omitempty"`
	GitComparisonNote     string          `json:"git_comparison_note,omitempty"`
	LatestRuntimeSnapshot Record          `json:"latest_runtime_snapshot,omitempty"`
	LastSync              *SyncState      `json:"last_sync,omitempty"`
}

type CheckpointGraph struct {
	Nodes []CheckpointGraphNode `json:"nodes"`
	Edges []CheckpointGraphEdge `json:"edges"`
}

type CheckpointGraphNode struct {
	CheckpointID string   `json:"checkpoint_id"`
	ParentIDs    []string `json:"parent_ids"`
	Purpose      string   `json:"purpose"`
	Stability    string   `json:"stability"`
	WorkStatus   string   `json:"work_status"`
}

type CheckpointGraphEdge struct {
	ParentID string `json:"parent_id"`
	ChildID  string `json:"child_id"`
}

// SyncState is local, derived diagnostic metadata. It is never copied by
// filesystem sync and must not be mistaken for a checkpoint record.
type SyncState struct {
	Remote    string `yaml:"remote" json:"remote"`
	Direction string `yaml:"direction" json:"direction"`
	Status    string `yaml:"status" json:"status"`
	At        string `yaml:"at" json:"at"`
	Message   string `yaml:"message,omitempty" json:"message,omitempty"`
}

func (s *Service) Status(ctx context.Context, cwd string) (StatusResult, error) {
	var result StatusResult
	err := s.withRepositoryIdentityLock(cwd, func(observation gitobs.Observation) error {
		var err error
		result, err = s.statusFromObservation(observation)
		return err
	})
	return result, err
}

func (s *Service) statusFromObservation(observation gitobs.Observation) (StatusResult, error) {
	resolved, err := s.resolveFromObservation(observation)
	if err != nil {
		return StatusResult{}, err
	}
	result := StatusResult{Resolved: resolved, CheckpointGraph: buildCheckpointGraph(nil), Git: gitState(observation.Snapshot)}
	result.LastSync = s.lastSync(resolved.RepoID)
	if resolved.TaskID == "" {
		return result, nil
	}
	manifest, err := s.readManifest(resolved.RepoID, resolved.TaskID)
	if err != nil {
		return StatusResult{}, err
	}
	summary := taskSummary(manifest)
	result.Task = &summary
	records, err := s.listCheckpoints(resolved.RepoID, resolved.TaskID)
	if err != nil {
		return StatusResult{}, err
	}
	result.Heads, err = CheckpointHeads(records)
	if err != nil {
		return StatusResult{}, err
	}
	result.CheckpointGraph = buildCheckpointGraph(records)
	if checkpoint, selectionErr := selectStableHead(records); selectionErr == nil {
		repositoryAliases, err := s.repositoryAliases(resolved.RepoID)
		if err != nil {
			return StatusResult{}, err
		}
		result.GitDifferences = compareWorkspace(checkpoint, observation, repositoryAliases...)
	} else if errors.Is(selectionErr, ErrAmbiguous) || errors.Is(selectionErr, ErrNotFound) {
		result.GitComparisonNote = "omitted: " + selectionErr.Error()
	} else {
		return StatusResult{}, selectionErr
	}
	result.LatestRuntimeSnapshot = s.latestSnapshot(resolved.RepoID, resolved.TaskID, observation.Repository.WorkingCopyID)
	return result, nil
}

func buildCheckpointGraph(records []Record) CheckpointGraph {
	nodes := make([]CheckpointGraphNode, 0, len(records))
	var edges []CheckpointGraphEdge
	for _, record := range records {
		checkpointID, _ := record["checkpoint_id"].(string)
		parents := append([]string(nil), stringSlice(record["parent_ids"])...)
		sort.Strings(parents)
		nodes = append(nodes, CheckpointGraphNode{
			CheckpointID: checkpointID,
			ParentIDs:    parents,
			Purpose:      stringField(record, "purpose"),
			Stability:    stringField(record, "stability"),
			WorkStatus:   stringField(record, "work_status"),
		})
		for _, parentID := range parents {
			edges = append(edges, CheckpointGraphEdge{ParentID: parentID, ChildID: checkpointID})
		}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].CheckpointID < nodes[j].CheckpointID })
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].ParentID == edges[j].ParentID {
			return edges[i].ChildID < edges[j].ChildID
		}
		return edges[i].ParentID < edges[j].ParentID
	})
	if nodes == nil {
		nodes = []CheckpointGraphNode{}
	}
	if edges == nil {
		edges = []CheckpointGraphEdge{}
	}
	return CheckpointGraph{Nodes: nodes, Edges: edges}
}

func (s *Service) ensureRepository(resolved Resolved) error {
	path := filepath.Join(s.store.RepoDir(resolved.RepoID), "repo.yaml")
	repository := model.Repository{SchemaVersion: model.SchemaVersion, RepoID: resolved.RepoID, CanonicalRemote: resolved.CanonicalRemote, WorkingCopies: map[string]string{resolved.WorkingCopyID: resolved.Root}}
	var existing model.Repository
	if err := s.store.ReadYAML(path, &existing); err == nil {
		if err := validateRepositoryMetadataForResolved(existing, resolved, path); err != nil {
			return err
		}
		if existing.WorkingCopies == nil {
			existing.WorkingCopies = map[string]string{}
		}
		existing.WorkingCopies[resolved.WorkingCopyID] = resolved.Root
		if existing.CanonicalRemote == "" {
			existing.CanonicalRemote = resolved.CanonicalRemote
		}
		repository = existing
	} else if !isNotFound(err) {
		return storeError(err)
	}
	if err := s.store.WriteYAML(path, repository); err != nil {
		return storeError(err)
	}
	return nil
}

func (s *Service) readRepository(repoID string) (model.Repository, error) {
	path := filepath.Join(s.store.RepoDir(repoID), "repo.yaml")
	var repository model.Repository
	if err := s.store.ReadYAML(path, &repository); err != nil {
		if isNotFound(err) {
			return model.Repository{}, fmt.Errorf("%w: repository %q", ErrNotFound, repoID)
		}
		return model.Repository{}, storeError(err)
	}
	if err := validateRepositoryMetadataVersion(repository, path); err != nil {
		return model.Repository{}, err
	}
	return repository, nil
}

func (s *Service) repositoryAliases(repoID string) ([]string, error) {
	repository, err := s.readRepository(repoID)
	if err != nil {
		return nil, err
	}
	return append([]string(nil), repository.Aliases...), nil
}

func (s *Service) repositoryTaskIDs(repoID string) ([]string, error) {
	directory := filepath.Dir(s.store.TaskDir(repoID, "placeholder"))
	entries, err := os.ReadDir(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, storeError(err)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != unboundRuntimeID {
			ids = append(ids, entry.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func sortedRepositoryAliases(aliases []string, repoID string) []string {
	unique := uniqueStrings(aliases)
	result := unique[:0]
	for _, alias := range unique {
		if alias != repoID {
			result = append(result, alias)
		}
	}
	sort.Strings(result)
	return result
}

func (s *Service) writeBinding(repoID, taskID string) error {
	binding := model.Binding{SchemaVersion: model.SchemaVersion, RepoID: repoID, TaskID: taskID, Client: s.client, BoundAt: s.now().UTC()}
	if err := s.store.WriteYAML(s.store.BindingPath(repoID, taskID, s.client), binding); err != nil {
		return storeError(err)
	}
	return nil
}

func (s *Service) readBinding(repoID string) (model.Binding, error) {
	manifests, err := s.listManifests(repoID)
	if err != nil {
		return model.Binding{}, err
	}
	repositoryAliases, err := s.repositoryAliases(repoID)
	if err != nil {
		return model.Binding{}, err
	}
	var latest model.Binding
	for _, manifest := range manifests {
		var binding model.Binding
		err := s.store.ReadYAML(s.store.BindingPath(repoID, manifest.TaskID, s.client), &binding)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return model.Binding{}, storeError(err)
		}
		if binding.TaskID == "" {
			return model.Binding{}, fmt.Errorf("%w: binding has no task", ErrValidation)
		}
		if binding.TaskID != manifest.TaskID || binding.Client != s.client {
			return model.Binding{}, fmt.Errorf("%w: binding identity does not match its task or client", ErrValidation)
		}
		if binding.RepoID != repoID && !contains(repositoryAliases, binding.RepoID) {
			return model.Binding{}, fmt.Errorf("%w: binding repository %q is not linked to %q", ErrValidation, binding.RepoID, repoID)
		}
		if latest.TaskID == "" || binding.BoundAt.After(latest.BoundAt) {
			latest = binding
		}
	}
	if latest.TaskID == "" {
		return model.Binding{}, fs.ErrNotExist
	}
	return latest, nil
}

func (s *Service) activeTaskID(repoID string) (string, error) {
	binding, err := s.readBinding(repoID)
	if err != nil {
		if isNotFound(err) {
			return "", fmt.Errorf("%w: no active task for client %q", ErrNotFound, s.client)
		}
		return "", err
	}
	return binding.TaskID, nil
}

func (s *Service) listManifests(repoID string) ([]model.TaskManifest, error) {
	tasksDir := filepath.Dir(s.store.TaskDir(repoID, "placeholder"))
	entries, err := os.ReadDir(tasksDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, storeError(err)
	}
	manifests := make([]model.TaskManifest, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == unboundRuntimeID {
			continue
		}
		manifest, err := s.readManifest(repoID, entry.Name())
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		manifests = append(manifests, manifest)
	}
	return manifests, nil
}

func (s *Service) readManifest(repoID, taskID string) (model.TaskManifest, error) {
	path := s.store.ManifestPath(repoID, taskID)
	var manifest model.TaskManifest
	if err := s.store.ReadYAML(path, &manifest); err != nil {
		if isNotFound(err) {
			return model.TaskManifest{}, fmt.Errorf("%w: task %q", ErrNotFound, taskID)
		}
		return model.TaskManifest{}, storeError(err)
	}
	if err := validateTaskManifestVersion(manifest, path); err != nil {
		return model.TaskManifest{}, err
	}
	return manifest, nil
}

func (s *Service) findTask(repoID, selector string) (model.TaskManifest, error) {
	manifests, err := s.listManifests(repoID)
	if err != nil {
		return model.TaskManifest{}, err
	}
	var matches []model.TaskManifest
	for _, manifest := range manifests {
		if manifest.TaskID == selector || contains(manifest.Aliases, selector) {
			matches = append(matches, manifest)
		}
	}
	switch len(matches) {
	case 0:
		return model.TaskManifest{}, fmt.Errorf("%w: task %q", ErrNotFound, selector)
	case 1:
		return matches[0], nil
	default:
		return model.TaskManifest{}, fmt.Errorf("%w: task selector %q matches %d tasks", ErrAmbiguous, selector, len(matches))
	}
}

// findResumableTask lets a new client continue without a prior local binding.
// A unique current handoff is preferred; otherwise exactly one task with a
// stable checkpoint subgraph is required.
func (s *Service) findResumableTask(repoID string) (string, error) {
	manifests, err := s.listManifests(repoID)
	if err != nil {
		return "", err
	}
	var handoffTasks, resumableTasks []string
	for _, manifest := range manifests {
		records, err := s.listCheckpoints(repoID, manifest.TaskID)
		if err != nil {
			return "", err
		}
		stable, err := StableCheckpointHeads(records)
		if err != nil {
			return "", err
		}
		if len(stable) == 0 {
			continue
		}
		resumableTasks = append(resumableTasks, manifest.TaskID)
		if _, ok, err := s.ensureValidHandoff(repoID, manifest.TaskID, records); err != nil {
			return "", err
		} else if ok {
			handoffTasks = append(handoffTasks, manifest.TaskID)
		}
	}
	sort.Strings(handoffTasks)
	sort.Strings(resumableTasks)
	switch len(handoffTasks) {
	case 1:
		return handoffTasks[0], nil
	case 0:
		// Fall through to the stable-task selection rule.
	default:
		return "", fmt.Errorf("%w: resumable handoffs for tasks %v; provide --task", ErrAmbiguous, handoffTasks)
	}
	switch len(resumableTasks) {
	case 0:
		return "", fmt.Errorf("%w: no task has a stable checkpoint", ErrNotFound)
	case 1:
		return resumableTasks[0], nil
	default:
		return "", fmt.Errorf("%w: resumable tasks %v; provide --task", ErrAmbiguous, resumableTasks)
	}
}

func (s *Service) ensureAliasesAvailable(repoID string, aliases []string, currentTaskID string) error {
	if len(aliases) == 0 {
		return nil
	}
	manifests, err := s.listManifests(repoID)
	if err != nil {
		return err
	}
	for _, alias := range aliases {
		if alias == "" {
			return fmt.Errorf("%w: task alias cannot be empty", ErrValidation)
		}
		for _, manifest := range manifests {
			if manifest.TaskID != currentTaskID && (manifest.TaskID == alias || contains(manifest.Aliases, alias)) {
				return fmt.Errorf("%w: task alias %q conflicts with an existing task selector", ErrValidation, alias)
			}
		}
	}
	return nil
}

func (s *Service) ensureTaskIDAvailable(repoID, taskID string) error {
	manifests, err := s.listManifests(repoID)
	if err != nil {
		return err
	}
	for _, manifest := range manifests {
		if manifest.TaskID == taskID || contains(manifest.Aliases, taskID) {
			return fmt.Errorf("%w: task ID %q conflicts with an existing task selector", ErrValidation, taskID)
		}
	}
	return nil
}

func (s *Service) listCheckpoints(repoID, taskID string) ([]Record, error) {
	records, err := s.store.ListJSON(filepath.Dir(s.store.CheckpointPath(repoID, taskID, "placeholder")))
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, storeError(err)
	}
	for _, record := range records {
		checkpointID, _ := record["checkpoint_id"].(string)
		if err := schema.Validate(schema.Checkpoint, record); err != nil {
			return nil, fmt.Errorf("%w: checkpoint %q: %v", ErrValidation, checkpointID, err)
		}
		if record["task_id"] != taskID {
			return nil, fmt.Errorf("%w: checkpoint %q task_id does not match task directory %q", ErrValidation, checkpointID, taskID)
		}
	}
	return records, nil
}

func (s *Service) updateManifest(repoID string, manifest model.TaskManifest, records []Record) error {
	path := s.store.ManifestPath(repoID, manifest.TaskID)
	if err := validateTaskManifestVersion(manifest, path); err != nil {
		return err
	}
	if err := applyCheckpointMetadata(&manifest, records); err != nil {
		return err
	}
	manifest.LastUsedAt = s.now().UTC()
	if err := s.store.WriteYAML(path, manifest); err != nil {
		return storeError(err)
	}
	return nil
}

func applyCheckpointMetadata(manifest *model.TaskManifest, records []Record) error {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		id, err := recordString(record, "checkpoint_id")
		if err != nil {
			return err
		}
		ids = append(ids, id)
	}
	heads, err := CheckpointHeads(records)
	if err != nil {
		return err
	}
	manifest.CheckpointIDs = ids
	manifest.HeadIDs = heads
	stable, err := StableCheckpointHeads(records)
	if err != nil {
		return err
	}
	manifest.StableHeadIDs = stable
	manifest.StableHeadID = ""
	manifest.HeadStatuses = make(map[string]string, len(stable))
	for _, id := range stable {
		record, err := findCheckpoint(records, id)
		if err != nil {
			return err
		}
		if status, ok := record["work_status"].(string); ok {
			manifest.HeadStatuses[id] = status
		}
	}
	if len(stable) == 1 {
		manifest.StableHeadID = stable[0]
		manifest.Status = manifest.HeadStatuses[stable[0]]
	} else if len(stable) > 1 {
		manifest.Status = "ambiguous"
	}
	return nil
}

func applyRecoveredCheckpointMetadata(manifest *model.TaskManifest, records []Record) error {
	if len(records) == 0 {
		return nil
	}
	latest := records[0]
	earliest := records[0]
	for _, record := range records[1:] {
		if fmt.Sprint(record["created_at"]) > fmt.Sprint(latest["created_at"]) {
			latest = record
		}
		if fmt.Sprint(record["created_at"]) < fmt.Sprint(earliest["created_at"]) {
			earliest = record
		}
	}
	if manifest.Title == "" {
		if title, ok := asRecord(latest["context"])["title"].(string); ok {
			manifest.Title = title
		}
	}
	if status, ok := latest["work_status"].(string); ok {
		manifest.Status = status
	}
	if manifest.CreatedAt.IsZero() {
		if created, err := time.Parse(time.RFC3339Nano, fmt.Sprint(earliest["created_at"])); err == nil {
			manifest.CreatedAt = created
		}
	}
	if lastUsed, err := time.Parse(time.RFC3339Nano, fmt.Sprint(latest["created_at"])); err == nil {
		manifest.LastUsedAt = lastUsed
	}
	return applyCheckpointMetadata(manifest, records)
}

func (s *Service) latestSnapshot(repoID, taskID, workingCopyID string) Record {
	directory := filepath.Dir(s.store.SnapshotPath(repoID, taskID, "placeholder"))
	records, err := s.store.ListJSON(directory)
	if err != nil {
		return nil
	}
	var latest Record
	for _, record := range records {
		repository := asRecord(record["repository"])
		if repository["working_copy_id"] != workingCopyID {
			continue
		}
		if latest == nil || fmt.Sprint(record["captured_at"]) > fmt.Sprint(latest["captured_at"]) || (fmt.Sprint(record["captured_at"]) == fmt.Sprint(latest["captured_at"]) && fmt.Sprint(record["snapshot_id"]) > fmt.Sprint(latest["snapshot_id"])) {
			latest = record
		}
	}
	return latest
}

func (s *Service) syncStatePath(repoID string) string {
	return filepath.Join(s.store.RepoDir(repoID), "sync-status.yaml")
}

func (s *Service) writeSyncState(repoID string, state SyncState) error {
	if err := s.store.WriteYAML(s.syncStatePath(repoID), state); err != nil {
		return storeError(err)
	}
	return nil
}

func (s *Service) lastSync(repoID string) *SyncState {
	var state SyncState
	if err := s.store.ReadYAML(s.syncStatePath(repoID), &state); err != nil {
		return nil
	}
	return &state
}

func (s *Service) rebuildManifests(repoID string) error {
	entries, err := os.ReadDir(filepath.Dir(s.store.TaskDir(repoID, "placeholder")))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return storeError(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		taskID := entry.Name()
		records, err := s.listCheckpoints(repoID, taskID)
		if err != nil {
			return err
		}
		if len(records) == 0 {
			continue
		}
		for _, record := range records {
			if err := schema.Validate(schema.Checkpoint, record); err != nil {
				return fmt.Errorf("%w: synced checkpoint %q: %v", ErrValidation, record["checkpoint_id"], err)
			}
			if record["task_id"] != taskID {
				return fmt.Errorf("%w: checkpoint task does not match its directory", ErrValidation)
			}
		}
		manifest, err := s.readManifest(repoID, taskID)
		if isNotFound(err) {
			manifest = model.TaskManifest{SchemaVersion: model.SchemaVersion, TaskID: taskID, IdentityRecovered: true}
		} else if err != nil {
			return err
		}
		if err := applyRecoveredCheckpointMetadata(&manifest, records); err != nil {
			return err
		}
		if err := s.store.WriteYAML(s.store.ManifestPath(repoID, manifest.TaskID), manifest); err != nil {
			return storeError(err)
		}
		if _, _, err := s.ensureValidHandoff(repoID, taskID, records); err != nil {
			return err
		}
	}
	return nil
}

// Sync exchanges immutable checkpoint JSON and the minimal task identity
// metadata required to render and select a task on another device. Heads,
// status, and last-used time are derived locally from checkpoints.
func (s *Service) Sync(ctx context.Context, cwd, remoteRoot, direction string) error {
	if remoteRoot == "" {
		return fmt.Errorf("%w: --remote is required for filesystem sync", ErrValidation)
	}
	if direction == "" {
		direction = "both"
	}
	if direction != "both" && direction != "pull" && direction != "push" {
		return fmt.Errorf("%w: --direction must be both, pull, or push", ErrValidation)
	}
	return s.withRepositoryIdentityLock(cwd, func(observation gitobs.Observation) error {
		resolved, err := s.resolveFromObservation(observation)
		if err != nil {
			return err
		}
		syncErr := withRemoteRepositoryLock(remoteRoot, resolved.RepoID, direction, func() error {
			return s.syncResolved(resolved, remoteRoot, direction)
		})
		if syncErr == nil || errors.Is(syncErr, ErrSync) {
			return syncErr
		}
		state := SyncState{Remote: remoteRoot, Direction: direction, Status: "failed", At: timestamp(s.now()), Message: "remote access: " + syncErr.Error()}
		if stateErr := s.writeSyncState(resolved.RepoID, state); stateErr != nil {
			return fmt.Errorf("%w: remote access: %v; record failed sync: %v", ErrSync, syncErr, stateErr)
		}
		return fmt.Errorf("%w: remote access: %v", ErrSync, syncErr)
	})
}

// Sync always holds its local repository identity locks before acquiring this
// shared remote lock. Push and both cover the complete remote
// preflight-to-publication interval exclusively; pull uses a shared lock so it
// cannot observe another producer's partial publication.
func withRemoteRepositoryLock(remoteRoot, repoID, direction string, fn func() error) error {
	remote := store.New(remoteRoot)
	if direction == "pull" {
		return remote.WithRepositoryLock(repoID, fn)
	}
	return remote.WithRepositoryTransitionLock(repoID, fn)
}

func (s *Service) syncResolved(resolved Resolved, remoteRoot, direction string) error {
	localRepo := s.store.RepoDir(resolved.RepoID)
	remoteRepo := filepath.Join(remoteRoot, "repos", resolved.RepoID)
	if direction == "pull" {
		if pullErr := validateExplicitPullSource(remoteRepo, localRepo); pullErr != nil {
			state := SyncState{Remote: remoteRoot, Direction: direction, Status: "failed", At: timestamp(s.now()), Message: "pull: " + pullErr.Error()}
			if stateErr := s.writeSyncState(resolved.RepoID, state); stateErr != nil {
				return fmt.Errorf("%w: pull: %v; record failed sync: %v", ErrSync, pullErr, stateErr)
			}
			return fmt.Errorf("%w: pull: %v", ErrSync, pullErr)
		}
	} else if initializationErr := validateInitializableRemote(remoteRepo); initializationErr != nil {
		state := SyncState{Remote: remoteRoot, Direction: direction, Status: "failed", At: timestamp(s.now()), Message: "initialize: " + initializationErr.Error()}
		if stateErr := s.writeSyncState(resolved.RepoID, state); stateErr != nil {
			return fmt.Errorf("%w: initialize: %v; record failed sync: %v", ErrSync, initializationErr, stateErr)
		}
		return fmt.Errorf("%w: initialize: %v", ErrSync, initializationErr)
	}
	if direction == "pull" || direction == "both" {
		if err := syncRepository(remoteRepo, localRepo, true, s.afterSyncPreflight); err != nil {
			s.writeSyncState(resolved.RepoID, SyncState{Remote: remoteRoot, Direction: direction, Status: "failed", At: timestamp(s.now()), Message: "pull: " + err.Error()})
			return fmt.Errorf("%w: pull: %v", ErrSync, err)
		}
	}
	if direction == "push" || direction == "both" {
		if err := syncRepository(localRepo, remoteRepo, false, s.afterSyncPreflight); err != nil {
			s.writeSyncState(resolved.RepoID, SyncState{Remote: remoteRoot, Direction: direction, Status: "failed", At: timestamp(s.now()), Message: "push: " + err.Error()})
			return fmt.Errorf("%w: push: %v", ErrSync, err)
		}
	}
	if err := s.rebuildManifests(resolved.RepoID); err != nil {
		state := SyncState{Remote: remoteRoot, Direction: direction, Status: "failed", At: timestamp(s.now()), Message: "rebuild: " + err.Error()}
		if stateErr := s.writeSyncState(resolved.RepoID, state); stateErr != nil {
			return fmt.Errorf("%w: rebuild: %v; record failed sync: %v", ErrSync, err, stateErr)
		}
		return fmt.Errorf("%w: rebuild: %v", ErrSync, err)
	}
	if err := s.writeSyncState(resolved.RepoID, SyncState{Remote: remoteRoot, Direction: direction, Status: "ok", At: timestamp(s.now())}); err != nil {
		return err
	}
	return nil
}

func validateExplicitPullSource(remoteRepo, localRepo string) error {
	remoteIdentityPath := filepath.Join(remoteRepo, "repo.yaml")
	_, _, exists, err := validateRepositoryIdentityFiles(remoteIdentityPath, filepath.Join(localRepo, "repo.yaml"))
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("remote repository identity %q does not exist", remoteIdentityPath)
	}
	return nil
}

func validateInitializableRemote(remoteRepo string) error {
	if _, err := os.Stat(filepath.Join(remoteRepo, "repo.yaml")); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	hasData := false
	err := filepath.WalkDir(remoteRepo, func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && path == remoteRepo {
			return nil
		}
		if err != nil {
			return err
		}
		if path != remoteRepo && !entry.IsDir() {
			hasData = true
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return err
	}
	if hasData {
		return fmt.Errorf("remote repository %q contains data but has no repo.yaml", remoteRepo)
	}
	return nil
}

func (s *Service) snapshotRecord(id, taskID, triggerKind, triggerName string, observation gitobs.Observation) Record {
	capture := Record{"completeness": "complete", "warnings": []any{}, "omitted_sections": []any{}}
	if observation.Snapshot.Worktree.State == "unknown" {
		capture["completeness"] = "partial"
		capture["warnings"] = []any{observation.Snapshot.Worktree.Diagnostic}
	}
	record := Record{
		"schema_version": model.SchemaVersion,
		"record_type":    "ctx.runtime_snapshot",
		"snapshot_id":    id,
		"captured_at":    timestamp(s.now()),
		"device_id":      s.deviceID,
		"trigger":        triggerRecord(triggerKind, triggerName),
		"producer":       s.producer(),
		"repository":     repositorySnapshot(observation),
		"session_refs":   s.sessionRefs(),
		"capture":        capture,
		"extensions":     Record{},
	}
	if taskID != "" {
		record["task_id"] = taskID
	}
	return record
}

func (s *Service) producer() Record {
	record := Record{"actor_type": "cli", "system": s.client, "extensions": Record{}}
	if s.version != "" {
		record["version"] = s.version
	}
	if s.deviceID != "" {
		record["device_id"] = s.deviceID
	}
	return record
}

func (s *Service) sessionRefs() []any {
	if s.sessionID == "" {
		return []any{}
	}
	sum := sha256.Sum256([]byte(s.sessionID))
	return []any{Record{
		"session_ref_id": "session-" + hex.EncodeToString(sum[:])[:16],
		"system":         s.client,
		"session_id":     s.sessionID,
		"relation":       "authoring",
		"logs":           []any{},
		"extensions":     Record{},
	}}
}

func workspaceBaseline(observation gitobs.Observation, observedAt string) Record {
	return Record{
		"primary_repo_id": observation.Repository.RepoID,
		"repositories": []any{Record{
			"repo_id":         observation.Repository.RepoID,
			"working_copy_id": observation.Repository.WorkingCopyID,
			"observed_at":     observedAt,
			"object_format":   observation.Snapshot.ObjectFormat,
			"head":            headRecord(observation.Snapshot.Head),
			"operation":       operationRecord(observation.Snapshot.Operation),
			"worktree":        baselineWorktree(observation.Snapshot.Worktree),
		}},
	}
}

func repositorySnapshot(observation gitobs.Observation) Record {
	return Record{
		"repo_id":         observation.Repository.RepoID,
		"working_copy_id": observation.Repository.WorkingCopyID,
		"root_uri":        "file://" + filepath.ToSlash(observation.Repository.Root),
		"git":             gitState(observation.Snapshot),
	}
}

func gitState(snapshot gitobs.Snapshot) Record {
	record := Record{
		"object_format": snapshot.ObjectFormat,
		"head":          headRecord(snapshot.Head),
		"operation":     operationRecord(snapshot.Operation),
		"worktree":      worktreeState(snapshot.Worktree),
	}
	if snapshot.Upstream != nil {
		upstream := Record{"remote": snapshot.Upstream.Remote, "ref": snapshot.Upstream.Ref, "ahead": snapshot.Upstream.Ahead, "behind": snapshot.Upstream.Behind}
		if snapshot.Upstream.OID != "" {
			upstream["oid"] = snapshot.Upstream.OID
		}
		record["upstream"] = upstream
	}
	return record
}

func headRecord(head gitobs.Head) Record {
	record := Record{"state": head.State}
	if head.SymbolicRef != "" {
		record["symbolic_ref"] = head.SymbolicRef
	}
	if head.OID != "" {
		record["oid"] = head.OID
	}
	return record
}

func operationRecord(operation gitobs.Operation) Record {
	record := Record{"kind": operation.Kind}
	if operation.Detail != "" {
		record["detail"] = operation.Detail
	}
	return record
}

func worktreeState(worktree gitobs.Worktree) Record {
	entries := make([]any, 0, len(worktree.Changes.Entries))
	for _, change := range worktree.Changes.Entries {
		entry := Record{"path": change.Path, "index_status": change.IndexStatus, "worktree_status": change.WorktreeStatus, "conflict": change.Conflict}
		if change.OriginalPath != "" {
			entry["original_path"] = change.OriginalPath
		}
		entries = append(entries, entry)
	}
	changes := Record{"complete": worktree.Changes.Complete, "total_entries": len(entries), "untracked_included": worktree.Changes.UntrackedIncluded, "ignored_included": worktree.Changes.IgnoredIncluded, "entries": entries}
	record := Record{"state": worktree.State, "changes": changes}
	if worktree.Fingerprint != "" {
		record["fingerprint"] = Record{"profile": "ctx-git-worktree-v1", "digest": worktree.Fingerprint}
	}
	if worktree.Diagnostic != "" {
		record["diagnostic"] = worktree.Diagnostic
	}
	return record
}

func baselineWorktree(worktree gitobs.Worktree) Record {
	record := Record{"state": worktree.State}
	if worktree.Fingerprint != "" {
		record["fingerprint"] = Record{"profile": "ctx-git-worktree-v1", "digest": worktree.Fingerprint}
	}
	if worktree.Diagnostic != "" {
		record["diagnostic"] = worktree.Diagnostic
	}
	return record
}

func triggerRecord(kind, name string) Record {
	record := Record{"kind": kind}
	if name != "" {
		record["name"] = name
	}
	return record
}

func validateCaptureShape(input Record) error {
	if err := schema.Validate(schema.CaptureInput, input); err != nil {
		return fmt.Errorf("%w: %v", ErrValidation, err)
	}
	return nil
}

func completeness(input Record) string {
	capture := asRecord(input["capture"])
	value, _ := capture["completeness"].(string)
	return value
}

func dedupeKey(record Record) (string, error) {
	workspace := asRecord(record["workspace"])
	repositories := recordsFromAny(workspace["repositories"])
	sort.Slice(repositories, func(i, j int) bool {
		left, _ := repositories[i]["repo_id"].(string)
		right, _ := repositories[j]["repo_id"].(string)
		return left < right
	})
	for i := range repositories {
		repositories[i] = Record{
			"repo_id":       repositories[i]["repo_id"],
			"object_format": repositories[i]["object_format"],
			"head":          repositories[i]["head"],
			"operation":     repositories[i]["operation"],
			"worktree":      repositories[i]["worktree"],
		}
	}
	parents := uniqueStrings(stringSlice(record["parent_ids"]))
	sort.Strings(parents)
	key := Record{
		"schema_version": record["schema_version"],
		"task_id":        record["task_id"],
		"purpose":        record["purpose"],
		"stability":      record["stability"],
		"work_status":    record["work_status"],
		"capture":        record["capture"],
		"parent_ids":     stringsAny(parents),
		"context_digest": record["context_digest"],
		"repositories":   recordsAny(repositories),
	}
	if record["purpose"] == "handoff" {
		if target := handoffTarget(record); len(target) > 0 {
			key["handoff_target"] = target
		}
	}
	return canonical.Digest(key)
}

func findCheckpoint(records []Record, id string) (Record, error) {
	for _, record := range records {
		if record["checkpoint_id"] == id {
			return record, nil
		}
	}
	return nil, fmt.Errorf("%w: checkpoint %q", ErrNotFound, id)
}

func selectStableHead(records []Record) (Record, error) {
	heads, err := StableCheckpointHeads(records)
	if err != nil {
		return nil, err
	}
	stable := make([]Record, 0, len(heads))
	for _, id := range heads {
		record, err := findCheckpoint(records, id)
		if err != nil {
			return nil, err
		}
		stable = append(stable, record)
	}
	switch len(stable) {
	case 0:
		return nil, fmt.Errorf("%w: no stable checkpoint head", ErrNotFound)
	case 1:
		return stable[0], nil
	default:
		ids := make([]string, 0, len(stable))
		for _, record := range stable {
			ids = append(ids, record["checkpoint_id"].(string))
		}
		return nil, fmt.Errorf("%w: stable checkpoint heads %v; provide --checkpoint", ErrAmbiguous, ids)
	}
}

func (s *Service) validHandoff(repoID, taskID string, checkpoints []Record) (string, bool) {
	data, err := os.ReadFile(s.store.HandoffPath(repoID, taskID))
	if err != nil {
		return "", false
	}
	parts := strings.SplitN(string(data), "---\n", 3)
	if len(parts) != 3 || parts[0] != "" {
		return "", false
	}
	var handoff Record
	if yaml.Unmarshal([]byte(parts[1]), &handoff) != nil {
		return "", false
	}
	if err := schema.Validate(schema.Handoff, handoff); err != nil {
		return "", false
	}
	expected, err := render.RenderHandoff(handoff)
	if err != nil || !bytes.Equal(data, expected) {
		return "", false
	}
	checkpointID, ok := handoff["checkpoint_id"].(string)
	if !ok || handoff["task_id"] != taskID {
		return "", false
	}
	checkpoint, err := findCheckpoint(checkpoints, checkpointID)
	if err != nil || checkpoint["content_digest"] != handoff["checkpoint_digest"] {
		return "", false
	}
	if target := handoffTarget(checkpoint); len(target) > 0 {
		pointerTarget := asRecord(handoff["target"])
		want, wantErr := canonical.Digest(target)
		got, gotErr := canonical.Digest(pointerTarget)
		if wantErr != nil || gotErr != nil || got != want {
			return "", false
		}
	}
	body := render.HandoffBody(taskID, checkpointID)
	if handoff["rendered_body_digest"] != render.Digest([]byte(body)) {
		return "", false
	}
	if checkpoint["purpose"] != "handoff" || checkpoint["stability"] != "stable" || completeness(Record{"capture": checkpoint["capture"]}) != "complete" {
		return "", false
	}
	stableHeads, err := StableCheckpointHeads(checkpoints)
	if err != nil || len(stableHeads) != 1 || stableHeads[0] != checkpointID {
		return "", false
	}
	return checkpointID, true
}

func (s *Service) ensureValidHandoff(repoID, taskID string, checkpoints []Record) (string, bool, error) {
	if checkpointID, ok := s.validHandoff(repoID, taskID, checkpoints); ok {
		return checkpointID, true, nil
	}
	stableHeads, err := StableCheckpointHeads(checkpoints)
	if err != nil {
		return "", false, err
	}
	if len(stableHeads) != 1 {
		return "", false, nil
	}
	checkpoint, err := findCheckpoint(checkpoints, stableHeads[0])
	if err != nil {
		return "", false, err
	}
	if checkpoint["purpose"] != "handoff" || checkpoint["stability"] != "stable" || completeness(Record{"capture": checkpoint["capture"]}) != "complete" {
		return "", false, nil
	}
	checkpointID := stableHeads[0]
	checkpointDigest, _ := checkpoint["content_digest"].(string)
	_, content, err := s.prepareHandoffPointer(taskID, checkpointID, checkpointDigest, handoffTarget(checkpoint))
	if err != nil {
		return "", false, err
	}
	if err := s.atomicWrite(s.store.HandoffPath(repoID, taskID), content, 0644); err != nil {
		return "", false, storeError(err)
	}
	return checkpointID, true, nil
}

func handoffTarget(checkpoint Record) Record {
	extensions := asRecord(checkpoint["extensions"])
	ctx := asRecord(extensions["io.github.robinjoon.ctx"])
	target := asRecord(ctx["handoff_target"])
	if len(target) == 0 {
		return nil
	}
	return cloneRecord(target)
}

func compareWorkspace(checkpoint Record, observation gitobs.Observation, equivalentRepoIDs ...string) []string {
	workspace := asRecord(checkpoint["workspace"])
	for _, baseline := range recordsFromAny(workspace["repositories"]) {
		baselineRepoID, _ := baseline["repo_id"].(string)
		if baselineRepoID != observation.Repository.RepoID && !contains(equivalentRepoIDs, baselineRepoID) {
			continue
		}
		var differences []string
		head := asRecord(baseline["head"])
		if stringField(head, "oid") != observation.Snapshot.Head.OID || stringField(head, "symbolic_ref") != observation.Snapshot.Head.SymbolicRef || stringField(head, "state") != observation.Snapshot.Head.State {
			differences = append(differences, "HEAD or branch differs from the checkpoint baseline.")
		}
		operation := asRecord(baseline["operation"])
		if stringField(operation, "kind") != observation.Snapshot.Operation.Kind {
			differences = append(differences, "Git operation differs from the checkpoint baseline.")
		}
		worktree := asRecord(baseline["worktree"])
		fingerprint := asRecord(worktree["fingerprint"])
		if stringField(worktree, "state") != observation.Snapshot.Worktree.State || stringField(fingerprint, "digest") != observation.Snapshot.Worktree.Fingerprint {
			differences = append(differences, "Worktree differs from the checkpoint baseline; ctx did not modify it.")
		}
		return differences
	}
	return []string{"The checkpoint has no baseline for this repository."}
}

func stringField(record Record, key string) string {
	value, _ := record[key].(string)
	return value
}

func syncRepository(sourceRepo, destinationRepo string, preserveDestinationWorkingCopies bool, afterPreflight func()) error {
	if err := preflightSyncRepository(sourceRepo, destinationRepo); err != nil {
		return err
	}
	if afterPreflight != nil {
		afterPreflight()
	}
	if err := syncRepositoryIdentity(sourceRepo, destinationRepo, preserveDestinationWorkingCopies); err != nil {
		return err
	}
	sourceTasks := filepath.Join(sourceRepo, "tasks")
	entries, err := os.ReadDir(sourceTasks)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, task := range entries {
		if !task.IsDir() {
			continue
		}
		sourceTask := filepath.Join(sourceTasks, task.Name())
		destinationTask := filepath.Join(destinationRepo, "tasks", task.Name())
		if err := syncTaskIdentity(filepath.Join(sourceTask, "manifest.yaml"), filepath.Join(destinationTask, "manifest.yaml"), task.Name()); err != nil {
			return err
		}
		sourceCheckpoints := filepath.Join(sourceTask, "checkpoints")
		files, err := os.ReadDir(sourceCheckpoints)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		for _, file := range files {
			if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
				continue
			}
			source := filepath.Join(sourceCheckpoints, file.Name())
			destination := filepath.Join(destinationTask, "checkpoints", file.Name())
			data, err := os.ReadFile(source)
			if err != nil {
				return err
			}
			record, err := schema.Decode(data)
			if err != nil {
				return fmt.Errorf("decode %s: %w", source, err)
			}
			if err := schema.Validate(schema.Checkpoint, record); err != nil {
				return fmt.Errorf("validate %s: %w", source, err)
			}
			existing, err := os.ReadFile(destination)
			if err == nil {
				if string(existing) != string(data) {
					return fmt.Errorf("immutable checkpoint conflict at %s", destination)
				}
				continue
			}
			if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
				return err
			}
			if err := atomicFile(destination, data, 0644); err != nil {
				return err
			}
		}
	}
	return nil
}

// preflightSyncRepository validates the complete source graph and every
// immutable destination conflict before copying a single file. A malformed
// remote therefore cannot leave a partially imported bad checkpoint behind.
func preflightSyncRepository(sourceRepo, destinationRepo string) error {
	if _, _, _, err := validateRepositoryIdentityFiles(filepath.Join(sourceRepo, "repo.yaml"), filepath.Join(destinationRepo, "repo.yaml")); err != nil {
		return err
	}
	sourceManifests, err := taskManifestsFromRepository(sourceRepo)
	if err != nil {
		return err
	}
	destinationManifests, err := taskManifestsFromRepository(destinationRepo)
	if err != nil {
		return err
	}
	if err := validateTaskSelectorUnion(sourceManifests, destinationManifests); err != nil {
		return err
	}
	sourceTasks := filepath.Join(sourceRepo, "tasks")
	entries, err := os.ReadDir(sourceTasks)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, task := range entries {
		if !task.IsDir() {
			continue
		}
		taskID := task.Name()
		sourceTask := filepath.Join(sourceTasks, taskID)
		destinationTask := filepath.Join(destinationRepo, "tasks", taskID)
		if _, _, _, _, err := validateTaskIdentityFiles(filepath.Join(sourceTask, "manifest.yaml"), filepath.Join(destinationTask, "manifest.yaml"), taskID); err != nil {
			return err
		}

		sourceCheckpoints := filepath.Join(sourceTask, "checkpoints")
		files, err := os.ReadDir(sourceCheckpoints)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		records := make([]Record, 0, len(files))
		for _, file := range files {
			if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
				continue
			}
			source := filepath.Join(sourceCheckpoints, file.Name())
			data, err := os.ReadFile(source)
			if err != nil {
				return err
			}
			record, err := schema.Decode(data)
			if err != nil {
				return fmt.Errorf("decode %s: %w", source, err)
			}
			if err := schema.Validate(schema.Checkpoint, record); err != nil {
				return fmt.Errorf("validate %s: %w", source, err)
			}
			checkpointID, err := recordString(record, "checkpoint_id")
			if err != nil {
				return fmt.Errorf("validate %s: %w", source, err)
			}
			if record["task_id"] != taskID {
				return fmt.Errorf("checkpoint %q task_id does not match directory %q", checkpointID, taskID)
			}
			if file.Name() != checkpointID+".json" {
				return fmt.Errorf("checkpoint filename %q does not match checkpoint_id %q", file.Name(), checkpointID)
			}
			destination := filepath.Join(destinationTask, "checkpoints", file.Name())
			if existing, readErr := os.ReadFile(destination); readErr == nil {
				if string(existing) != string(data) {
					return fmt.Errorf("immutable checkpoint conflict at %s", destination)
				}
			} else if !errors.Is(readErr, fs.ErrNotExist) {
				return readErr
			}
			records = append(records, record)
		}
		if err := validateCheckpointGraph(records); err != nil {
			return fmt.Errorf("task %q checkpoint graph: %w", taskID, err)
		}
	}
	return nil
}

func taskManifestsFromRepository(repoDirectory string) ([]model.TaskManifest, error) {
	tasksDirectory := filepath.Join(repoDirectory, "tasks")
	entries, err := os.ReadDir(tasksDirectory)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	manifests := make([]model.TaskManifest, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == unboundRuntimeID {
			continue
		}
		manifest, exists, err := readManifestFile(filepath.Join(tasksDirectory, entry.Name(), "manifest.yaml"))
		if err != nil {
			return nil, err
		}
		if !exists {
			manifest, exists, err = checkpointBackedTaskIdentity(filepath.Join(tasksDirectory, entry.Name()), entry.Name())
			if err != nil {
				return nil, err
			}
			if !exists {
				// A directory containing only device runtime data is not a
				// portable task identity and does not participate in selectors.
				continue
			}
		}
		if manifest.TaskID != entry.Name() {
			return nil, fmt.Errorf("task identity does not match directory %q", entry.Name())
		}
		manifests = append(manifests, manifest)
	}
	return manifests, nil
}

// checkpointBackedTaskIdentity recovers only the task ID needed for selector
// preflight. Titles and aliases are not encoded as checkpoint identity and
// therefore cannot be inferred when manifest.yaml is absent. A checkpoint-
// backed identity is accepted only after validating every record, its path,
// and the complete graph; malformed task data must fail before sync copies it.
func checkpointBackedTaskIdentity(taskDirectory, taskID string) (model.TaskManifest, bool, error) {
	_, exists, err := validatedCheckpointGraphFromTaskDirectory(taskDirectory, taskID)
	if err != nil || !exists {
		return model.TaskManifest{}, exists, err
	}
	return model.TaskManifest{SchemaVersion: model.SchemaVersion, TaskID: taskID, IdentityRecovered: true}, true, nil
}

func validatedCheckpointGraphFromTaskDirectory(taskDirectory, taskID string) ([]Record, bool, error) {
	checkpointDirectory := filepath.Join(taskDirectory, "checkpoints")
	files, err := os.ReadDir(checkpointDirectory)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	records := make([]Record, 0, len(files))
	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
			continue
		}
		path := filepath.Join(checkpointDirectory, file.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, false, err
		}
		record, err := schema.Decode(data)
		if err != nil {
			return nil, false, fmt.Errorf("decode %s: %w", path, err)
		}
		if err := schema.Validate(schema.Checkpoint, record); err != nil {
			return nil, false, fmt.Errorf("validate %s: %w", path, err)
		}
		checkpointID, err := recordString(record, "checkpoint_id")
		if err != nil {
			return nil, false, fmt.Errorf("validate %s: %w", path, err)
		}
		if record["task_id"] != taskID {
			return nil, false, fmt.Errorf("checkpoint %q task_id does not match directory %q", checkpointID, taskID)
		}
		if file.Name() != checkpointID+".json" {
			return nil, false, fmt.Errorf("checkpoint filename %q does not match checkpoint_id %q", file.Name(), checkpointID)
		}
		records = append(records, record)
	}
	if len(records) == 0 {
		return nil, false, nil
	}
	if err := validateCheckpointGraph(records); err != nil {
		return nil, false, fmt.Errorf("task %q checkpoint graph: %w", taskID, err)
	}
	return records, true, nil
}

func validateTaskSelectorUnion(groups ...[]model.TaskManifest) error {
	owners := make(map[string]string)
	for _, manifests := range groups {
		for _, manifest := range manifests {
			selectors := append([]string{manifest.TaskID}, manifest.Aliases...)
			for _, selector := range selectors {
				if owner, exists := owners[selector]; exists && owner != manifest.TaskID {
					return fmt.Errorf("task selector %q conflicts between tasks %q and %q", selector, owner, manifest.TaskID)
				}
				owners[selector] = manifest.TaskID
			}
		}
	}
	return nil
}

func syncRepositoryIdentity(sourceRepo, destinationRepo string, preserveDestinationWorkingCopies bool) error {
	sourcePath := filepath.Join(sourceRepo, "repo.yaml")
	destinationPath := filepath.Join(destinationRepo, "repo.yaml")
	source, destination, exists, err := validateRepositoryIdentityFiles(sourcePath, destinationPath)
	if err != nil || !exists {
		return err
	}
	if destination.SchemaVersion == 0 {
		destination.SchemaVersion = model.SchemaVersion
	}
	destination.RepoID = source.RepoID
	if destination.CanonicalRemote == "" {
		destination.CanonicalRemote = source.CanonicalRemote
	}
	destination.Aliases = sortedRepositoryAliases(append(destination.Aliases, source.Aliases...), destination.RepoID)
	if !preserveDestinationWorkingCopies {
		destination.WorkingCopies = nil
	}
	data, err := yaml.Marshal(destination)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destinationPath), 0700); err != nil {
		return err
	}
	return atomicFile(destinationPath, data, 0644)
}

func validateRepositoryIdentityFiles(sourcePath, destinationPath string) (model.Repository, model.Repository, bool, error) {
	source, exists, err := readRepositoryFile(sourcePath)
	if err != nil || !exists {
		return model.Repository{}, model.Repository{}, exists, err
	}
	if err := validateRepositoryMetadataVersion(source, sourcePath); err != nil {
		return model.Repository{}, model.Repository{}, false, err
	}
	sourceDirectoryID := filepath.Base(filepath.Dir(sourcePath))
	if source.RepoID != sourceDirectoryID {
		return model.Repository{}, model.Repository{}, false, fmt.Errorf("repository identity does not match directory %q", sourceDirectoryID)
	}
	destination, destinationExists, err := readRepositoryFile(destinationPath)
	if err != nil {
		return model.Repository{}, model.Repository{}, false, err
	}
	if destinationExists {
		if err := validateRepositoryMetadataVersion(destination, destinationPath); err != nil {
			return model.Repository{}, model.Repository{}, false, err
		}
		if destination.RepoID != "" && destination.RepoID != source.RepoID {
			return model.Repository{}, model.Repository{}, false, fmt.Errorf("repository identity conflict for %q", source.RepoID)
		}
		if destination.CanonicalRemote != "" && source.CanonicalRemote != "" && destination.CanonicalRemote != source.CanonicalRemote {
			return model.Repository{}, model.Repository{}, false, fmt.Errorf("canonical remote conflict for repository %q", source.RepoID)
		}
	}
	return source, destination, true, nil
}

func validateRepositoryMetadataVersion(repository model.Repository, path string) error {
	if repository.SchemaVersion != model.SchemaVersion {
		return fmt.Errorf("%w: repository identity %q has unsupported schema_version %d", ErrValidation, path, repository.SchemaVersion)
	}
	return nil
}

func validateRepositoryMetadataForResolved(repository model.Repository, resolved Resolved, path string) error {
	if err := validateRepositoryMetadataVersion(repository, path); err != nil {
		return err
	}
	if repository.RepoID != resolved.RepoID {
		return fmt.Errorf("%w: repository identity %q has repo_id %q, want %q", ErrValidation, path, repository.RepoID, resolved.RepoID)
	}
	if repository.CanonicalRemote != "" && resolved.CanonicalRemote != "" && repository.CanonicalRemote != resolved.CanonicalRemote {
		return fmt.Errorf("%w: repository identity %q has canonical_remote %q, want %q", ErrValidation, path, repository.CanonicalRemote, resolved.CanonicalRemote)
	}
	return nil
}

func readRepositoryFile(path string) (model.Repository, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return model.Repository{}, false, nil
	}
	if err != nil {
		return model.Repository{}, false, err
	}
	var repository model.Repository
	if err := yaml.Unmarshal(data, &repository); err != nil {
		return model.Repository{}, false, err
	}
	return repository, true, nil
}

func validateCheckpointGraph(records []Record) error {
	byID := make(map[string]Record, len(records))
	for _, record := range records {
		id, err := recordString(record, "checkpoint_id")
		if err != nil {
			return err
		}
		if _, duplicate := byID[id]; duplicate {
			return fmt.Errorf("duplicate checkpoint ID %q", id)
		}
		byID[id] = record
	}
	for id, record := range byID {
		for _, parent := range stringSlice(record["parent_ids"]) {
			if _, exists := byID[parent]; !exists {
				return fmt.Errorf("checkpoint %q references missing parent %q", id, parent)
			}
		}
	}
	const (
		visiting = 1
		visited  = 2
	)
	state := make(map[string]int, len(byID))
	var visit func(string) error
	visit = func(id string) error {
		switch state[id] {
		case visiting:
			return fmt.Errorf("checkpoint graph contains a cycle at %q", id)
		case visited:
			return nil
		}
		state[id] = visiting
		for _, parent := range stringSlice(byID[id]["parent_ids"]) {
			if err := visit(parent); err != nil {
				return err
			}
		}
		state[id] = visited
		return nil
	}
	for id := range byID {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

func syncTaskIdentity(sourcePath, destinationPath, taskID string) error {
	source, destination, sourceExists, destinationExists, err := validateTaskIdentityFiles(sourcePath, destinationPath, taskID)
	if err != nil || !sourceExists {
		return err
	}
	if !destinationExists {
		destination = source
	} else {
		destination.SchemaVersion = model.SchemaVersion
		destination.TaskID = source.TaskID
		switch {
		case !source.IdentityRecovered && destination.IdentityRecovered:
			// A durable manifest restores identity that checkpoints cannot
			// recover, while keeping destination-derived graph metadata.
			destination.Title = source.Title
			destination.Aliases = append([]string(nil), source.Aliases...)
			destination.CreatedAt = source.CreatedAt
			destination.IdentityRecovered = false
		case source.IdentityRecovered && !destination.IdentityRecovered:
			// Never let provisional checkpoint-derived identity overwrite a
			// durable title, aliases, or creation time.
		case source.IdentityRecovered && destination.IdentityRecovered:
			if destination.Title == "" {
				destination.Title = source.Title
			}
			if destination.CreatedAt.IsZero() {
				destination.CreatedAt = source.CreatedAt
			}
		default:
			if destination.Title == "" {
				destination.Title = source.Title
			}
			if len(destination.Aliases) == 0 {
				destination.Aliases = append([]string(nil), source.Aliases...)
			}
			if destination.CreatedAt.IsZero() {
				destination.CreatedAt = source.CreatedAt
			}
		}
	}
	if destination.SchemaVersion == 0 {
		destination.SchemaVersion = model.SchemaVersion
	}
	destination.TaskID = source.TaskID
	data, err := yaml.Marshal(destination)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destinationPath), 0700); err != nil {
		return err
	}
	return atomicFile(destinationPath, data, 0644)
}

func validateTaskIdentityFiles(sourcePath, destinationPath, taskID string) (model.TaskManifest, model.TaskManifest, bool, bool, error) {
	source, exists, err := readManifestFile(sourcePath)
	if err != nil || !exists {
		return model.TaskManifest{}, model.TaskManifest{}, exists, false, err
	}
	if source.TaskID != taskID {
		return model.TaskManifest{}, model.TaskManifest{}, false, false, fmt.Errorf("task identity does not match directory %q", taskID)
	}
	destination, destinationExists, err := readManifestFile(destinationPath)
	if err != nil {
		return model.TaskManifest{}, model.TaskManifest{}, false, false, err
	}
	if destinationExists {
		if destination.TaskID != "" && destination.TaskID != source.TaskID {
			return model.TaskManifest{}, model.TaskManifest{}, false, false, fmt.Errorf("task identity conflict for %q: task ID differs", taskID)
		}
		if !source.IdentityRecovered && !destination.IdentityRecovered {
			if destination.Title != "" && source.Title != "" && destination.Title != source.Title {
				return model.TaskManifest{}, model.TaskManifest{}, false, false, fmt.Errorf("task identity conflict for %q: title differs", taskID)
			}
			if !destination.CreatedAt.IsZero() && !source.CreatedAt.IsZero() && !destination.CreatedAt.Equal(source.CreatedAt) {
				return model.TaskManifest{}, model.TaskManifest{}, false, false, fmt.Errorf("task identity conflict for %q: created_at differs", taskID)
			}
			if !sameAliases(destination.Aliases, source.Aliases) {
				return model.TaskManifest{}, model.TaskManifest{}, false, false, fmt.Errorf("task identity conflict for %q: aliases differ", taskID)
			}
		}
	}
	return source, destination, true, destinationExists, nil
}

func readManifestFile(path string) (model.TaskManifest, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return model.TaskManifest{}, false, nil
	}
	if err != nil {
		return model.TaskManifest{}, false, err
	}
	var manifest model.TaskManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return model.TaskManifest{}, false, err
	}
	if err := validateTaskManifestVersion(manifest, path); err != nil {
		return model.TaskManifest{}, false, err
	}
	return manifest, true, nil
}

func validateTaskManifestVersion(manifest model.TaskManifest, path string) error {
	if manifest.SchemaVersion != model.SchemaVersion {
		return fmt.Errorf("%w: task identity %q has unsupported schema_version %d", ErrValidation, path, manifest.SchemaVersion)
	}
	return nil
}

func sameAliases(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func atomicFile(path string, data []byte, perm fs.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".ctx-sync-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(perm); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}

func writeImmutableBytes(path string, data []byte, perm fs.FileMode) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return false, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".ctx-immutable-*")
	if err != nil {
		return false, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(perm); err != nil {
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
	return false, fmt.Errorf("%w: immutable unbound snapshot conflict at %s", ErrValidation, path)
}

func taskSummary(manifest model.TaskManifest) TaskSummary {
	statuses := make(map[string]string, len(manifest.HeadStatuses))
	for id, status := range manifest.HeadStatuses {
		statuses[id] = status
	}
	result := TaskSummary{TaskID: manifest.TaskID, Title: manifest.Title, Status: manifest.Status, Aliases: append([]string(nil), manifest.Aliases...), HeadIDs: append([]string(nil), manifest.HeadIDs...), StableHeadIDs: append([]string(nil), manifest.StableHeadIDs...), StableHeadID: manifest.StableHeadID, HeadStatuses: statuses}
	if !manifest.LastUsedAt.IsZero() {
		result.LastUsedAt = timestamp(manifest.LastUsedAt)
	}
	return result
}

func timestamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func taskIDOrDevice(taskID string) string {
	if taskID == "" {
		return unboundRuntimeID
	}
	return taskID
}

func validPurpose(value string) bool {
	switch value {
	case "progress", "milestone", "completion", "merge", "recovery":
		return true
	default:
		return false
	}
}

func validLocalRepositoryID(value string) bool {
	const prefix = "local-"
	if len(value) != len(prefix)+20 || !strings.HasPrefix(value, prefix) {
		return false
	}
	suffix := value[len(prefix):]
	if suffix != strings.ToLower(suffix) {
		return false
	}
	_, err := hex.DecodeString(suffix)
	return err == nil
}

func isNotFound(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, store.ErrNotFound) || errors.Is(err, ErrNotFound)
}

func storeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrValidation) || errors.Is(err, ErrAmbiguous) || errors.Is(err, ErrSync) || errors.Is(err, ErrGit) || errors.Is(err, ErrStore) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrStore, err)
}

func observationError(err error) error {
	if errors.Is(err, ErrStore) || errors.Is(err, ErrAmbiguous) || errors.Is(err, ErrValidation) || errors.Is(err, ErrNotFound) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrGit, err)
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func containsMapValue(values map[string]string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func asRecord(value any) Record {
	if record, ok := value.(map[string]any); ok {
		return record
	}
	return Record{}
}

func cloneRecord(record Record) Record {
	result := make(Record, len(record))
	for key, value := range record {
		result[key] = value
	}
	return result
}

func recordsFromAny(value any) []Record {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]Record, 0, len(items))
	for _, item := range items {
		if record, ok := item.(map[string]any); ok {
			result = append(result, record)
		}
	}
	return result
}

func recordsAny(records []Record) []any {
	result := make([]any, len(records))
	for i, record := range records {
		result[i] = record
	}
	return result
}

func stringsAny(values []string) []any {
	result := make([]any, len(values))
	for i, value := range values {
		result[i] = value
	}
	return result
}

func stringSliceAny(value any) []any {
	if items, ok := value.([]any); ok {
		return append([]any(nil), items...)
	}
	if items, ok := value.([]string); ok {
		return stringsAny(items)
	}
	return []any{}
}

func newULID(now time.Time) (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[6:]); err != nil {
		return "", err
	}
	millis := uint64(now.UTC().UnixMilli())
	for i := 5; i >= 0; i-- {
		data[i] = byte(millis)
		millis >>= 8
	}
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	value := new(big.Int).SetBytes(data[:])
	mask := big.NewInt(31)
	encoded := make([]byte, 26)
	for i := len(encoded) - 1; i >= 0; i-- {
		encoded[i] = alphabet[new(big.Int).And(value, mask).Int64()]
		value.Rsh(value, 5)
	}
	return string(encoded), nil
}
