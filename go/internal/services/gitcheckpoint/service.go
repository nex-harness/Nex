// Package gitcheckpoint ports packages/services/src/git/gitCheckpointService.ts
// (step 3.3): the IGitCheckpointService surface. It composes the checkpoint
// repo (go/internal/services/git) with the manifest store — the repo layer owns
// Git object references, the store layer owns local metadata files.
package gitcheckpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/tylinux/nex/go/internal/services/git"
	"github.com/tylinux/nex/go/internal/services/paths"
)

// ChannelName is ServiceChannels.GitCheckpoint.
const ChannelName = "git-checkpoint"

// StoreOptions configures NewStore.
type StoreOptions struct {
	// RootDir overrides the checkpoints root (tests inject a temp dir).
	RootDir string
	// Now is the clock (tests); nil uses time.Now via nowMillis in the repo.
	Now func() int64
}

// Store ports GitCheckpointStore: per-file write chains serialize concurrent
// writers to the same manifest; the manifest is written atomically.
type Store struct {
	rootDir     string
	mu          sync.Mutex
	writeChains map[string]*writeChain
}

type writeChain struct {
	// done is closed when the enqueued operation finishes; the next write waits
	// on it, preserving the TS's promise chaining.
	done chan struct{}
}

// NewStore builds the store.
func NewStore(opts StoreOptions) *Store {
	rootDir := opts.RootDir
	if rootDir == "" {
		rootDir = defaultCheckpointsRoot()
	}
	return &Store{rootDir: rootDir, writeChains: map[string]*writeChain{}}
}

func defaultCheckpointsRoot() string {
	return filepath.Join(paths.NewFromProcess().AppConfigDir(), "checkpoints")
}

func (s *Store) checkpointsDir(workspacePath string) string {
	return filepath.Join(s.rootDir, paths.WorkspaceHash(workspacePath, ""))
}

func (s *Store) checkpointFilePath(workspacePath, checkpointID string) string {
	return filepath.Join(s.checkpointsDir(workspacePath), checkpointID+".json")
}

// enqueue serializes an operation on one file path behind the previous one.
func (s *Store) enqueue(filePath string, operation func() error) error {
	s.mu.Lock()
	previous, hasPrevious := s.writeChains[filePath]
	chain := &writeChain{done: make(chan struct{})}
	s.writeChains[filePath] = chain
	s.mu.Unlock()
	if hasPrevious {
		<-previous.done
	}
	err := operation()
	close(chain.done)
	// Best-effort chain cleanup: drop the entry when it is still ours.
	s.mu.Lock()
	if s.writeChains[filePath] == chain {
		delete(s.writeChains, filePath)
	}
	s.mu.Unlock()
	return err
}

// waitForPendingWrite ports waitForPendingWrite (swallows the previous error).
func (s *Store) waitForPendingWrite(filePath string) {
	s.mu.Lock()
	chain := s.writeChains[filePath]
	s.mu.Unlock()
	if chain != nil {
		<-chain.done
	}
}

// writeManifestAtomic ports writeManifestAtomic: temp file + rename, JSON with
// 2-space indent and trailing newline — the exact bytes the TS writes.
func writeManifestAtomic(filePath string, meta git.GitCheckpointMeta) error {
	content, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(filePath)
	tempFile, err := os.CreateTemp(dir, filepath.Base(filePath)+".*.tmp")
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()
	if _, err := tempFile.Write(append(content, '\n')); err != nil {
		tempFile.Close()
		os.Remove(tempPath)
		return err
	}
	if err := tempFile.Close(); err != nil {
		os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, filePath); err != nil {
		os.Remove(tempPath)
		return err
	}
	return nil
}

// Save ports save.
func (s *Store) Save(ctx context.Context, meta git.GitCheckpointMeta) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	filePath := s.checkpointFilePath(meta.WorkspacePath, meta.CheckpointID)
	return s.enqueue(filePath, func() error {
		if err := os.MkdirAll(s.checkpointsDir(meta.WorkspacePath), 0o777); err != nil {
			return err
		}
		return writeManifestAtomic(filePath, meta)
	})
}

