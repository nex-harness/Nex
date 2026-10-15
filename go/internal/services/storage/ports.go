// ports.go ports storage/app/ports.ts: the app layer's seams. storageService
// depends only on these interfaces; IO is implemented by the adapters
// (walker.go / cleaner.go / roots.go / volumeprobe.go) and injected by the
// desktop host.
package storage

import (
	"context"
	"errors"
)

// ErrScanAborted is the abort sentinel the runner returns on cancel (the TS
// rejects with a DOMException named "AbortError"; isAbortError's check).
var ErrScanAborted = errors.New("storage scan aborted")

// RootsResolverPort ports RootsResolverPort.
type RootsResolverPort interface {
	ResolveRoots(ctx context.Context) ([]RootSpec, error)
}

// ScanProgress ports StorageScanProgress.
type ScanProgress struct {
	Roots  []RootUsage
	Errors []PathError
}

// ScanRunRequest ports StorageScanRunRequest. The runner reports progress at
// its own pace; throttling is the app-layer scan job's job.
type ScanRunRequest struct {
	Roots      []RootSpec
	Cancel     <-chan struct{}
	OnProgress func(ScanProgress)
}

// ScanRunnerPort ports ScanRunnerPort: cancels by returning ErrScanAborted.
type ScanRunnerPort interface {
	Run(ctx context.Context, request ScanRunRequest) (ScanProgress, error)
}

// VolumeProbePort ports VolumeProbePort.
type VolumeProbePort interface {
	Probe(ctx context.Context, path string) *Volume
}

// DeleteResult ports StorageDeleteResult.
type DeleteResult struct {
	DeletedCount int
	FreedBytes   int64
	Failures     []PathError
}

// FsCleanerPort ports FsCleanerPort.
type FsCleanerPort interface {
	ListCandidates(ctx context.Context, rootPath string, scopes []CleanScope) ([]CleanCandidate, error)
	DeleteFiles(ctx context.Context, rootPath string, targets []CleanCandidate, keepDirectories []string) (DeleteResult, error)
}

// Clock is the time seam (deps.now in the TS).
type Clock func() int64
