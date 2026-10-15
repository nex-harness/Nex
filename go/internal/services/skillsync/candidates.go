// candidates.go: the local user skill candidate collection — the .nex-first
// / uncovered-.agents pipeline, the covered-directory walk, and the
// by-name directory index.
package skillsync

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tylinux/nex/go/internal/skills"
)

// collectUserSkillCandidates ports collectUserSkillCandidates.
func (s *Service) collectUserSkillCandidates() ([]Candidate, error) {
	seenSkillRealpaths := map[string]bool{}
	userNexSkillRoot := s.userNexSkillRoot()
	rawNexCandidates, err := s.collectCandidatesInRoot(userNexSkillRoot)
	if err != nil {
		return nil, err
	}
	nexDirectoryNames, err := s.collectCoveredSkillDirectoryNamesInRoot(userNexSkillRoot)
	if err != nil {
		return nil, err
	}
	nexSkillNameKeys := map[string]bool{}
	for _, candidate := range rawNexCandidates {
		nexSkillNameKeys[skills.NormalizeSkillNameKey(candidate.Name)] = true
	}
	nexCandidates := dedupeCandidatesByCanonicalPath(rawNexCandidates, seenSkillRealpaths)

	agentsRaw, err := s.collectCandidatesInRoot(s.userAgentsSkillRoot())
	if err != nil {
		return nil, err
	}
	var uncoveredAgents []Candidate
	for _, candidate := range agentsRaw {
		if nexDirectoryNames[candidate.DirectoryName] ||
			nexSkillNameKeys[skills.NormalizeSkillNameKey(candidate.Name)] {
			continue
		}
		uncoveredAgents = append(uncoveredAgents, candidate)
	}
	agentsCandidates := dedupeCandidatesByCanonicalPath(uncoveredAgents, seenSkillRealpaths)

	out := append(nexCandidates, agentsCandidates...)
	sort.Slice(out, func(i, j int) bool { return out[i].DirectoryName < out[j].DirectoryName })
	return out, nil
}

func dedupeCandidatesByCanonicalPath(candidates []Candidate, seen map[string]bool) []Candidate {
	var out []Candidate
	for _, candidate := range candidates {
		canonicalPath := candidate.Path
		if resolved, err := filepath.EvalSymlinks(candidate.Path); err == nil {
			canonicalPath = resolved
		}
		if seen[canonicalPath] {
			continue
		}
		seen[canonicalPath] = true
		out = append(out, candidate)
	}
	return out
}

// collectCandidatesInRoot ports collectUserSkillCandidatesInRoot: the shared
// bounded walk (depth cap, symlink dedupe — a hand-rolled recursion would
// loop on circular symlinked dirs).
func (s *Service) collectCandidatesInRoot(root string) ([]Candidate, error) {
	if _, err := os.Stat(root); err != nil {
		return nil, nil
	}
	var candidates []Candidate
	for _, skillPath := range skills.WalkSkillMarkdownPaths(root, nil) {
		directoryPath := filepath.Dir(skillPath)
		relativePath, err := filepath.Rel(root, directoryPath)
		if err != nil || relativePath == "" || relativePath == "." {
			continue
		}
		content, err := os.ReadFile(skillPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		metadata := parseSkillMetadata(string(content), filepath.Base(relativePath))
		sizeBytes, err := computeRecursiveSize(directoryPath, map[string]bool{})
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, Candidate{
			ID:            skills.CandidateID(directoryPath),
			Name:          metadata.name,
			DirectoryName: filepath.ToSlash(relativePath),
			Description:   metadata.description,
			Path:          skillPath,
			SizeBytes:     sizeBytes,
		})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].DirectoryName < candidates[j].DirectoryName })
	return candidates, nil
}

// collectCoveredSkillDirectoryNamesInRoot ports the covered-directory walk:
// every subdir (relative path) that itself holds a SKILL.md.
func (s *Service) collectCoveredSkillDirectoryNamesInRoot(root string) (map[string]bool, error) {
	directoryNames := map[string]bool{}
	if _, err := os.Stat(root); err != nil {
		return directoryNames, nil
	}
	type frame struct {
		directoryPath string
		relativePath  string
		depth         int
	}
	stack := []frame{{root, "", 0}}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		skillFilePath := filepath.Join(current.directoryPath, skills.FileName)
		if current.relativePath != "" {
			if data, err := os.ReadFile(skillFilePath); err == nil {
				_ = data
				directoryNames[current.relativePath] = true
			}
		}
		if current.depth >= skills.MaxScanDepth {
			continue
		}
		entries, err := os.ReadDir(current.directoryPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			if !skills.ShouldWalkDirectoryEntry(entry.Name()) {
				continue
			}
			entryPath := filepath.Join(current.directoryPath, entry.Name())
			if !skills.IsDirEntry(entry, entryPath) {
				continue
			}
			relative := current.relativePath
			if relative != "" {
				relative += "/" + entry.Name()
			} else {
				relative = entry.Name()
			}
			stack = append(stack, frame{entryPath, relative, current.depth + 1})
		}
	}
	return directoryNames, nil
}

type skillSyncMetadata struct{ name, description string }

// parseSkillMetadata ports parseSkillMetadata: strict-ish YAML read with the
// folder-name fallback (this is the sync service's own simpler reader, which
// uses the plain /^---\r?\n…\n---/ regex, NOT the service frontmatter with
// leaked-body handling).
func parseSkillMetadata(content, fallbackName string) skillSyncMetadata {
	normalized := strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
	// Inline port of the sync regex (frontmatter, YAML name/description).
	start := strings.Index(normalized, "---\n")
	if start != 0 {
		return skillSyncMetadata{name: fallbackName}
	}
	rest := normalized[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return skillSyncMetadata{name: fallbackName}
	}
	frontmatter := rest[:end]
	name, description := readYAMLNameDescription(frontmatter)
	out := skillSyncMetadata{name: fallbackName, description: strings.TrimSpace(description)}
	if strings.TrimSpace(name) != "" {
		out.name = strings.TrimSpace(name)
	}
	return out
}

// readYAMLNameDescription reads name/description scalars from frontmatter.
func readYAMLNameDescription(frontmatter string) (name, description string) {
	for _, line := range strings.Split(frontmatter, "\n") {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		switch strings.TrimSpace(key) {
		case "name":
			name = strings.TrimSpace(value)
		case "description":
			description = strings.TrimSpace(value)
		}
	}
	return name, description
}

// collectUserSkillDirectoryPathByName ports collectUserSkillDirectoryPathByName.
func (s *Service) collectUserSkillDirectoryPathByName() (map[string]string, error) {
	result := map[string]string{}
	for _, root := range []string{s.userNexSkillRoot(), s.userAgentsSkillRoot()} {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		for _, skillPath := range skills.WalkSkillMarkdownPaths(root, nil) {
			content, err := os.ReadFile(skillPath)
			if err != nil {
				continue
			}
			directoryPath := filepath.Dir(skillPath)
			metadata := parseSkillMetadata(string(content), filepath.Base(directoryPath))
			nameKey := skills.NormalizeSkillNameKey(metadata.name)
			if _, ok := result[nameKey]; !ok {
				result[nameKey] = directoryPath
			}
		}
	}
	return result, nil
}
