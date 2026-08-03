// Package gitobs observes a Git working copy without changing it.
package gitobs

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"

	"github.com/robinjoon/ai-kit/cli/internal/canonical"
)

var (
	ErrNotRepository    = errors.New("not a Git repository")
	ErrAmbiguousRemote  = errors.New("ambiguous Git remote")
	errUnstableWorktree = errors.New("Git working copy changed during observation")
)

const maxObservationAttempts = 3

// Repository identifies one local working copy and its portable repository ID.
type Repository struct {
	Root            string
	CanonicalRemote string
	RepoID          string
	LocalRepoID     string
	WorkingCopyID   string
}

type Head struct {
	State       string `json:"state"`
	SymbolicRef string `json:"symbolic_ref,omitempty"`
	OID         string `json:"oid,omitempty"`
}
type Upstream struct {
	Remote, Ref, OID string
	Ahead, Behind    int
}
type Operation struct{ Kind, Detail string }
type Change struct {
	Path, OriginalPath, IndexStatus, WorktreeStatus string
	Conflict                                        bool
	worktreeMode                                    string
}
type Changes struct {
	Complete, UntrackedIncluded, IgnoredIncluded bool
	Entries                                      []Change
}
type Worktree struct {
	State, Fingerprint, Diagnostic string
	Changes                        Changes
}
type Snapshot struct {
	ObjectFormat string
	Head         Head
	Upstream     *Upstream
	Operation    Operation
	Worktree     Worktree
}

// Compare describes differences useful for resume/status rendering.
type Comparison struct{ HeadChanged, WorktreeChanged, OperationChanged bool }

func Compare(a, b Snapshot) Comparison {
	return Comparison{HeadChanged: a.Head != b.Head, WorktreeChanged: a.Worktree.State != b.Worktree.State || a.Worktree.Fingerprint != b.Worktree.Fingerprint, OperationChanged: a.Operation.Kind != b.Operation.Kind}
}

// Resolve finds the enclosing worktree and chooses its configured primary remote.
func Resolve(cwd string) (Repository, error) {
	root, err := run(cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return Repository{}, fmt.Errorf("%w: %v", ErrNotRepository, err)
	}
	root = strings.TrimSpace(root)
	abs, err := filepath.Abs(root)
	if err != nil {
		return Repository{}, err
	}
	remote, err := primaryRemote(abs)
	if err != nil {
		return Repository{}, err
	}
	localIdentity, err := localRepositoryIdentity(abs)
	if err != nil {
		return Repository{}, err
	}
	localRepoID := "local-" + shortDigest(localIdentity)
	repoID := localRepoID
	canonical := ""
	if remote != "" {
		canonical, err = CanonicalRemote(remote)
		if err != nil {
			return Repository{}, err
		}
		repoID = "repo-" + shortDigest(canonical)
	}
	return Repository{Root: abs, CanonicalRemote: canonical, RepoID: repoID, LocalRepoID: localRepoID, WorkingCopyID: "workcopy-" + shortDigest(abs)}, nil
}

// Observation combines repository identity with the Git facts needed by both
// a checkpoint baseline and a runtime snapshot.
type Observation struct {
	Repository Repository
	Snapshot   Snapshot
}

// Observe resolves cwd and obtains a complete, read-only point-in-time observation.
// A failed Git collection is represented as an unknown worktree; identity failures
// (for example cwd outside a repository) are returned to the caller.
func Observe(cwd string) (Observation, error) {
	repo, err := Resolve(cwd)
	if err != nil {
		return Observation{}, err
	}
	s, err := observe(repo.Root)
	if err != nil {
		s = Snapshot{Worktree: Worktree{State: "unknown", Diagnostic: err.Error(), Changes: Changes{Complete: false}}}
	}
	return Observation{Repository: repo, Snapshot: s}, nil
}

func observe(root string) (Snapshot, error) {
	return observeWithHook(root, nil)
}

