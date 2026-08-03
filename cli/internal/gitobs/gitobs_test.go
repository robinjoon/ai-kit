package gitobs

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/robinjoon/ai-kit/cli/internal/canonical"
)

func TestCanonicalRemote(t *testing.T) {
	cases := map[string]string{
		"git@GitHub.com:Owner/Repo.git":            "github.com/Owner/Repo",
		"GitHub.com:Owner/Repo.git":                "github.com/Owner/Repo",
		"ssh://git@github.com:22/Owner/Repo.git/":  "github.com/Owner/Repo",
		"ssh://git@github.com:2222/Owner/Repo.git": "github.com:2222/Owner/Repo",
		"https://GITHUB.com/Owner/Repo.git":        "github.com/Owner/Repo",
		"https://github.com:443/Owner/Repo.git":    "github.com/Owner/Repo",
		"https://github.com:8443/Owner/Repo.git":   "github.com:8443/Owner/Repo",
		"http://github.com:80/Owner/Repo.git":      "github.com/Owner/Repo",
	}
	for input, want := range cases {
		got, err := CanonicalRemote(input)
		if err != nil || got != want {
			t.Fatalf("CanonicalRemote(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, local := range []string{`C:\work\repo`, "C:/work/repo", "./host:path", "../host:path", "/tmp/host:path", "relative/path:repo"} {
		if got, err := CanonicalRemote(local); err == nil {
			t.Fatalf("CanonicalRemote(%q) = %q, want local-path rejection", local, got)
		}
	}
}

func TestResolveAndObserveDirtyRepository(t *testing.T) {
	repo := newRepo(t)
	runGit(t, repo, "remote", "add", "origin", "git@GitHub.com:Owner/Repo.git")
	write(t, filepath.Join(repo, "tracked.txt"), "one\n")
	runGit(t, repo, "add", "tracked.txt")
	runGit(t, repo, "commit", "-m", "initial")
	write(t, filepath.Join(repo, "tracked.txt"), "two\n")
	write(t, filepath.Join(repo, "new.txt"), "new\n")

	obs, err := Observe(filepath.Join(repo, "nested"))
	if err != nil {
		t.Fatal(err)
	}
	resolvedRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Repository.Root != resolvedRepo || obs.Repository.CanonicalRemote != "github.com/Owner/Repo" {
		t.Fatalf("unexpected repository: %#v", obs.Repository)
	}
	if !strings.HasPrefix(obs.Repository.RepoID, "repo-") || !strings.HasPrefix(obs.Repository.LocalRepoID, "local-") || !strings.HasPrefix(obs.Repository.WorkingCopyID, "workcopy-") {
		t.Fatalf("IDs violate expected shape: %#v", obs.Repository)
	}
	if obs.Snapshot.Head.State != "attached" || obs.Snapshot.Worktree.State != "dirty" {
		t.Fatalf("unexpected snapshot: %#v", obs.Snapshot)
	}
	if !strings.HasPrefix(obs.Snapshot.Worktree.Fingerprint, "sha256:") || len(obs.Snapshot.Worktree.Changes.Entries) != 2 {
		t.Fatalf("unexpected worktree: %#v", obs.Snapshot.Worktree)
	}
	again, err := Observe(repo)
	if err != nil {
		t.Fatal(err)
	}
	if again.Snapshot.Worktree.Fingerprint != obs.Snapshot.Worktree.Fingerprint {
		t.Fatal("unchanged repository produced a different fingerprint")
	}
}

func TestObserveDetectsStagedDeleteAndDetachedHead(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "old.txt"), "old\n")
	runGit(t, repo, "add", "old.txt")
	runGit(t, repo, "commit", "-m", "initial")
	runGit(t, repo, "rm", "old.txt")
	runGit(t, repo, "checkout", "--detach")

	obs, err := Observe(repo)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Snapshot.Head.State != "detached" {
		t.Fatalf("head = %#v", obs.Snapshot.Head)
	}
	if len(obs.Snapshot.Worktree.Changes.Entries) != 1 {
		t.Fatalf("changes = %#v", obs.Snapshot.Worktree.Changes)
	}
	c := obs.Snapshot.Worktree.Changes.Entries[0]
	if c.IndexStatus != "deleted" || c.WorktreeStatus != "unmodified" {
		t.Fatalf("change = %#v", c)
	}
	changes, paths, err := readChanges(repo)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := fingerprintPayload(repo, obs.Snapshot.ObjectFormat, obs.Snapshot.Head, obs.Snapshot.Operation, changes, paths)
	if err != nil {
		t.Fatal(err)
	}
	if got := decodedWorktrees(t, payload)["old.txt"].State; got != "missing" {
		t.Fatalf("staged deletion worktree state = %q, want missing", got)
	}
}

