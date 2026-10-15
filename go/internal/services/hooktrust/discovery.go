// discovery.go: the workspace hook config discovery and the settings-model
// glue the hooks service needs: candidate paths from the worktree walk,
// config parsing with the strict hooks schema, and the atomic config write.
package hooktrust

import (
	"os"
	"path/filepath"
)

// DiscoverConfigPaths ports discoverWorkspaceHookConfigPaths: nex.json and
// .nex/config.json at every directory from the working dir up to the
// worktree root (root-first order), plus the explicit path; duplicates by
// resolved path keep the first occurrence (auto-discovered before explicit
// — reordering would invalidate every trust record).
func DiscoverConfigPaths(workingDirectory string, explicitProjectConfigPath string) []ConfigPathRef {
	start, err := filepath.Abs(workingDirectory)
	if err != nil {
		start = workingDirectory
	}
	directories := projectConfigDirectories(start)
	var candidates []string
	for _, directory := range directories {
		candidates = append(candidates,
			filepath.Join(directory, "nex.json"),
			filepath.Join(directory, ".nex", "config.json"),
		)
	}
	var refs []ConfigPathRef
	seen := map[string]bool{}
	appendRef := func(path string, explicit bool) {
		resolved, err := filepath.Abs(path)
		if err != nil {
			resolved = path
		}
		if seen[resolved] {
			return
		}
		seen[resolved] = true
		refs = append(refs, ConfigPathRef{Path: path, ExplicitProjectConfig: explicit})
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			appendRef(path, false)
		}
	}
	if explicitProjectConfigPath != "" {
		if _, err := os.Stat(explicitProjectConfigPath); err == nil {
			appendRef(explicitProjectConfigPath, true)
		}
	}
	return refs
}

// ConfigPathRef ports WorkspaceHookConfigPathRef.
type ConfigPathRef struct {
	Path                  string
	ExplicitProjectConfig bool
}

// projectConfigDirectories ports getProjectConfigDirectories: from start up
// to (and including) the .git marker directory, reversed; without a marker,
// just start.
func projectConfigDirectories(start string) []string {
	var directories []string
	current := start
	for {
		directories = append(directories, current)
		if hasWorktreeMarker(current) {
			// reverse
			for i, j := 0, len(directories)-1; i < j; i, j = i+1, j-1 {
				directories[i], directories[j] = directories[j], directories[i]
			}
			return directories
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return []string{start}
}

func hasWorktreeMarker(directory string) bool {
	marker := filepath.Join(directory, ".git")
	info, err := os.Stat(marker)
	return err == nil && (info.IsDir() || info.Mode().IsRegular())
}

// ConfigFileKind ports WorkspaceHookConfigFileKind.
type ConfigFileKind string

const (
	KindNexJSON      ConfigFileKind = "nex.json"
	KindDotNexConfig ConfigFileKind = ".nex/config.json"
	KindExplicit     ConfigFileKind = "explicit"
)

// CreateSourceInput ports createWorkspaceHookSourceInput.
func CreateSourceInput(path, workingDirectory string, hooks *HooksConfig, discoveryOrder int, explicitProjectConfig bool) SourceInput {
	canonicalPath, err := filepath.Abs(path)
	if err != nil {
		canonicalPath = path
	}
	configFileKind := KindDotNexConfig
	if explicitProjectConfig {
		configFileKind = KindExplicit
	} else if filepath.Base(canonicalPath) == "nex.json" {
		configFileKind = KindNexJSON
	}
	configDirectory := filepath.Dir(canonicalPath)
	baseDir := configDirectory
	if filepath.Base(configDirectory) == ".nex" {
		baseDir = filepath.Dir(configDirectory)
	}
	editableWorkingDir, err := filepath.Abs(workingDirectory)
	if err != nil {
		editableWorkingDir = workingDirectory
	}
	editable := !explicitProjectConfig &&
		canonicalPath == filepath.Join(editableWorkingDir, ".nex", "config.json")
	return SourceInput{
		CanonicalPath:         canonicalPath,
		BaseDir:               baseDir,
		DiscoveryOrder:        discoveryOrder,
		ConfigFileKind:        string(configFileKind),
		ExplicitProjectConfig: explicitProjectConfig,
		Editable:              editable,
		Hooks:                 hooks,
	}
}