func observeWithHook(root string, beforeHash func(attempt int, path string)) (Snapshot, error) {
	format, err := run(root, "rev-parse", "--show-object-format")
	if err != nil {
		return Snapshot{}, err
	}
	objectFormat := strings.TrimSpace(format)
	var lastSnapshot Snapshot
	var lastChanges Changes
	var lastErr error
	for attempt := 1; attempt <= maxObservationAttempts; attempt++ {
		head, err := readHead(root)
		if err != nil {
			return Snapshot{}, err
		}
		operation := readOperation(root)
		snapshot := Snapshot{ObjectFormat: objectFormat, Head: head, Upstream: readUpstream(root), Operation: operation}
		lastSnapshot = snapshot
		indexBefore, err := readIndexIdentity(root)
		if err != nil {
			lastErr = err
			continue
		}
		changes, rawPaths, err := readChanges(root)
		if err != nil {
			lastErr = err
			continue
		}
		lastChanges = changes
		statsBefore, err := statChanges(root, changes)
		if err != nil {
			lastErr = err
			continue
		}
		var hook func(string)
		if beforeHash != nil {
			hook = func(path string) { beforeHash(attempt, path) }
		}
		payload, err := fingerprintPayloadWithHook(root, objectFormat, head, operation, changes, rawPaths, hook)
		if err != nil {
			lastErr = err
			continue
		}
		statsAfter, err := statChanges(root, changes)
		if err != nil {
			lastErr = err
			continue
		}
		indexAfter, err := readIndexIdentity(root)
		if err != nil {
			lastErr = err
			continue
		}
		headAfter, err := readHead(root)
		if err != nil {
			lastErr = err
			continue
		}
		changesAfter, rawPathsAfter, err := readChanges(root)
		if err != nil {
			lastErr = err
			continue
		}
		if head != headAfter || operation != readOperation(root) || indexBefore != indexAfter || !reflect.DeepEqual(statsBefore, statsAfter) || !reflect.DeepEqual(changes, changesAfter) || !reflect.DeepEqual(rawPaths, rawPathsAfter) {
			lastErr = errUnstableWorktree
			continue
		}
		b, err := canonical.JSON(payload)
		if err != nil {
			lastErr = err
			continue
		}
		worktree := Worktree{Changes: changes, Fingerprint: canonical.BytesDigest(b), State: "dirty"}
		if len(changes.Entries) == 0 {
			worktree.State = "clean"
		}
		snapshot.Worktree = worktree
		return snapshot, nil
	}
	if lastErr == nil {
		lastErr = errUnstableWorktree
	}
	diagnostic := fmt.Errorf("Git observation failed after %d attempts: %w", maxObservationAttempts, lastErr)
	if errors.Is(lastErr, errUnstableWorktree) {
		diagnostic = fmt.Errorf("%w after %d attempts", errUnstableWorktree, maxObservationAttempts)
	}
	lastSnapshot.Worktree = unknownWorktree(diagnostic, lastChanges)
	return lastSnapshot, nil
}

func unknownWorktree(err error, changes Changes) Worktree {
	changes.Complete = false
	if changes.Entries == nil {
		changes.Entries = []Change{}
	}
	return Worktree{State: "unknown", Diagnostic: err.Error(), Changes: changes}
}

