package configstore

// Content-addressed skill and prompt bundles (docs/next/config-mcp-skills.md P2).
//
// The doc only holds references ({name, digest, kind, enabled}); the files live
// under `<config dir>/bundles/<hex>/` so re-uploading the same content is free
// and a runner can verify what it receives. Nothing here executes bundle code.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Bundle file layout: `<config dir>/bundles/<hex>/bundle.json` (the manifest)
// and `<config dir>/bundles/<hex>/files/<path>` (UTF-8 text files).
const bundleManifestName = "bundle.json"

var bundleDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// BundleFile is one file of a bundle (text only: skills are markdown and
// scripts are text; a binary asset has no place in a skill).
type BundleFile struct {
	// Path is a slash-separated path relative to the bundle root (e.g.
	// `SKILL.md` or `references/notes.md`).
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Bundle is one stored manifest (what the runner receives).
type Bundle struct {
	Name        string       `json:"name"`
	Kind        string       `json:"kind"`
	Digest      string       `json:"digest"`
	Description string       `json:"description,omitempty"`
	Files       []BundleFile `json:"files"`
}

// maxBundleFileBytes bounds one uploaded file (skills are prose).
const maxBundleFileBytes = 512 * 1024

// maxBundleFiles bounds one bundle.
const maxBundleFiles = 200

// DigestFiles returns the content-addressed digest of files: a sorted
// `path\0sha256(content)\n` listing, hashed again. Independent of upload order
// and of unrelated whitespace in the manifest.
func DigestFiles(files []BundleFile) string {
	sorted := make([]BundleFile, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(left, right int) bool { return sorted[left].Path < sorted[right].Path })
	sum := sha256.New()
	for _, file := range sorted {
		content := sha256.Sum256([]byte(file.Content))
		fmt.Fprintf(sum, "%s\x00%s\n", file.Path, hex.EncodeToString(content[:]))
	}
	return "sha256:" + hex.EncodeToString(sum.Sum(nil))
}

// NormalizeBundlePath validates one bundle-relative path and returns it with
// forward slashes. Empty, absolute, dot-segment and backslash paths are
// rejected so a bundle can never write outside its directory.
func NormalizeBundlePath(path string) (string, error) {
	trimmed := strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
	trimmed = strings.TrimPrefix(trimmed, "./")
	if trimmed == "" {
		return "", fmt.Errorf("file path is empty")
	}
	if strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("file path %q must be relative", path)
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("file path %q has an invalid segment", path)
		}
	}
	return trimmed, nil
}

// validateBundles checks name, digest and kind of one layer's bundle list.
func validateBundles(path string, refs []BundleRef, kind string) []Issue {
	var issues []Issue
	seen := map[string]struct{}{}
	for index, ref := range refs {
		entryPath := fmt.Sprintf("%s.%d", path, index)
		if strings.TrimSpace(ref.Name) == "" {
			issues = append(issues, Issue{Path: entryPath + ".name", Code: "bundle-name-missing", Message: "bundle name is required"})
		} else if _, dup := seen[ref.Name]; dup {
			issues = append(issues, Issue{Path: entryPath + ".name", Code: "bundle-duplicate", Message: "duplicate bundle " + ref.Name})
		}
		seen[ref.Name] = struct{}{}
		if !bundleDigestPattern.MatchString(ref.Digest) {
			issues = append(issues, Issue{Path: entryPath + ".digest", Code: "bundle-digest-invalid", Message: `digest must be "sha256:<64 hex>"`})
		}
		if ref.Kind != kind {
			issues = append(issues, Issue{Path: entryPath + ".kind", Code: "bundle-kind-invalid", Message: "kind must be " + kind})
		}
	}
	return issues
}

// BundleDir returns the directory of one stored digest.
func (s *Store) BundleDir(digest string) string {
	return filepath.Join(s.ConfigDir(), "bundles", strings.TrimPrefix(digest, "sha256:"))
}

// StoreBundle writes one bundle into the content-addressed store (idempotent)
// and returns the digest. Files must carry normalized paths.
func (s *Store) StoreBundle(bundle Bundle) (string, error) {
	if len(bundle.Files) == 0 {
		return "", fmt.Errorf("bundle %s has no files", bundle.Name)
	}
	if len(bundle.Files) > maxBundleFiles {
		return "", fmt.Errorf("bundle %s has more than %d files", bundle.Name, maxBundleFiles)
	}
	for _, file := range bundle.Files {
		if len(file.Content) > maxBundleFileBytes {
			return "", fmt.Errorf("bundle %s: %s exceeds %d bytes", bundle.Name, file.Path, maxBundleFileBytes)
		}
	}
	digest := DigestFiles(bundle.Files)
	directory := s.BundleDir(digest)
	manifestPath := filepath.Join(directory, bundleManifestName)
	if _, err := os.Stat(manifestPath); err == nil {
		return digest, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(directory), 0o700); err != nil {
		return "", fmt.Errorf("creating bundle store: %w", err)
	}
	temporary, err := os.MkdirTemp(filepath.Dir(directory), ".bundle-*")
	if err != nil {
		return "", fmt.Errorf("creating bundle temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	for _, file := range bundle.Files {
		target := filepath.Join(temporary, "files", filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return "", fmt.Errorf("creating %s: %w", file.Path, err)
		}
		if err := os.WriteFile(target, []byte(file.Content), 0o600); err != nil {
			return "", fmt.Errorf("writing %s: %w", file.Path, err)
		}
	}
	manifest := bundle
	manifest.Digest = digest
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(temporary, bundleManifestName), append(encoded, '\n'), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(temporary, directory); err != nil {
		// A concurrent writer may have stored the same digest first.
		if _, statErr := os.Stat(manifestPath); statErr == nil {
			return digest, nil
		}
		return "", fmt.Errorf("publishing bundle: %w", err)
	}
	return digest, nil
}

// LoadBundle reads one stored bundle and verifies the digest against its files.
func (s *Store) LoadBundle(digest string) (Bundle, error) {
	raw, err := os.ReadFile(filepath.Join(s.BundleDir(digest), bundleManifestName))
	if err != nil {
		return Bundle{}, fmt.Errorf("bundle %s: %w", digest, err)
	}
	var bundle Bundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return Bundle{}, fmt.Errorf("bundle %s manifest: %w", digest, err)
	}
	if actual := DigestFiles(bundle.Files); actual != digest {
		return Bundle{}, fmt.Errorf("bundle %s content digest mismatch (%s)", digest, actual)
	}
	return bundle, nil
}