func TestCachedRemovalMergesDeletedIndexAndUntrackedWorktree(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "kept.txt"), "contents\n")
	runGit(t, repo, "add", "kept.txt")
	runGit(t, repo, "commit", "-m", "initial")
	runGit(t, repo, "rm", "--cached", "kept.txt")
	changes, paths, err := readChanges(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Entries) != 1 {
		t.Fatalf("changes = %#v, want one merged path", changes.Entries)
	}
	change := changes.Entries[0]
	if change.Path != "kept.txt" || change.IndexStatus != "deleted" || change.WorktreeStatus != "untracked" {
		t.Fatalf("merged change = %#v", change)
	}
	if len(paths) != 1 || string(paths["kept.txt"]) != "kept.txt" {
		t.Fatalf("raw paths = %#v", paths)
	}
	head, err := readHead(repo)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := fingerprintPayload(repo, "sha1", head, Operation{Kind: "none"}, changes, paths)
	if err != nil {
		t.Fatal(err)
	}
	if got := decodedWorktrees(t, payload)["kept.txt"].State; got != "present" {
		t.Fatalf("cached removal worktree state = %q, want present", got)
	}
}

func TestResolveRejectsAmbiguousRemotes(t *testing.T) {
	repo := newRepo(t)
	runGit(t, repo, "remote", "add", "one", "https://example.com/one.git")
	runGit(t, repo, "remote", "add", "two", "https://example.com/two.git")
	_, err := Resolve(repo)
	if !strings.Contains(err.Error(), ErrAmbiguousRemote.Error()) {
		t.Fatalf("Resolve error = %v", err)
	}
}

func TestResolveTreatsDotBranchRemoteAsLocal(t *testing.T) {
	repo := newRepo(t)
	runGit(t, repo, "remote", "add", "origin", "https://example.com/Owner/Repo.git")
	runGit(t, repo, "config", "branch.master.remote", ".")
	resolved, err := Resolve(repo)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.CanonicalRemote != "" || !strings.HasPrefix(resolved.RepoID, "local-") || resolved.LocalRepoID != resolved.RepoID {
		t.Fatalf("dot remote resolved as external: %#v", resolved)
	}
}

func TestLocalRepositoryIDSurvivesDirectoryMove(t *testing.T) {
	parent := t.TempDir()
	original := filepath.Join(parent, "original")
	newRepoAt(t, original)
	before, err := Resolve(original)
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(parent, "moved")
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	after, err := Resolve(moved)
	if err != nil {
		t.Fatal(err)
	}
	if before.RepoID != after.RepoID {
		t.Fatalf("repo ID changed after move: %q -> %q", before.RepoID, after.RepoID)
	}
	if before.WorkingCopyID == after.WorkingCopyID {
		t.Fatal("working copy ID did not reflect moved path")
	}
	other := filepath.Join(parent, "other")
	newRepoAt(t, other)
	separate, err := Resolve(other)
	if err != nil {
		t.Fatal(err)
	}
	if separate.RepoID == after.RepoID {
		t.Fatal("distinct local repositories received the same ID")
	}
}