// isGitCheckpointMeta ports isGitCheckpointMeta.
func isGitCheckpointMeta(value map[string]any) bool {
	if value == nil {
		return false
	}
	str := func(key string) (string, bool) { v, ok := value[key].(string); return v, ok }
	checkpointID, okID := str("checkpointId")
	workspacePath, okWS := str("workspacePath")
	repoRoot, okRR := str("repoRoot")
	workspaceInRepoPath, okWR := str("workspaceInRepoPath")
	refName, okRef := str("refName")
	commitOid, okOid := str("commitOid")
	scope, okScope := str("scope")
	_, okCreated := value["createdAt"].(float64)
	return okID && okWS && okRR && okWR && okRef && okOid && okScope &&
		checkpointID != "" && workspacePath != "" && repoRoot != "" &&
		workspaceInRepoPath != "" && refName != "" && commitOid != "" &&
		scope == "workspace" && okCreated
}

// Load ports load: any read/parse/validation failure returns (zero, nil) —
// the TS catches and returns null.
func (s *Store) Load(ctx context.Context, workspacePath, checkpointID string) (git.GitCheckpointMeta, bool, error) {
	if err := ctx.Err(); err != nil {
		return git.GitCheckpointMeta{}, false, err
	}
	filePath := s.checkpointFilePath(workspacePath, checkpointID)
	s.waitForPendingWrite(filePath)
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return git.GitCheckpointMeta{}, false, nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return git.GitCheckpointMeta{}, false, nil
	}
	if !isGitCheckpointMeta(parsed) {
		return git.GitCheckpointMeta{}, false, nil
	}
	// Re-marshal through the typed shape for the concrete fields.
	typed, err := json.Marshal(parsed)
	if err != nil {
		return git.GitCheckpointMeta{}, false, nil
	}
	var meta git.GitCheckpointMeta
	if err := json.Unmarshal(typed, &meta); err != nil {
		return git.GitCheckpointMeta{}, false, nil
	}
	return meta, true, nil
}

