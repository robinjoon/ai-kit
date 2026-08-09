package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestStartCheckpointAndResume(t *testing.T) {
	repository := testRepository(t)
	service, err := New(t.TempDir(), "com.openai.codex")
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.Start(repository, "Simplify ctx")
	if err != nil {
		t.Fatal(err)
	}
	if started.Checkpoint.Context.Goal != "Simplify ctx" {
		t.Fatalf("initial goal = %q", started.Checkpoint.Context.Goal)
	}

	saved, err := service.Checkpoint(repository, "progress", CheckpointInput{
		Goal: "Simplify ctx", Summary: "Removed distributed features.",
		Decisions: []string{"Keep one active context"}, NextActions: []string{"Run tests"},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := service.Resume(repository)
	if err != nil {
		t.Fatal(err)
	}
	if state.Latest.ID != saved.ID || state.Latest.Context.Summary != "Removed distributed features." {
		t.Fatalf("unexpected latest checkpoint: %#v", state.Latest)
	}
	if len(state.Differences) != 0 {
		t.Fatalf("unexpected Git differences: %v", state.Differences)
	}
}

func TestResumeReportsWorkingTreeChange(t *testing.T) {
	repository := testRepository(t)
	service, err := New(t.TempDir(), "com.anthropic.claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Start(repository, "Track Git changes"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "changed.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := service.Resume(repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Differences) != 1 || state.Differences[0] != "working tree changed since the checkpoint" {
		t.Fatalf("Git differences = %v", state.Differences)
	}
}

func TestStartReplacesTheSingleActiveContext(t *testing.T) {
	repository := testRepository(t)
	service, err := New(t.TempDir(), "ctx.test")
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Start(repository, "First")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Start(repository, "Second")
	if err != nil {
		t.Fatal(err)
	}
	if first.Active.ContextID == second.Active.ContextID {
		t.Fatal("new work reused the previous context ID")
	}
	state, err := service.Status(repository)
	if err != nil {
		t.Fatal(err)
	}
	if state.Active.Title != "Second" {
		t.Fatalf("active title = %q", state.Active.Title)
	}
}

func TestCheckpointRequiresActiveContext(t *testing.T) {
	repository := testRepository(t)
	service, err := New(t.TempDir(), "ctx.test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Checkpoint(repository, "progress", CheckpointInput{Goal: "Goal", Summary: "Summary"})
	if err != ErrNoActiveContext {
		t.Fatalf("error = %v, want ErrNoActiveContext", err)
	}
}

func TestBranchesKeepIndependentLatestContexts(t *testing.T) {
	repository := testRepository(t)
	service, err := New(t.TempDir(), "ctx.test")
	if err != nil {
		t.Fatal(err)
	}
	mainContext, err := service.Start(repository, "Main branch work")
	if err != nil {
		t.Fatal(err)
	}

	featureBranch := "feature/context-scope"
	runGit(t, repository, "checkout", "-q", "-b", featureBranch)
	if _, err := service.Status(repository); err != ErrNoActiveContext {
		t.Fatalf("new branch status error = %v, want ErrNoActiveContext", err)
	}
	featureContext, err := service.Start(repository, "Feature branch work")
	if err != nil {
		t.Fatal(err)
	}
	if service.scopeDir(mainContext.Scope) == service.scopeDir(featureContext.Scope) {
		t.Fatal("different branches shared one scope directory")
	}
	if _, err := os.Stat(service.scopePath(featureContext.Scope)); err != nil {
		t.Fatalf("feature scope metadata: %v", err)
	}

	runGit(t, repository, "checkout", "-q", mainContext.Scope.Branch)
	mainState, err := service.Status(repository)
	if err != nil {
		t.Fatal(err)
	}
	if mainState.Active.Title != "Main branch work" {
		t.Fatalf("main title = %q", mainState.Active.Title)
	}

	runGit(t, repository, "checkout", "-q", featureBranch)
	featureState, err := service.Status(repository)
	if err != nil {
		t.Fatal(err)
	}
	if featureState.Active.Title != "Feature branch work" {
		t.Fatalf("feature title = %q", featureState.Active.Title)
	}
}

func TestWorktreesKeepIndependentLatestContexts(t *testing.T) {
	repository := testRepository(t)
	service, err := New(t.TempDir(), "ctx.test")
	if err != nil {
		t.Fatal(err)
	}
	mainContext, err := service.Start(repository, "Main worktree")
	if err != nil {
		t.Fatal(err)
	}

	linkedWorktree := filepath.Join(t.TempDir(), "review-worktree")
	runGit(t, repository, "worktree", "add", "-q", "-b", "review/context-scope", linkedWorktree)
	linkedContext, err := service.Start(linkedWorktree, "Review worktree")
	if err != nil {
		t.Fatal(err)
	}
	if mainContext.Scope.RepositoryCommonDir != linkedContext.Scope.RepositoryCommonDir {
		t.Fatalf("common dirs differ: %q != %q", mainContext.Scope.RepositoryCommonDir, linkedContext.Scope.RepositoryCommonDir)
	}
	if mainContext.Scope.WorktreeRoot == linkedContext.Scope.WorktreeRoot {
		t.Fatal("linked worktree reused the main worktree root")
	}
	if service.scopeDir(mainContext.Scope) == service.scopeDir(linkedContext.Scope) {
		t.Fatal("different worktrees shared one scope directory")
	}

	mainState, err := service.Status(repository)
	if err != nil {
		t.Fatal(err)
	}
	linkedState, err := service.Status(linkedWorktree)
	if err != nil {
		t.Fatal(err)
	}
	if mainState.Active.Title != "Main worktree" || linkedState.Active.Title != "Review worktree" {
		t.Fatalf("titles = %q, %q", mainState.Active.Title, linkedState.Active.Title)
	}
}

func testRepository(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	runGit(t, repository, "init", "-q")
	runGit(t, repository, "config", "user.email", "ctx@example.com")
	runGit(t, repository, "config", "user.name", "ctx test")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", "README.md")
	runGit(t, repository, "commit", "-q", "-m", "initial")
	return repository
}

func runGit(t *testing.T, repository string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repository}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
