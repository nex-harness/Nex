// repo_graph.go ports the commit-graph record parser from the bottom of
// gitCliRepo.ts (step 3.3). Split out of repo_mutations.go to stay under the
// 400-line archcheck limit.
package git

import (
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// commit graph parsing (parseGitGraphRecords & friends)
// ---------------------------------------------------------------------------

func addGitGraphRef(refs []GitCommitGraphRef, ref GitCommitGraphRef) []GitCommitGraphRef {
	for _, candidate := range refs {
		if candidate.Kind == ref.Kind && candidate.Name == ref.Name {
			return refs
		}
	}
	return append(refs, ref)
}

// parseGitGraphDecorationRef ports parseGitGraphDecorationRef.
func parseGitGraphDecorationRef(rawRef string) *GitCommitGraphRef {
	ref := strings.TrimSpace(rawRef)
	if ref == "" {
		return nil
	}
	if ref == "HEAD" {
		return &GitCommitGraphRef{Name: "HEAD", Kind: GraphRefKindHead}
	}
	const tagPrefix = "tag: "
	if strings.HasPrefix(ref, tagPrefix) {
		tagRef := strings.TrimSpace(ref[len(tagPrefix):])
		name := tagRef
		if strings.HasPrefix(name, "refs/tags/") {
			name = name[len("refs/tags/"):]
		}
		if name == "" {
			return nil
		}
		return &GitCommitGraphRef{Name: name, Kind: GraphRefKindTag}
	}
	if name := strings.TrimPrefix(ref, "refs/heads/"); name != ref && name != "" {
		return &GitCommitGraphRef{Name: name, Kind: GraphRefKindBranch}
	}
	if name := strings.TrimPrefix(ref, "refs/remotes/"); name != ref && name != "" {
		return &GitCommitGraphRef{Name: name, Kind: GraphRefKindRemote}
	}
	if name := strings.TrimPrefix(ref, "refs/tags/"); name != ref && name != "" {
		return &GitCommitGraphRef{Name: name, Kind: GraphRefKindTag}
	}
	kind := GraphRefKindBranch
	if strings.Contains(ref, "/") {
		kind = GraphRefKindRemote
	}
	return &GitCommitGraphRef{Name: ref, Kind: kind}
}

// parseGitGraphRefs ports parseGitGraphRefs.
func parseGitGraphRefs(rawDecorations string) []GitCommitGraphRef {
	refs := []GitCommitGraphRef{}
	for _, rawDecoration := range strings.Split(rawDecorations, ",") {
		decoration := strings.TrimSpace(rawDecoration)
		if decoration == "" {
			continue
		}
		const headPointer = "HEAD -> "
		if strings.HasPrefix(decoration, headPointer) {
			refs = addGitGraphRef(refs, GitCommitGraphRef{Name: "HEAD", Kind: GraphRefKindHead})
			if pointed := parseGitGraphDecorationRef(decoration[len(headPointer):]); pointed != nil {
				refs = addGitGraphRef(refs, *pointed)
			}
			continue
		}
		if parsed := parseGitGraphDecorationRef(decoration); parsed != nil {
			refs = addGitGraphRef(refs, *parsed)
		}
	}
	return refs
}

// parseGitGraphRecords ports parseGitGraphRecords.
func parseGitGraphRecords(stdout string) []GitCommitGraphCommit {
	commits := []GitCommitGraphCommit{}
	for _, record := range strings.Split(stdout, gitGraphRecordSeparator) {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		fields := strings.Split(record, gitGraphFieldSeparator)
		field := func(i int) string {
			if i < len(fields) {
				return fields[i]
			}
			return ""
		}
		hash := field(0)
		if hash == "" {
			continue
		}
		var parents []string
		for _, parent := range strings.Split(field(1), " ") {
			if parent != "" {
				parents = append(parents, parent)
			}
		}
		if parents == nil {
			parents = []string{}
		}
		var authorName *string
		if name := field(2); name != "" {
			authorName = ptr(name)
		}
		var authoredAtMs *int64
		if seconds := field(3); seconds != "" {
			if value, err := strconv.ParseInt(seconds, 10, 64); err == nil {
				ms := value * 1000
				authoredAtMs = &ms
			}
		}
		commits = append(commits, GitCommitGraphCommit{
			Hash:         hash,
			Parents:      parents,
			Refs:         parseGitGraphRefs(field(5)),
			Subject:      field(4),
			AuthorName:   authorName,
			AuthoredAtMs: authoredAtMs,
		})
	}
	return commits
}

// ---------------------------------------------------------------------------
// branch mutation guards
// ---------------------------------------------------------------------------

// hasOperationInProgress ports hasOperationInProgress: 进行中的 merge / rebase /
// cherry-pick 在不同 Git 版本上的报错并不完全稳定，先通过 git-dir 标记位做轻量探测。
