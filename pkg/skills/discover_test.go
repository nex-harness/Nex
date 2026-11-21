package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nex-harness/nex/pkg/store"
)

func writeSkillFile(t *testing.T, baseDir, skillDir, content string) {
	t.Helper()
	dir := filepath.Join(baseDir, skillDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverPrecedenceAndWorkspaceVisibility(t *testing.T) {
	tmp := t.TempDir()
	homeDir := filepath.Join(tmp, "home")
	wsA := filepath.Join(tmp, "repo-a")
	wsB := filepath.Join(tmp, "repo-b")

	// User skill: shared-review & user-only
	writeSkillFile(t, filepath.Join(homeDir, ".agents", "skills"), "shared-review", `---
name: shared-review
description: User-level code review checklist
---
# User review
`)
	writeSkillFile(t, filepath.Join(homeDir, ".claude", "skills"), "user-only", `---
name: user-only
description: Personal formatting helper
---
# Format helper
`)
	// Workspace A overrides shared-review and adds ws-deploy with scripts/
	writeSkillFile(t, filepath.Join(wsA, ".agents", "skills"), "shared-review", `---
name: shared-review
description: Repo A specific code review rules
---
# Repo A review
`)
	writeSkillFile(t, filepath.Join(wsA, ".agents", "skills"), "ws-deploy", `---
name: ws-deploy
description: Deploy Repo A safely
---
# Deploy
`)
	if err := os.MkdirAll(filepath.Join(wsA, ".agents", "skills", "ws-deploy", "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}

	falseVal := false
	rules := map[string]store.SkillVisibilityRule{
		"user-only": {
			WorkspaceOverrides: map[string]bool{
				store.NormalizeWorkspaceKey(wsA): false,
			},
		},
		"ws-deploy": {
			GlobalEnabled: &falseVal,
			WorkspaceOverrides: map[string]bool{
				store.NormalizeWorkspaceKey(wsA): true,
			},
		},
	}

	// Discover in Workspace A
	foundA := Discover(DiscoverOptions{
		WorkspacePath:  wsA,
		HomeDir:        homeDir,
		Rules:          rules,
		IncludeContent: true,
	})
	if len(foundA) != 4 {
		t.Fatalf("expected 4 discovered entries (including shadowed user shared-review), got %d: %+v", len(foundA), foundA)
	}

	// Verify workspace skill shadows user skill of same name
	var wsShared, userShared, wsDeploy *DiscoveredSkill
	for i := range foundA {
		s := &foundA[i]
		if s.Name == "shared-review" && s.Scope == "workspace" {
			wsShared = s
		}
		if s.Name == "shared-review" && s.Scope == "user" {
			userShared = s
		}
		if s.Name == "ws-deploy" {
			wsDeploy = s
		}
	}
	if wsShared == nil || wsShared.ShadowedBy != "" {
		t.Fatalf("expected workspace shared-review to win precedence, got %+v", wsShared)
	}
	if userShared == nil || userShared.ShadowedBy != "workspace" {
		t.Fatalf("expected user shared-review to be shadowed by workspace, got %+v", userShared)
	}
	if wsDeploy == nil || !wsDeploy.HasScripts || !wsDeploy.WorkspaceEnabled || !wsDeploy.WorkspaceOverridden {
		t.Fatalf("expected ws-deploy to have scripts and be enabled via workspace override, got %+v", wsDeploy)
	}

	// Prompt skills in Workspace A: shared-review (workspace) + ws-deploy (override true); user-only hidden!
	promptA := VisiblePromptSkills(DiscoverOptions{
		WorkspacePath: wsA,
		HomeDir:       homeDir,
		Rules:         rules,
	})
	if len(promptA) != 2 {
		t.Fatalf("expected 2 visible prompt skills in wsA, got %d: %+v", len(promptA), promptA)
	}

	// Prompt skills in Workspace B: shared-review (user) + user-only (visible in wsB!)
	promptB := VisiblePromptSkills(DiscoverOptions{
		WorkspacePath: wsB,
		HomeDir:       homeDir,
		Rules:         rules,
	})
	if len(promptB) != 2 {
		t.Fatalf("expected 2 visible prompt skills in wsB, got %d: %+v", len(promptB), promptB)
	}
}
