// roots.go ports storage/adapters/rootsResolver.ts: the data roots.
// R1 = .nex under the home directory (always); R2 = .nex under the custom
// data-base dir (only when set and different from home). Paths come from the
// injected getters; the module reads no environment itself.
package storage

import (
	"context"
	"path/filepath"
)

// nexDataDirName ports NEX_DATA_DIR_NAME.
const nexDataDirName = ".nex"

// ResolveStorageRoots ports resolveStorageRoots.
func ResolveStorageRoots(homeDir, dataBaseDir string) []RootSpec {
	home := abs(homeDir)
	dataBase := abs(dataBaseDir)
	hasCustomDataBaseDir := dataBase != home
	roots := []RootSpec{
		{ID: RootHome, Path: filepath.Join(home, nexDataDirName), HasCustomDataBaseDir: hasCustomDataBaseDir},
	}
	if hasCustomDataBaseDir {
		roots = append(roots, RootSpec{
			ID: RootDataBaseDir, Path: filepath.Join(dataBase, nexDataDirName), HasCustomDataBaseDir: hasCustomDataBaseDir,
		})
	}
	return roots
}

func abs(path string) string {
	if path == "" {
		return "."
	}
	return filepath.Clean(path)
}

// rootsResolver ports createStorageRootsResolver.
type rootsResolver struct {
	getHomeDir     func() string
	getDataBaseDir func() string
}

// NewStorageRootsResolver ports createStorageRootsResolver.
func NewStorageRootsResolver(getHomeDir, getDataBaseDir func() string) RootsResolverPort {
	return &rootsResolver{getHomeDir: getHomeDir, getDataBaseDir: getDataBaseDir}
}

// ResolveRoots implements RootsResolverPort.
func (r *rootsResolver) ResolveRoots(_ context.Context) ([]RootSpec, error) {
	return ResolveStorageRoots(r.getHomeDir(), r.getDataBaseDir()), nil
}