func TestFingerprintPayloadUsesSpecifiedJSONKeys(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "tracked.txt"), "one\n")
	runGit(t, repo, "add", "tracked.txt")
	runGit(t, repo, "commit", "-m", "initial")
	write(t, filepath.Join(repo, "tracked.txt"), "two\n")
	changes, paths, err := readChanges(repo)
	if err != nil {
		t.Fatal(err)
	}
	head, err := readHead(repo)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := fingerprintPayload(repo, "sha1", head, Operation{Kind: "none"}, changes, paths)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, key := range []string{`"object_format":"sha1"`, `"symbolic_ref":"refs/heads/`, `"index_entries"`, `"path_b64"`, `"worktree_status"`} {
		if !strings.Contains(text, key) {
			t.Fatalf("payload missing %s: %s", key, text)
		}
	}
	canonicalBytes, err := canonical.JSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	obs, err := Observe(repo)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := obs.Snapshot.Worktree.Fingerprint, canonical.BytesDigest(canonicalBytes); got != want {
		t.Fatalf("fingerprint = %q, want JCS digest %q", got, want)
	}
}

func TestFingerprintArraysAreNeverNull(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "tracked.txt"), "one\n")
	runGit(t, repo, "add", "tracked.txt")
	runGit(t, repo, "commit", "-m", "initial")
	cleanChanges, cleanPaths, err := readChanges(repo)
	if err != nil {
		t.Fatal(err)
	}
	head, err := readHead(repo)
	if err != nil {
		t.Fatal(err)
	}
	clean, err := fingerprintPayload(repo, "sha1", head, Operation{Kind: "none"}, cleanChanges, cleanPaths)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(clean)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"entries":[]`) {
		t.Fatalf("clean entries are not an array: %s", encoded)
	}
	write(t, filepath.Join(repo, "untracked.txt"), "new\n")
	dirtyChanges, dirtyPaths, err := readChanges(repo)
	if err != nil {
		t.Fatal(err)
	}
	dirty, err := fingerprintPayload(repo, "sha1", head, Operation{Kind: "none"}, dirtyChanges, dirtyPaths)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(dirty)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"index_entries":[]`) {
		t.Fatalf("untracked index entries are not an array: %s", encoded)
	}
}

