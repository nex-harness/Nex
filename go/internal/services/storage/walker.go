// walker.go ports storage/adapters/fsWalker.ts: the data-root walker.
// Bounded concurrency, cancellable, does not follow symlinks. Emits
// (relativePath, bytes, mtimeMs); classification and aggregation are the
// domain layer's job in the caller.
package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ScanEntry ports StorageScanEntry.
type ScanEntry struct {
	RelativePath string
	Bytes        int64
	MtimeMs      int64
}

// WalkResult ports WalkStorageRootResult.
type WalkResult struct {
	DirectoriesScanned int
	FilesScanned       int
	// MissingRoot: the root itself does not exist (treated as an empty root,
	// not an error).
	MissingRoot bool
}

// walkOptions ports WalkStorageRootOptions (concurrency defaults: 4 dirs at
// once, a yield every 256 entries — the yield exists to share the Node event
// loop and has no Go equivalent, so it is not ported).
type walkOptions struct {
	rootPath string
	onEntry  func(ScanEntry)
	onError  func(PathError)
	cancel   <-chan struct{}
}

func errCode(err error) string {
	if err == nil {
		return "UNKNOWN"
	}
	if pathErr, ok := err.(*os.PathError); ok {
		return strings.ToUpper(pathErr.Op) + ":" + errnoName(pathErr.Err)
	}
	return "UNKNOWN"
}

// toRelative ports toRelative: forward-slash path relative to the root.
func toRelative(rootPath, absolutePath string) string {
	rel, err := filepath.Rel(rootPath, absolutePath)
	if err != nil {
		return absolutePath
	}
	return filepath.ToSlash(rel)
}

// walkStorageRoot ports walkStorageRoot. Errors per path are reported via
// onError and do not abort the walk; cancellation aborts with ErrScanAborted.
func walkStorageRoot(ctx context.Context, options walkOptions) (WalkResult, error) {
	const concurrency = 4
	pending := []string{options.rootPath}
	result := WalkResult{}
	aborted := func() bool {
		select {
		case <-options.cancel:
			return true
		default:
			return false
		}
	}
	var mu sync.Mutex // guards result counters, pending, and the entry callback ordering

	for {
		if aborted() {
			return result, ErrScanAborted
		}
		mu.Lock()
		if len(pending) == 0 {
			mu.Unlock()
			return result, nil
		}
		// Copy the batch out: `pending = pending[:n]` keeps aliasing the same
		// backing array, and a goroutine's append below would overwrite the
		// entries the range loop is still reading.
		take := min(concurrency, len(pending))
		batch := append([]string(nil), pending[len(pending)-take:]...)
		pending = pending[:len(pending)-take]
		mu.Unlock()

		var wg sync.WaitGroup
		for _, directory := range batch {
			wg.Add(1)
			go func(directoryPath string) {
				defer wg.Done()
				if aborted() {
					return
				}
				entries, err := os.ReadDir(directoryPath)
				if err != nil {
					if directoryPath == options.rootPath && os.IsNotExist(err) {
						mu.Lock()
						result.MissingRoot = true
						mu.Unlock()
						return
					}
					if options.onError != nil {
						mu.Lock()
						options.onError(PathError{Path: toRelative(options.rootPath, directoryPath), Code: errCode(err)})
						mu.Unlock()
					}
					return
				}
				mu.Lock()
				result.DirectoriesScanned++
				mu.Unlock()
				for _, entry := range entries {
					if aborted() {
						return
					}
					entryPath := filepath.Join(directoryPath, entry.Name())
					if entry.Type() == os.ModeSymlink {
						continue
					}
					if entry.IsDir() {
						mu.Lock()
						pending = append(pending, entryPath)
						mu.Unlock()
						continue
					}
					if !entry.Type().IsRegular() {
						continue
					}
					info, err := os.Lstat(entryPath)
					if err != nil {
						// 扫描期间文件可能被日志轮转或 Agent 删除；ENOENT 属于正常竞态，不计为错误。
						if !os.IsNotExist(err) && options.onError != nil {
							mu.Lock()
							options.onError(PathError{Path: toRelative(options.rootPath, entryPath), Code: errCode(err)})
							mu.Unlock()
						}
						continue
					}
					if !info.Mode().IsRegular() {
						continue
					}
					mu.Lock()
					result.FilesScanned++
					if options.onEntry != nil {
						options.onEntry(ScanEntry{
							RelativePath: toRelative(options.rootPath, entryPath),
							Bytes:        info.Size(),
							MtimeMs:      info.ModTime().UnixMilli(),
						})
					}
					mu.Unlock()
				}
			}(directory)
		}
		wg.Wait()
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// inProcessScanRunner ports storage/adapters/inProcessScanRunner.ts: walker +
// domain accumulator + volume probe, one scan path shared by the host and the
// tests. Runner-side progress interval defaults to 300ms.
type inProcessScanRunner struct {
	volumeProbe        VolumeProbePort
	progressIntervalMs int64
	now                Clock
}

// NewInProcessScanRunner ports runStorageScan's default assembly.
func NewInProcessScanRunner(volumeProbe VolumeProbePort, progressIntervalMs int64, now Clock) ScanRunnerPort {
	if progressIntervalMs <= 0 {
		progressIntervalMs = 300
	}
	if now == nil {
		now = func() int64 { return timeNowMillis() }
	}
	return &inProcessScanRunner{volumeProbe: volumeProbe, progressIntervalMs: progressIntervalMs, now: now}
}

// Run ports runStorageScan.
func (r *inProcessScanRunner) Run(ctx context.Context, request ScanRunRequest) (ScanProgress, error) {
	var errorsMu sync.Mutex
	var scanErrors []PathError
	finished := make([]RootUsage, 0, len(request.Roots))
	lastReportAt := int64(-1 << 62)

	for _, root := range request.Roots {
		accumulator := newUsageAccumulator(root, 0)
		var volume *Volume
		if r.volumeProbe != nil {
			volume = r.volumeProbe.Probe(ctx, root.Path)
		}
		report := func() {
			lastReportAt = r.now()
			snapshot := accumulator.snapshot(volume)
			errorsMu.Lock()
			errors := append([]PathError{}, scanErrors...)
			errorsMu.Unlock()
			request.OnProgress(ScanProgress{Roots: append(append([]RootUsage{}, finished...), snapshot), Errors: errors})
		}
		cancel := request.Cancel
		if cancel == nil {
			cancel = make(chan struct{})
		}
		_, walkErr := walkStorageRoot(ctx, walkOptions{
			rootPath: root.Path,
			cancel:   cancel,
			onEntry: func(entry ScanEntry) {
				accumulator.add(entry)
				if r.now()-lastReportAt >= r.progressIntervalMs {
					report()
				}
			},
			onError: func(scanErr PathError) {
				errorsMu.Lock()
				scanErrors = append(scanErrors, PathError{Path: root.ID + ":" + scanErr.Path, Code: scanErr.Code})
				errorsMu.Unlock()
			},
		})
		if walkErr != nil {
			return ScanProgress{}, walkErr
		}
		finished = append(finished, accumulator.snapshot(volume))
	}
	errorsMu.Lock()
	defer errorsMu.Unlock()
	progress := ScanProgress{Roots: finished, Errors: scanErrors}
	request.OnProgress(progress)
	return progress, nil
}