// Delete ports delete.
func (s *Store) Delete(ctx context.Context, workspacePath, checkpointID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	filePath := s.checkpointFilePath(workspacePath, checkpointID)
	return s.enqueue(filePath, func() error {
		// rm force: a missing file is not an error.
		if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// service
// ---------------------------------------------------------------------------

// Service ports createGitCheckpointService.
type Service struct {
	store *Store
	repo  *git.CheckpointRepo
	// NewCheckpointID generates ids; tests override for determinism.
	NewCheckpointID func() string
}

// ServiceOptions configures NewService.
type ServiceOptions struct {
	Store *Store
	Repo  *git.CheckpointRepo
}

// NewService ports createGitCheckpointService.
func NewService(opts ServiceOptions) *Service {
	store := opts.Store
	if store == nil {
		store = NewStore(StoreOptions{})
	}
	repo := opts.Repo
	if repo == nil {
		repo = git.NewCheckpointRepo(nil, nil, "")
	}
	return &Service{store: store, repo: repo, NewCheckpointID: newUUID}
}

func newUUID() string {
	// The TS uses crypto.randomUUID(); the Go module already depends on
	// google/uuid, but a dependency-free v4 keeps this package self-contained.
	b := make([]byte, 16)
	if _, err := readRandom(b); err != nil {
		panic(fmt.Sprintf("gitcheckpoint: uuid: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return formatUUID(b)
}

// ensureCheckpointExists / ensureSameWorkspace / ensureComparableCheckpoints
// port the same-named guards.
func ensureCheckpointExists(meta git.GitCheckpointMeta, found bool, checkpointID string) error {
	if !found {
		return fmt.Errorf("Checkpoint does not exist: %s", checkpointID)
	}
	return nil
}

func ensureSameWorkspace(meta git.GitCheckpointMeta, workspacePath string) error {
	if meta.WorkspacePath != workspacePath {
		return fmt.Errorf("Checkpoint workspace mismatch: %s", meta.CheckpointID)
	}
	return nil
}

func ensureComparableCheckpoints(left, right git.GitCheckpointMeta) error {
	if left.RepoRoot != right.RepoRoot {
		return errors.New("Checkpoint repoRoot mismatch.")
	}
	if left.Scope != right.Scope {
		return errors.New("Checkpoint scope mismatch.")
	}
	if left.WorkspaceInRepoPath != right.WorkspaceInRepoPath {
		return errors.New("Checkpoint workspace scope mismatch.")
	}
	return nil
}

func (s *Service) loadCheckpoint(ctx context.Context, workspacePath, checkpointID string) (git.GitCheckpointMeta, error) {
	meta, found, err := s.store.Load(ctx, workspacePath, checkpointID)
	if err != nil {
		return git.GitCheckpointMeta{}, err
	}
	if err := ensureCheckpointExists(meta, found, checkpointID); err != nil {
		return git.GitCheckpointMeta{}, err
	}
	if err := ensureSameWorkspace(meta, workspacePath); err != nil {
		return git.GitCheckpointMeta{}, err
	}
	return meta, nil
}

// CreateCheckpoint ports createCheckpoint.
func (s *Service) CreateCheckpoint(ctx context.Context, workspacePath string) (git.GitCheckpointMeta, error) {
	checkpointID := s.NewCheckpointID()
	meta, err := s.repo.CreateCheckpoint(ctx, workspacePath, checkpointID)
	if err != nil {
		return git.GitCheckpointMeta{}, err
	}
	if err := s.store.Save(ctx, meta); err != nil {
		return git.GitCheckpointMeta{}, err
	}
	return meta, nil
}

// DiffCheckpoints ports diffCheckpoints.
func (s *Service) DiffCheckpoints(ctx context.Context, workspacePath, fromCheckpointID, toCheckpointID string) (git.GitCheckpointDiff, error) {
	from, err := s.loadCheckpoint(ctx, workspacePath, fromCheckpointID)
	if err != nil {
		return git.GitCheckpointDiff{}, err
	}
	to, err := s.loadCheckpoint(ctx, workspacePath, toCheckpointID)
	if err != nil {
		return git.GitCheckpointDiff{}, err
	}
	if err := ensureComparableCheckpoints(from, to); err != nil {
		return git.GitCheckpointDiff{}, err
	}
	return s.repo.DiffCheckpoints(ctx, workspacePath, from, to)
}

// RestoreBetweenCheckpoints ports restoreBetweenCheckpoints.
func (s *Service) RestoreBetweenCheckpoints(ctx context.Context, workspacePath, fromCheckpointID, toCheckpointID string, force bool) (git.GitCheckpointRestoreResult, error) {
	from, err := s.loadCheckpoint(ctx, workspacePath, fromCheckpointID)
	if err != nil {
		return git.GitCheckpointRestoreResult{}, err
	}
	to, err := s.loadCheckpoint(ctx, workspacePath, toCheckpointID)
	if err != nil {
		return git.GitCheckpointRestoreResult{}, err
	}
	if err := ensureComparableCheckpoints(from, to); err != nil {
		return git.GitCheckpointRestoreResult{}, err
	}
	return s.repo.RestoreBetweenCheckpoints(ctx, workspacePath, from, to, force)
}

// DeleteCheckpoint ports deleteCheckpoint.
func (s *Service) DeleteCheckpoint(ctx context.Context, workspacePath, checkpointID string) error {
	checkpoint, err := s.loadCheckpoint(ctx, workspacePath, checkpointID)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteCheckpoint(ctx, workspacePath, checkpoint); err != nil {
		return err
	}
	return s.store.Delete(ctx, workspacePath, checkpointID)
}