func TestUntrackedSymlinksHashLinkTargetBytes(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "target.txt"), "file contents must not be hashed\n")
	runGit(t, repo, "add", "target.txt")
	runGit(t, repo, "commit", "-m", "initial")
	links := map[string]string{"normal-link": "target.txt", "broken-link": "missing.txt"}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(repo, name)); err != nil {
			t.Fatal(err)
		}
	}
	changes, paths, err := readChanges(repo)
	if err != nil {
		t.Fatal(err)
	}
	head, err := readHead(repo)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := fingerprintPayload(repo, "sha1", head, Operation{Kind: "none"}, changes, paths)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Entries []struct {
			PathB64  string `json:"path_b64"`
			Worktree struct {
				Mode string `json:"mode"`
				OID  string `json:"oid"`
			} `json:"worktree"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Entries) != len(links) {
		t.Fatalf("symlink entries = %d, want %d: %s", len(decoded.Entries), len(links), encoded)
	}
	for _, entry := range decoded.Entries {
		path, err := base64.RawURLEncoding.DecodeString(entry.PathB64)
		if err != nil {
			t.Fatal(err)
		}
		target, ok := links[string(path)]
		if !ok {
			t.Fatalf("unexpected path %q", path)
		}
		if entry.Worktree.Mode != "120000" || entry.Worktree.OID != gitBlobSHA1(target) {
			t.Fatalf("symlink %q worktree = %#v", path, entry.Worktree)
		}
	}
}

func TestRegularModesUseOwnerExecuteBitWhenFileModeEnabled(t *testing.T) {
	repo := newRepo(t)
	runGit(t, repo, "config", "core.fileMode", "true")
	otherExecute := filepath.Join(repo, "other-execute")
	write(t, otherExecute, "one\n")
	if err := os.Chmod(otherExecute, 0641); err != nil {
		t.Fatal(err)
	}
	ownerExecute := filepath.Join(repo, "owner-execute")
	write(t, ownerExecute, "two\n")
	if err := os.Chmod(ownerExecute, 0744); err != nil {
		t.Fatal(err)
	}
	payload := currentFingerprintPayload(t, repo)
	worktrees := decodedWorktrees(t, payload)
	if got := worktrees["other-execute"].Mode; got != "100644" {
		t.Fatalf("other execute bit mode = %q, want 100644", got)
	}
	if got := worktrees["owner-execute"].Mode; got != "100755" {
		t.Fatalf("owner execute bit mode = %q, want 100755", got)
	}
}

func TestRegularModesRespectIndexWhenFileModeDisabled(t *testing.T) {
	repo := newRepo(t)
	tracked := filepath.Join(repo, "tracked")
	write(t, tracked, "one\n")
	if err := os.Chmod(tracked, 0755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "tracked")
	runGit(t, repo, "commit", "-m", "initial")
	runGit(t, repo, "config", "core.fileMode", "false")
	if err := os.Chmod(tracked, 0644); err != nil {
		t.Fatal(err)
	}
	write(t, tracked, "changed\n")
	untracked := filepath.Join(repo, "untracked")
	write(t, untracked, "new\n")
	if err := os.Chmod(untracked, 0755); err != nil {
		t.Fatal(err)
	}
	worktrees := decodedWorktrees(t, currentFingerprintPayload(t, repo))
	if got := worktrees["tracked"].Mode; got != "100755" {
		t.Fatalf("tracked mode = %q, want index mode 100755", got)
	}
	if got := worktrees["untracked"].Mode; got != "100644" {
		t.Fatalf("untracked mode = %q, want 100644", got)
	}
}

func TestWorktreeFailurePreservesGitFacts(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "tracked.txt"), "one\n")
	runGit(t, repo, "add", "tracked.txt")
	runGit(t, repo, "commit", "-m", "initial")
	unsupported := filepath.Join(repo, "unsupported")
	write(t, unsupported, "cannot read\n")
	if err := os.Chmod(unsupported, 0000); err != nil {
		t.Fatal(err)
	}
	obs, err := Observe(repo)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Snapshot.ObjectFormat != "sha1" || obs.Snapshot.Head.State != "attached" || obs.Snapshot.Operation.Kind != "none" {
		t.Fatalf("Git facts were discarded: %#v", obs.Snapshot)
	}
	if obs.Snapshot.Worktree.State != "unknown" || obs.Snapshot.Worktree.Changes.Complete || obs.Snapshot.Worktree.Changes.Entries == nil {
		t.Fatalf("failure was not represented as schema-compatible unknown: %#v", obs.Snapshot.Worktree)
	}
}

func TestObservationRetriesWhenEarlierFileChangesWhileLaterLargeFileIsCollected(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "a-first"), "initial\n")
	if err := os.WriteFile(filepath.Join(repo, "z-large"), bytes.Repeat([]byte("z"), 4<<20), 0644); err != nil {
		t.Fatal(err)
	}
	hookCalls := 0
	snapshot, err := observeWithHook(repo, func(attempt int, path string) {
		if path == "z-large" {
			hookCalls++
			if attempt == 1 {
				write(t, filepath.Join(repo, "a-first"), "changed during later hash\n")
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if hookCalls != 2 {
		t.Fatalf("large file collection attempts = %d, want one retry", hookCalls)
	}
	if snapshot.Worktree.State != "dirty" || !snapshot.Worktree.Changes.Complete {
		t.Fatalf("retry did not produce a stable observation: %#v", snapshot.Worktree)
	}
}

func TestObservationBecomesUnknownAfterRepeatedConcurrentChanges(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "a-first"), "initial\n")
	if err := os.WriteFile(filepath.Join(repo, "z-large"), bytes.Repeat([]byte("z"), 4<<20), 0644); err != nil {
		t.Fatal(err)
	}
	hookCalls := 0
	snapshot, err := observeWithHook(repo, func(attempt int, path string) {
		if path == "z-large" {
			hookCalls++
			write(t, filepath.Join(repo, "a-first"), strings.Repeat("changed", attempt))
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if hookCalls != maxObservationAttempts {
		t.Fatalf("large file collection attempts = %d, want %d", hookCalls, maxObservationAttempts)
	}
	if snapshot.Worktree.State != "unknown" || snapshot.Worktree.Changes.Complete || snapshot.Worktree.Fingerprint != "" || !strings.Contains(snapshot.Worktree.Diagnostic, "changed during observation") {
		t.Fatalf("unstable observation = %#v", snapshot.Worktree)
	}
}

func TestDirtySubmoduleUsesGitlinkFingerprintForm(t *testing.T) {
	source := newRepo(t)
	write(t, filepath.Join(source, "tracked.txt"), "one\n")
	runGit(t, source, "add", "tracked.txt")
	runGit(t, source, "commit", "-m", "initial")
	parent := newRepo(t)
	runGit(t, parent, "-c", "protocol.file.allow=always", "submodule", "add", source, "sub")
	runGit(t, parent, "commit", "-am", "add submodule")
	write(t, filepath.Join(parent, "sub", "tracked.txt"), "two\n")
	write(t, filepath.Join(parent, "sub", "untracked.txt"), "new\n")
	runGit(t, parent, "config", "submodule.sub.ignore", "all")
	changes, paths, err := readChanges(parent)
	if err != nil {
		t.Fatal(err)
	}
	head, err := readHead(parent)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := fingerprintPayload(parent, "sha1", head, Operation{Kind: "none"}, changes, paths)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := canonical.JSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"mode":"160000"`, `"object_kind":"gitlink"`, `"tracked_dirty":true`, `"untracked_dirty":true`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("submodule payload missing %s: %s", want, encoded)
		}
	}
}