func primaryRemote(root string) (string, error) {
	branch, _ := run(root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if branch = strings.TrimSpace(branch); branch != "" {
		if remote, err := run(root, "config", "--get", "branch."+branch+".remote"); err == nil && strings.TrimSpace(remote) != "" {
			remote = strings.TrimSpace(remote)
			if remote == "." {
				return "", nil
			}
			return remoteURL(root, remote)
		}
	}
	remotes, err := run(root, "remote")
	if err != nil {
		return "", err
	}
	items := strings.Fields(remotes)
	for _, name := range items {
		if name == "origin" {
			return remoteURL(root, name)
		}
	}
	if len(items) == 0 {
		return "", nil
	}
	if len(items) == 1 {
		return remoteURL(root, items[0])
	}
	return "", ErrAmbiguousRemote
}
func remoteURL(root, name string) (string, error) { return run(root, "remote", "get-url", name) }

// CanonicalRemote makes SSH, SCP and HTTP forms of a remote comparable.
func CanonicalRemote(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("empty remote URL")
	}
	if host, path, ok := scpRemoteParts(raw); ok {
		return canonicalParts(host, path), nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse remote URL: %w", err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("remote URL lacks host: %q", raw)
	}
	host := u.Hostname()
	port := u.Port()
	defaultPort := (u.Scheme == "ssh" && port == "22") || (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443")
	if port != "" && !defaultPort {
		host = net.JoinHostPort(host, port)
	}
	return canonicalParts(host, u.Path), nil
}

func scpRemoteParts(raw string) (string, string, bool) {
	colon := strings.IndexByte(raw, ':')
	if colon <= 0 || strings.Contains(raw, "://") || strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "./") || strings.HasPrefix(raw, "../") || strings.HasPrefix(raw, "~/") {
		return "", "", false
	}
	prefix := raw[:colon]
	if len(prefix) == 1 && ((prefix[0] >= 'A' && prefix[0] <= 'Z') || (prefix[0] >= 'a' && prefix[0] <= 'z')) {
		return "", "", false
	}
	if strings.ContainsAny(prefix, `/\`) || colon == len(raw)-1 {
		return "", "", false
	}
	host := prefix
	if at := strings.LastIndexByte(prefix, '@'); at >= 0 {
		host = prefix[at+1:]
	}
	if host == "" {
		return "", "", false
	}
	return host, raw[colon+1:], true
}
func canonicalParts(host, path string) string {
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	return strings.ToLower(host) + "/" + path
}
func shortDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:20]
}

func localRepositoryIdentity(root string) (string, error) {
	commonDir, err := run(root, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(commonDir)
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat Git common directory: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", errors.New("filesystem identity is unavailable for Git common directory")
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), nil
}

func readHead(root string) (Head, error) {
	ref, _ := run(root, "symbolic-ref", "--quiet", "HEAD")
	oid, oidErr := run(root, "rev-parse", "--verify", "HEAD")
	if strings.TrimSpace(ref) != "" {
		if oidErr != nil {
			return Head{State: "unborn", SymbolicRef: strings.TrimSpace(ref)}, nil
		}
		return Head{State: "attached", SymbolicRef: strings.TrimSpace(ref), OID: strings.TrimSpace(oid)}, nil
	}
	if oidErr != nil {
		return Head{}, oidErr
	}
	return Head{State: "detached", OID: strings.TrimSpace(oid)}, nil
}
func readUpstream(root string) *Upstream {
	ref, err := run(root, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if err != nil {
		return nil
	}
	ref = strings.TrimSpace(ref)
	parts := strings.SplitN(strings.TrimPrefix(ref, "refs/remotes/"), "/", 2)
	if len(parts) != 2 {
		return nil
	}
	oid, _ := run(root, "rev-parse", "@{upstream}")
	counts, err := run(root, "rev-list", "--left-right", "--count", "HEAD...@{upstream}")
	if err != nil {
		return nil
	}
	var ahead, behind int
	if _, err := fmt.Sscanf(strings.TrimSpace(counts), "%d\t%d", &ahead, &behind); err != nil {
		return nil
	}
	return &Upstream{Remote: parts[0], Ref: "refs/remotes/" + ref, OID: strings.TrimSpace(oid), Ahead: ahead, Behind: behind}
}
func readOperation(root string) Operation {
	gitDir, err := run(root, "rev-parse", "--git-path", ".")
	if err != nil {
		return Operation{Kind: "other", Detail: "cannot locate git directory"}
	}
	base := strings.TrimSpace(gitDir)
	if !filepath.IsAbs(base) {
		base = filepath.Join(root, base)
	}
	if _, err := os.Stat(filepath.Join(base, "rebase-apply", "applying")); err == nil {
		return Operation{Kind: "other", Detail: "git am in progress"}
	}
	checks := []struct{ path, kind string }{{"MERGE_HEAD", "merge"}, {"CHERRY_PICK_HEAD", "cherry-pick"}, {"REVERT_HEAD", "revert"}, {"BISECT_LOG", "bisect"}, {"rebase-merge", "rebase"}, {"rebase-apply", "rebase"}}
	for _, c := range checks {
		if _, err := os.Stat(filepath.Join(base, c.path)); err == nil {
			return Operation{Kind: c.kind}
		}
	}
	return Operation{Kind: "none"}
}

func readChanges(root string) (Changes, map[string][]byte, error) {
	out, err := runBytes(root, "status", "--porcelain=v2", "-z", "--untracked-files=all", "--no-renames", "--ignore-submodules=none")
	if err != nil {
		return Changes{}, nil, err
	}
	return parseChanges(out)
}

func parseChanges(out []byte) (Changes, map[string][]byte, error) {
	entries := map[string]Change{}
	paths := map[string][]byte{}
	parts := bytes.Split(out, []byte{0})
	for _, rec := range parts {
		if len(rec) == 0 {
			continue
		}
		tag := rec[0]
		switch tag {
		case '?':
			addChange(entries, paths, rec[2:], Change{IndexStatus: "untracked", WorktreeStatus: "untracked"})
		case '1':
			fields := bytes.SplitN(rec, []byte{' '}, 9)
			if len(fields) != 9 {
				return Changes{}, nil, fmt.Errorf("invalid porcelain v2 entry")
			}
			x, y := statusPair(string(fields[1]))
			addChange(entries, paths, fields[8], Change{IndexStatus: x, WorktreeStatus: y, worktreeMode: string(fields[5])})
		case 'u':
			fields := bytes.SplitN(rec, []byte{' '}, 11)
			if len(fields) != 11 {
				return Changes{}, nil, fmt.Errorf("invalid porcelain v2 conflict")
			}
			addChange(entries, paths, fields[10], Change{IndexStatus: "unmerged", WorktreeStatus: "unmerged", Conflict: true, worktreeMode: string(fields[6])})
		}
	}
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(paths[keys[i]], paths[keys[j]]) < 0 })
	ordered := make([]Change, 0, len(keys))
	for _, key := range keys {
		ordered = append(ordered, entries[key])
	}
	return Changes{Complete: true, UntrackedIncluded: true, IgnoredIncluded: false, Entries: ordered}, paths, nil
}
func addChange(entries map[string]Change, paths map[string][]byte, raw []byte, c Change) {
	key := string(raw)
	c.Path = key
	if existing, ok := entries[key]; ok {
		if existing.IndexStatus == "untracked" && c.IndexStatus != "untracked" {
			existing.IndexStatus = c.IndexStatus
		}
		if existing.WorktreeStatus == "unmodified" && c.WorktreeStatus != "unmodified" {
			existing.WorktreeStatus = c.WorktreeStatus
		}
		existing.Conflict = existing.Conflict || c.Conflict
		if existing.OriginalPath == "" {
			existing.OriginalPath = c.OriginalPath
		}
		if existing.worktreeMode == "" {
			existing.worktreeMode = c.worktreeMode
		}
		entries[key] = existing
		return
	}
	entries[key] = c
	paths[key] = append([]byte(nil), raw...)
}
func statusPair(xy string) (string, string) {
	if len(xy) != 2 {
		return "unknown", "unknown"
	}
	return mapStatus(xy[0]), mapStatus(xy[1])
}
func mapStatus(c byte) string {
	switch c {
	case '.':
		return "unmodified"
	case 'M':
		return "modified"
	case 'A':
		return "added"
	case 'D':
		return "deleted"
	case 'T':
		return "type-changed"
	case 'U':
		return "unmerged"
	case 'R':
		return "renamed"
	case 'C':
		return "copied"
	}
	return "unknown"
}

type fingerprintEntry struct {
	PathB64        string       `json:"path_b64"`
	IndexStatus    string       `json:"index_status"`
	WorktreeStatus string       `json:"worktree_status"`
	Conflict       bool         `json:"conflict"`
	IndexEntries   []indexEntry `json:"index_entries"`
	Worktree       any          `json:"worktree"`
}
type indexEntry struct {
	Stage int    `json:"stage"`
	Mode  string `json:"mode"`
	OID   string `json:"oid"`
}
type fingerprint struct {
	Profile      string `json:"profile"`
	ObjectFormat string `json:"object_format"`
	Head         Head   `json:"head"`
	Operation    struct {
		Kind string `json:"kind"`
	} `json:"operation"`
	Entries []fingerprintEntry `json:"entries"`
}

func fingerprintPayload(root, format string, head Head, op Operation, changes Changes, paths map[string][]byte) (fingerprint, error) {
	return fingerprintPayloadWithHook(root, format, head, op, changes, paths, nil)
}

func fingerprintPayloadWithHook(root, format string, head Head, op Operation, changes Changes, paths map[string][]byte, beforeHash func(path string)) (fingerprint, error) {
	indexes, err := readIndex(root)
	if err != nil {
		return fingerprint{}, err
	}
	result := fingerprint{Profile: "ctx-git-worktree-v1", ObjectFormat: format, Head: head, Entries: make([]fingerprintEntry, 0, len(changes.Entries))}
	result.Operation.Kind = op.Kind
	for _, change := range changes.Entries {
		if beforeHash != nil {
			beforeHash(change.Path)
		}
		raw := paths[change.Path]
		indexEntries := indexes[change.Path]
		if indexEntries == nil {
			indexEntries = []indexEntry{}
		}
		e := fingerprintEntry{PathB64: base64.RawURLEncoding.EncodeToString(raw), IndexStatus: change.IndexStatus, WorktreeStatus: change.WorktreeStatus, Conflict: change.Conflict, IndexEntries: indexEntries}
		wt, err := worktreeValue(root, change, indexEntries)
		if err != nil {
			return fingerprint{}, err
		}
		e.Worktree = wt
		result.Entries = append(result.Entries, e)
	}
	return result, nil
}

type pathIdentity struct {
	Exists  bool
	Size    int64
	Mode    os.FileMode
	ModTime int64
	Device  uint64
	Inode   uint64
}

func statChanges(root string, changes Changes) (map[string]pathIdentity, error) {
	result := make(map[string]pathIdentity, len(changes.Entries))
	for _, change := range changes.Entries {
		info, err := os.Lstat(filepath.Join(root, change.Path))
		if errors.Is(err, os.ErrNotExist) {
			result[change.Path] = pathIdentity{}
			continue
		}
		if err != nil {
			return nil, err
		}
		identity := pathIdentity{Exists: true, Size: info.Size(), Mode: info.Mode(), ModTime: info.ModTime().UnixNano()}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			identity.Device = uint64(stat.Dev)
			identity.Inode = uint64(stat.Ino)
		}
		result[change.Path] = identity
	}
	return result, nil
}

func readIndexIdentity(root string) (string, error) {
	indexPath, err := run(root, "rev-parse", "--git-path", "index")
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(indexPath)
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	return canonical.BytesDigest(data), nil
}

func readIndex(root string) (map[string][]indexEntry, error) {
	out, err := runBytes(root, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, err
	}
	result := map[string][]indexEntry{}
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		parts := bytes.SplitN(rec, []byte{'\t'}, 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid index entry")
		}
		f := strings.Fields(string(parts[0]))
		if len(f) != 3 {
			return nil, fmt.Errorf("invalid index entry")
		}
		var stage int
		if _, err := fmt.Sscanf(f[2], "%d", &stage); err != nil {
			return nil, err
		}
		key := string(parts[1])
		result[key] = append(result[key], indexEntry{Stage: stage, Mode: f[0], OID: f[1]})
	}
	for key := range result {
		sort.Slice(result[key], func(i, j int) bool { return result[key][i].Stage < result[key][j].Stage })
	}
	return result, nil
}
func worktreeValue(root string, c Change, indexes []indexEntry) (any, error) {
	if c.WorktreeStatus == "unmodified" {
		if c.IndexStatus == "deleted" && len(indexes) == 0 {
			return struct {
				State string `json:"state"`
			}{"missing"}, nil
		}
		return struct {
			State string `json:"state"`
		}{"matches-index"}, nil
	}
	path := filepath.Join(root, c.Path)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) || c.WorktreeStatus == "deleted" {
		return struct {
			State string `json:"state"`
		}{"missing"}, nil
	}
	if err != nil {
		return nil, err
	}
	if hasGitlinkMode(indexes) {
		return submoduleValue(path)
	}
	mode := "100644"
	var oid string
	if info.Mode()&os.ModeSymlink != 0 {
		mode = "120000"
		target, err := os.Readlink(path)
		if err != nil {
			return nil, err
		}
		hashed, err := runBytesInput(root, []byte(target), "hash-object", "--stdin")
		if err != nil {
			return nil, err
		}
		oid = strings.TrimSpace(string(hashed))
	} else if info.Mode().IsRegular() {
		mode, err = regularMode(root, info.Mode(), indexes, c.worktreeMode)
		if err != nil {
			return nil, err
		}
	} else {
		return nil, fmt.Errorf("unsupported file type: %s", c.Path)
	}
	if mode != "120000" {
		hashed, err := run(root, "hash-object", "--path="+c.Path, "--", c.Path)
		if err != nil {
			return nil, err
		}
		oid = strings.TrimSpace(hashed)
	}
	return struct {
		State      string `json:"state"`
		Mode       string `json:"mode"`
		ObjectKind string `json:"object_kind"`
		OID        string `json:"oid"`
	}{"present", mode, "blob", oid}, nil
}

func regularMode(root string, fileMode os.FileMode, indexes []indexEntry, observedMode string) (string, error) {
	configured, err := run(root, "config", "--bool", "--get", "core.fileMode")
	if err == nil && strings.TrimSpace(configured) == "false" {
		if isRegularMode(observedMode) {
			return observedMode, nil
		}
		if mode := stageZeroRegularMode(indexes); mode != "" {
			return mode, nil
		}
		if len(indexes) == 0 {
			return "100644", nil
		}
		return "", errors.New("tracked path has no canonical worktree mode or stage 0 index entry")
	}
	if err == nil && strings.TrimSpace(configured) != "true" {
		return "", fmt.Errorf("invalid core.fileMode value %q", strings.TrimSpace(configured))
	}
	if fileMode&0100 != 0 {
		return "100755", nil
	}
	return "100644", nil
}

func stageZeroRegularMode(entries []indexEntry) string {
	for _, entry := range entries {
		if entry.Stage == 0 && isRegularMode(entry.Mode) {
			return entry.Mode
		}
	}
	return ""
}

func isRegularMode(mode string) bool { return mode == "100644" || mode == "100755" }

func hasGitlinkMode(entries []indexEntry) bool {
	for _, entry := range entries {
		if entry.Mode == "160000" {
			return true
		}
	}
	return false
}

func submoduleValue(path string) (any, error) {
	oid, err := run(path, "rev-parse", "--verify", "HEAD")
	var currentOID *string
	if err == nil {
		value := strings.TrimSpace(oid)
		currentOID = &value
	}
	trackedDirty, untrackedDirty := false, false
	status, statusErr := runBytes(path, "status", "--porcelain=v2", "-z", "--untracked-files=all", "--no-renames", "--ignore-submodules=none")
	if statusErr == nil {
		trackedDirty, untrackedDirty = submoduleStatusDirtiness(status)
	} else if currentOID != nil {
		return nil, fmt.Errorf("observe submodule %s: %w", path, statusErr)
	}
	return struct {
		State          string  `json:"state"`
		Mode           string  `json:"mode"`
		ObjectKind     string  `json:"object_kind"`
		OID            *string `json:"oid"`
		TrackedDirty   bool    `json:"tracked_dirty"`
		UntrackedDirty bool    `json:"untracked_dirty"`
	}{"present", "160000", "gitlink", currentOID, trackedDirty, untrackedDirty}, nil
}

func submoduleStatusDirtiness(status []byte) (tracked, untracked bool) {
	for _, record := range bytes.Split(status, []byte{0}) {
		if len(record) == 0 || record[0] == '!' {
			continue
		}
		if record[0] == '?' {
			untracked = true
			continue
		}
		fields := bytes.SplitN(record, []byte{' '}, 4)
		if len(fields) < 3 || len(fields[2]) != 4 || fields[2][0] != 'S' {
			tracked = true
			continue
		}
		tracked = tracked || fields[2][1] == 'C' || fields[2][2] == 'M'
		untracked = untracked || fields[2][3] == 'U'
	}
	return tracked, untracked
}

func run(cwd string, args ...string) (string, error) {
	out, err := runBytes(cwd, args...)
	return string(out), err
}
func runBytes(cwd string, args ...string) ([]byte, error) {
	return runBytesInput(cwd, nil, args...)
}

func runBytesInput(cwd string, input []byte, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	cmd.Env = gitObservationEnv()
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, err
	}
	return out, nil
}

func gitObservationEnv() []string {
	env := os.Environ()
	filtered := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, "GIT_OPTIONAL_LOCKS=") {
			filtered = append(filtered, entry)
		}
	}
	return append(filtered, "GIT_OPTIONAL_LOCKS=0")
}
