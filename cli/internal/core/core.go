package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const DataVersion = 1

var ErrNoActiveContext = errors.New("no active ctx context; run ctx start first")

// Decision records what was decided and the reasoning behind it.
type Decision struct {
	What string `json:"what"`
	Why  string `json:"why"`
}

type CheckpointInput struct {
	Goal        string     `json:"goal"`
	Summary     string     `json:"summary"`
	Decisions   []Decision `json:"decisions,omitempty"`
	NextActions []string   `json:"next_actions,omitempty"`
	Blockers    []string   `json:"blockers,omitempty"`
}

type GitState struct {
	Branch string `json:"branch"`
	Head   string `json:"head"`
	Status string `json:"status"`
}

type GitScope struct {
	Version             int    `json:"version"`
	RepositoryCommonDir string `json:"repository_common_dir"`
	WorktreeRoot        string `json:"worktree_root"`
	Branch              string `json:"branch"`
	DetachedHead        string `json:"detached_head,omitempty"`
}

type ActiveContext struct {
	Version            int       `json:"version"`
	ContextID          string    `json:"context_id"`
	Title              string    `json:"title"`
	WorktreeRoot       string    `json:"worktree_root"`
	StartedAt          time.Time `json:"started_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	LatestCheckpointID string    `json:"latest_checkpoint_id"`
}

type Checkpoint struct {
	Version   int             `json:"version"`
	ID        string          `json:"id"`
	ContextID string          `json:"context_id"`
	Title     string          `json:"title"`
	CreatedAt time.Time       `json:"created_at"`
	Client    string          `json:"client"`
	Reason    string          `json:"reason"`
	Git       GitState        `json:"git"`
	Context   CheckpointInput `json:"context"`
}

type StartResult struct {
	Scope      GitScope      `json:"scope"`
	Active     ActiveContext `json:"active"`
	Checkpoint Checkpoint    `json:"checkpoint"`
}

type State struct {
	Scope       GitScope      `json:"scope"`
	Active      ActiveContext `json:"active"`
	Latest      Checkpoint    `json:"latest"`
	CurrentGit  GitState      `json:"current_git"`
	Differences []string      `json:"differences"`
}

type Service struct {
	storeRoot string
	client    string
}

func New(storeRoot, client string) (*Service, error) {
	if storeRoot == "" {
		configRoot, err := os.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("resolve user config directory: %w", err)
		}
		storeRoot = filepath.Join(configRoot, "ctx")
	}
	if client == "" {
		client = "ctx.cli"
	}
	return &Service{storeRoot: storeRoot, client: client}, nil
}

func (s *Service) Start(cwd, title string) (StartResult, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return StartResult{}, errors.New("title is required")
	}
	scope, gitState, err := observe(cwd)
	if err != nil {
		return StartResult{}, err
	}
	now := time.Now().UTC()
	contextID, err := newID("context", now)
	if err != nil {
		return StartResult{}, err
	}
	checkpointID, err := newID("checkpoint", now)
	if err != nil {
		return StartResult{}, err
	}
	checkpoint := Checkpoint{
		Version: DataVersion, ID: checkpointID, ContextID: contextID, Title: title,
		CreatedAt: now, Client: s.client, Reason: "start", Git: gitState,
		Context: CheckpointInput{Goal: title, Summary: "Work context started."},
	}
	active := ActiveContext{
		Version: DataVersion, ContextID: contextID, Title: title, WorktreeRoot: scope.WorktreeRoot,
		StartedAt: now, UpdatedAt: now, LatestCheckpointID: checkpointID,
	}
	if err := writeJSONAtomic(s.scopePath(scope), scope); err != nil {
		return StartResult{}, err
	}
	if err := s.writeCheckpoint(scope, checkpoint); err != nil {
		return StartResult{}, err
	}
	if err := writeJSONAtomic(s.activePath(scope), active); err != nil {
		return StartResult{}, err
	}
	return StartResult{Scope: scope, Active: active, Checkpoint: checkpoint}, nil
}

func (s *Service) Checkpoint(cwd, reason string, input CheckpointInput) (Checkpoint, error) {
	input = cleanInput(input)
	if input.Goal == "" || input.Summary == "" {
		return Checkpoint{}, errors.New("checkpoint goal and summary are required")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "progress"
	}
	scope, gitState, err := observe(cwd)
	if err != nil {
		return Checkpoint{}, err
	}
	active, err := s.readActive(scope)
	if err != nil {
		return Checkpoint{}, err
	}
	now := time.Now().UTC()
	id, err := newID("checkpoint", now)
	if err != nil {
		return Checkpoint{}, err
	}
	checkpoint := Checkpoint{
		Version: DataVersion, ID: id, ContextID: active.ContextID, Title: active.Title,
		CreatedAt: now, Client: s.client, Reason: reason, Git: gitState, Context: input,
	}
	if err := writeJSONAtomic(s.scopePath(scope), scope); err != nil {
		return Checkpoint{}, err
	}
	if err := s.writeCheckpoint(scope, checkpoint); err != nil {
		return Checkpoint{}, err
	}
	active.LatestCheckpointID = id
	active.UpdatedAt = now
	if err := writeJSONAtomic(s.activePath(scope), active); err != nil {
		return Checkpoint{}, err
	}
	return checkpoint, nil
}

func (s *Service) Resume(cwd string) (State, error) {
	return s.state(cwd)
}

func (s *Service) Status(cwd string) (State, error) {
	return s.state(cwd)
}

func (s *Service) state(cwd string) (State, error) {
	scope, currentGit, err := observe(cwd)
	if err != nil {
		return State{}, err
	}
	active, err := s.readActive(scope)
	if err != nil {
		return State{}, err
	}
	latest, err := s.readCheckpoint(scope, active.LatestCheckpointID)
	if err != nil {
		return State{}, err
	}
	return State{
		Scope: scope, Active: active, Latest: latest, CurrentGit: currentGit,
		Differences: compareGit(latest.Git, currentGit),
	}, nil
}

func cleanInput(input CheckpointInput) CheckpointInput {
	input.Goal = strings.TrimSpace(input.Goal)
	input.Summary = strings.TrimSpace(input.Summary)
	input.Decisions = cleanDecisions(input.Decisions)
	input.NextActions = cleanList(input.NextActions)
	input.Blockers = cleanList(input.Blockers)
	return input
}

func cleanDecisions(decisions []Decision) []Decision {
	result := make([]Decision, 0, len(decisions))
	for _, d := range decisions {
		d.What = strings.TrimSpace(d.What)
		d.Why = strings.TrimSpace(d.Why)
		if d.What != "" {
			result = append(result, d)
		}
	}
	return result
}

func cleanList(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func compareGit(saved, current GitState) []string {
	var differences []string
	if saved.Branch != current.Branch {
		differences = append(differences, fmt.Sprintf("branch changed: %s -> %s", display(saved.Branch), display(current.Branch)))
	}
	if saved.Head != current.Head {
		differences = append(differences, fmt.Sprintf("HEAD changed: %s -> %s", display(saved.Head), display(current.Head)))
	}
	if saved.Status != current.Status {
		differences = append(differences, "working tree changed since the checkpoint")
	}
	return differences
}

func display(value string) string {
	if value == "" {
		return "(none)"
	}
	return value
}

func observe(cwd string) (GitScope, GitState, error) {
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return GitScope{}, GitState{}, fmt.Errorf("get working directory: %w", err)
		}
	}
	rootBytes, err := git(cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return GitScope{}, GitState{}, errors.New("current directory is not inside a Git repository")
	}
	root := filepath.Clean(strings.TrimSpace(string(rootBytes)))
	commonDirBytes, err := git(root, "rev-parse", "--git-common-dir")
	if err != nil {
		return GitScope{}, GitState{}, errors.New("cannot resolve the Git common directory")
	}
	commonDir := strings.TrimSpace(string(commonDirBytes))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(root, commonDir)
	}
	commonDir = filepath.Clean(commonDir)
	branchBytes, branchErr := git(root, "symbolic-ref", "--quiet", "--short", "HEAD")
	branch := "detached"
	if branchErr == nil {
		branch = strings.TrimSpace(string(branchBytes))
	}
	headBytes, headErr := git(root, "rev-parse", "--verify", "HEAD")
	head := ""
	if headErr == nil {
		head = strings.TrimSpace(string(headBytes))
	}
	statusBytes, err := git(root, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return GitScope{}, GitState{}, fmt.Errorf("read Git status: %w", err)
	}
	scope := GitScope{
		Version: DataVersion, RepositoryCommonDir: commonDir,
		WorktreeRoot: root, Branch: branch,
	}
	if branch == "detached" {
		scope.DetachedHead = head
	}
	return scope, GitState{Branch: branch, Head: head, Status: strings.TrimSpace(string(statusBytes))}, nil
}

func git(cwd string, args ...string) ([]byte, error) {
	command := exec.Command("git", append([]string{"-C", cwd}, args...)...)
	return command.Output()
}

func (s *Service) scopeDir(scope GitScope) string {
	repositoryLabel := filepath.Base(scope.WorktreeRoot)
	if filepath.Base(scope.RepositoryCommonDir) == ".git" {
		repositoryLabel = filepath.Base(filepath.Dir(scope.RepositoryCommonDir))
	}
	branchIdentity := "branch:" + scope.Branch
	branchLabel := scope.Branch
	if scope.Branch == "detached" {
		branchIdentity = "detached:" + scope.DetachedHead
		branchLabel = "detached"
		if len(scope.DetachedHead) >= 8 {
			branchLabel += "-" + scope.DetachedHead[:8]
		}
	}
	return filepath.Join(
		s.storeRoot,
		"repos", pathKey(repositoryLabel, scope.RepositoryCommonDir),
		"worktrees", pathKey(filepath.Base(scope.WorktreeRoot), scope.WorktreeRoot),
		"branches", pathKey(branchLabel, branchIdentity),
	)
}

func (s *Service) scopePath(scope GitScope) string {
	return filepath.Join(s.scopeDir(scope), "scope.json")
}

func (s *Service) activePath(scope GitScope) string {
	return filepath.Join(s.scopeDir(scope), "active.json")
}

func (s *Service) checkpointPath(scope GitScope, id string) string {
	return filepath.Join(s.scopeDir(scope), "checkpoints", id+".json")
}

func (s *Service) readActive(scope GitScope) (ActiveContext, error) {
	var active ActiveContext
	if err := readJSON(s.activePath(scope), &active); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return active, ErrNoActiveContext
		}
		return active, fmt.Errorf("read active context: %w", err)
	}
	if active.Version != DataVersion || active.WorktreeRoot != scope.WorktreeRoot || active.LatestCheckpointID == "" {
		return active, errors.New("active context is invalid")
	}
	return active, nil
}

func (s *Service) readCheckpoint(scope GitScope, id string) (Checkpoint, error) {
	var checkpoint Checkpoint
	if err := readJSON(s.checkpointPath(scope, id), &checkpoint); err != nil {
		return checkpoint, fmt.Errorf("read latest checkpoint: %w", err)
	}
	if checkpoint.Version != DataVersion || checkpoint.ID != id {
		return checkpoint, errors.New("latest checkpoint is invalid")
	}
	return checkpoint, nil
}

func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func (s *Service) writeCheckpoint(scope GitScope, checkpoint Checkpoint) error {
	path := s.checkpointPath(scope, checkpoint.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create checkpoint directory: %w", err)
	}
	data, err := json.MarshalIndent(checkpoint, "", "  ")
	if err != nil {
		return fmt.Errorf("encode checkpoint: %w", err)
	}
	data = append(data, '\n')
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create checkpoint: %w", err)
	}
	if _, err = file.Write(data); err == nil {
		err = file.Close()
	} else {
		_ = file.Close()
	}
	if err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("write checkpoint: %w", err)
	}
	return nil
}

func writeJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create store directory: %w", err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".active-*")
	if err != nil {
		return fmt.Errorf("create temporary state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary state: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish state: %w", err)
	}
	return nil
}

func newID(prefix string, now time.Time) (string, error) {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate ID: %w", err)
	}
	return fmt.Sprintf("%s-%s-%s", prefix, now.Format("20060102T150405.000000000Z"), hex.EncodeToString(random)), nil
}

func pathKey(label, identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return safeLabel(label) + "-" + hex.EncodeToString(sum[:4])
}

func safeLabel(value string) string {
	var result strings.Builder
	dash := false
	for _, char := range strings.ToLower(value) {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			if dash && result.Len() > 0 {
				result.WriteByte('-')
			}
			result.WriteRune(char)
			dash = false
		} else {
			dash = true
		}
		if result.Len() >= 32 {
			break
		}
	}
	label := strings.Trim(result.String(), "-")
	if label == "" {
		return "scope"
	}
	return label
}