func TestSubmoduleValueIncludesDirtyNestedSubmoduleIgnoredByConfig(t *testing.T) {
	leaf := newRepo(t)
	write(t, filepath.Join(leaf, "tracked.txt"), "one\n")
	runGit(t, leaf, "add", "tracked.txt")
	runGit(t, leaf, "commit", "-m", "initial")
	middle := newRepo(t)
	runGit(t, middle, "-c", "protocol.file.allow=always", "submodule", "add", leaf, "leaf")
	runGit(t, middle, "commit", "-am", "add nested submodule")
	runGit(t, middle, "config", "submodule.leaf.ignore", "all")
	untrackedPath := filepath.Join(middle, "leaf", "untracked.txt")
	write(t, untrackedPath, "new\n")
	untrackedValue, err := submoduleValue(middle)
	if err != nil {
		t.Fatal(err)
	}
	untrackedJSON, err := canonical.JSON(untrackedValue)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(untrackedJSON), `"tracked_dirty":false`) || !strings.Contains(string(untrackedJSON), `"untracked_dirty":true`) {
		t.Fatalf("nested untracked-only dirtiness was misclassified: %s", untrackedJSON)
	}
	if err := os.Remove(untrackedPath); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(middle, "leaf", "tracked.txt"), "changed\n")
	trackedValue, err := submoduleValue(middle)
	if err != nil {
		t.Fatal(err)
	}
	trackedJSON, err := canonical.JSON(trackedValue)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(trackedJSON), `"tracked_dirty":true`) || !strings.Contains(string(trackedJSON), `"untracked_dirty":false`) {
		t.Fatalf("nested tracked-only dirtiness was misclassified: %s", trackedJSON)
	}
	if bytes.Equal(untrackedJSON, trackedJSON) {
		t.Fatalf("tracked-only and untracked-only nested states collided: %s", trackedJSON)
	}
}

func TestSubmoduleConflictStagesUseGitlinkFormWithoutStageZero(t *testing.T) {
	root := t.TempDir()
	submodule := filepath.Join(root, "sub")
	newRepoAt(t, submodule)
	write(t, filepath.Join(submodule, "tracked.txt"), "one\n")
	runGit(t, submodule, "add", "tracked.txt")
	runGit(t, submodule, "commit", "-m", "initial")
	oid, err := run(submodule, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	stages := []indexEntry{{Stage: 1, Mode: "160000", OID: strings.TrimSpace(oid)}, {Stage: 2, Mode: "160000", OID: strings.TrimSpace(oid)}}
	value, err := worktreeValue(root, Change{Path: "sub", IndexStatus: "unmerged", WorktreeStatus: "unmerged", Conflict: true}, stages)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := canonical.JSON(value)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"mode":"160000"`) || !strings.Contains(string(encoded), `"object_kind":"gitlink"`) {
		t.Fatalf("conflicted submodule was not represented as gitlink: %s", encoded)
	}
}

func TestConflictWithoutStageZeroUsesPorcelainWorktreeMode(t *testing.T) {
	repo := newRepo(t)
	runGit(t, repo, "config", "core.fileMode", "false")
	write(t, filepath.Join(repo, "conflicted"), "worktree\n")
	oid := strings.Repeat("1", 40)
	porcelain := []byte(fmt.Sprintf("u UU N... 100755 100755 100755 100644 %s %s %s conflicted\x00", oid, oid, oid))
	changes, _, err := parseChanges(porcelain)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Entries) != 1 || changes.Entries[0].worktreeMode != "100644" {
		t.Fatalf("parsed conflict = %#v", changes.Entries)
	}
	change := changes.Entries[0]
	stages := []indexEntry{{Stage: 1, Mode: "100755", OID: strings.Repeat("1", 40)}, {Stage: 2, Mode: "100755", OID: strings.Repeat("2", 40)}}
	value, err := worktreeValue(repo, change, stages)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := canonical.JSON(value)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`{"mode":"100644","object_kind":"blob","oid":"%s","state":"present"}`, gitBlobSHA1("worktree\n"))
	if string(encoded) != want {
		t.Fatalf("conflict worktree = %s, want %s", encoded, want)
	}
	public, err := json.Marshal(change)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(public), "100644") || strings.Contains(string(public), "worktreeMode") {
		t.Fatalf("internal worktree mode leaked through public change: %s", public)
	}
}

func TestObserveReportsMergeOperation(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "base.txt"), "base\n")
	runGit(t, repo, "add", "base.txt")
	runGit(t, repo, "commit", "-m", "base")
	runGit(t, repo, "checkout", "-b", "feature")
	write(t, filepath.Join(repo, "feature.txt"), "feature\n")
	runGit(t, repo, "add", "feature.txt")
	runGit(t, repo, "commit", "-m", "feature")
	runGit(t, repo, "checkout", "master")
	write(t, filepath.Join(repo, "main.txt"), "main\n")
	runGit(t, repo, "add", "main.txt")
	runGit(t, repo, "commit", "-m", "main")
	runGit(t, repo, "merge", "--no-commit", "--no-ff", "feature")
	obs, err := Observe(repo)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Snapshot.Operation.Kind != "merge" {
		t.Fatalf("operation = %#v", obs.Snapshot.Operation)
	}
}

func TestReadOperationDistinguishesGitAmFromRebaseApply(t *testing.T) {
	repo := newRepo(t)
	gitDir, err := run(repo, "rev-parse", "--git-dir")
	if err != nil {
		t.Fatal(err)
	}
	applyDir := filepath.Join(repo, strings.TrimSpace(gitDir), "rebase-apply")
	if err := os.MkdirAll(applyDir, 0755); err != nil {
		t.Fatal(err)
	}
	applying := filepath.Join(applyDir, "applying")
	write(t, applying, "")
	operation := readOperation(repo)
	if operation.Kind != "other" || strings.TrimSpace(operation.Detail) == "" {
		t.Fatalf("git am operation = %#v", operation)
	}
	if err := os.Remove(applying); err != nil {
		t.Fatal(err)
	}
	if operation := readOperation(repo); operation.Kind != "rebase" {
		t.Fatalf("rebase-apply operation = %#v", operation)
	}
}

func TestObservationDoesNotRefreshIndexStatCache(t *testing.T) {
	repo := newRepo(t)
	tracked := filepath.Join(repo, "tracked.txt")
	write(t, tracked, "contents\n")
	runGit(t, repo, "add", "tracked.txt")
	runGit(t, repo, "commit", "-m", "initial")
	indexName, err := run(repo, "rev-parse", "--git-path", "index")
	if err != nil {
		t.Fatal(err)
	}
	indexPath := strings.TrimSpace(indexName)
	if !filepath.IsAbs(indexPath) {
		indexPath = filepath.Join(repo, indexPath)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(tracked, future, future); err != nil {
		t.Fatal(err)
	}
	beforeBytes, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_OPTIONAL_LOCKS", "1")
	observation, err := Observe(repo)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Snapshot.Worktree.State != "clean" {
		t.Fatalf("touch-only observation = %#v", observation.Snapshot.Worktree)
	}
	afterBytes, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeBytes, afterBytes) || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) || !os.SameFile(beforeInfo, afterInfo) {
		t.Fatalf("observation refreshed index stat cache: bytes_equal=%t mtime_before=%s mtime_after=%s same_file=%t", bytes.Equal(beforeBytes, afterBytes), beforeInfo.ModTime(), afterInfo.ModTime(), os.SameFile(beforeInfo, afterInfo))
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	newRepoAt(t, repo)
	return repo
}

func newRepoAt(t *testing.T, repo string) {
	t.Helper()
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "ctx@example.test")
	runGit(t, repo, "config", "user.name", "ctx test")
	if err := os.Mkdir(filepath.Join(repo, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
}
func write(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0644); err != nil {
		t.Fatal(err)
	}
}

func gitBlobSHA1(content string) string {
	payload := append([]byte(fmt.Sprintf("blob %d\x00", len(content))), []byte(content)...)
	digest := sha1.Sum(payload)
	return hex.EncodeToString(digest[:])
}

type decodedWorktree struct {
	State string `json:"state"`
	Mode  string `json:"mode"`
	OID   string `json:"oid"`
}

func currentFingerprintPayload(t *testing.T, repo string) fingerprint {
	t.Helper()
	changes, paths, err := readChanges(repo)
	if err != nil {
		t.Fatal(err)
	}
	head, err := readHead(repo)
	if err != nil {
		t.Fatal(err)
	}
	format, err := run(repo, "rev-parse", "--show-object-format")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := fingerprintPayload(repo, strings.TrimSpace(format), head, readOperation(repo), changes, paths)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func decodedWorktrees(t *testing.T, payload fingerprint) map[string]decodedWorktree {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Entries []struct {
			PathB64  string          `json:"path_b64"`
			Worktree decodedWorktree `json:"worktree"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	result := make(map[string]decodedWorktree, len(decoded.Entries))
	for _, entry := range decoded.Entries {
		path, err := base64.RawURLEncoding.DecodeString(entry.PathB64)
		if err != nil {
			t.Fatal(err)
		}
		result[string(path)] = entry.Worktree
	}
	return result
}

func runGit(t *testing.T, cwd string, args ...string) {
	t.Helper()
	if _, err := run(cwd, args...); err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
}
