package promptctx

import (
	"encoding/json"
	"os"
	"testing"
)

type vecConfig struct {
	Env struct {
		Cwd             string   `json:"cwd"`
		Platform        string   `json:"platform"`
		Shell           string   `json:"shell"`
		OSVersion       string   `json:"osVersion"`
		IsGitRepository *bool    `json:"isGitRepository"`
		GitStatus       string   `json:"gitStatus"`
		GitBranch       string   `json:"gitBranch"`
		GitMainBranch   string   `json:"gitMainBranch"`
		GitUser         string   `json:"gitUser"`
		GitStatusLines  []string `json:"gitStatusLines"`
		RecentCommits   []string `json:"recentCommits"`
	} `json:"envInfo"`
	Model *struct {
		ProviderID string `json:"providerId"`
		ModelID    string `json:"modelId"`
	} `json:"model"`
	Surface string `json:"presentationSurface"`
	Date    string `json:"currentDate"`
	Skills  *struct {
		Skills []struct {
			Name          string `json:"name"`
			QualifiedName string `json:"qualifiedName"`
			Description   string `json:"description"`
			WhenToUse     string `json:"whenToUse"`
			Path          string `json:"path"`
		} `json:"skills"`
	} `json:"skills"`
	SkillBudget  int `json:"skillMetadataBudget"`
	Instructions *struct {
		Scope     string `json:"scope"`
		FilePath  string `json:"filePath"`
		FileName  string `json:"fileName"`
		Content   string `json:"content"`
		Truncated bool   `json:"truncated"`
		Sources   []struct {
			Scope     string `json:"scope"`
			FilePath  string `json:"filePath"`
			FileName  string `json:"fileName"`
			Content   string `json:"content"`
			Truncated bool   `json:"truncated"`
		} `json:"sources"`
	} `json:"userInstructions"`
	MemoryRoot   string                         `json:"memoryRoot"`
	MemoryIndex  *string                        `json:"memoryIndexContent"`
	Custom       string                         `json:"customSystemPrompt"`
	OutputStyle  *struct{ Name, Prompt string } `json:"outputStyle"`
	GuidanceTool *[]string                      `json:"guidanceToolNames"`
}

type vector struct {
	Name         string                             `json:"name"`
	Config       vecConfig                          `json:"config"`
	SystemCount  int                                `json:"systemMessageCount"`
	MergedSystem *string                            `json:"mergedSystem"`
	MetaUser     []struct{ Source, Content string } `json:"metaUser"`
	Sections     []struct {
		Name, Source, InjectionTarget, CacheHint string
		Chars, Tokens                            int
	} `json:"sections"`
}

func (v vecConfig) toConfig() Config {
	c := Config{Surface: v.Surface, CurrentDate: v.Date, MemoryRoot: v.MemoryRoot, MemoryIndexContent: v.MemoryIndex,
		CustomSystemPrompt: v.Custom, SkillMetadataBudget: v.SkillBudget}
	e := v.Env
	c.Env = EnvInfo{Cwd: e.Cwd, Platform: e.Platform, Shell: e.Shell, OSVersion: e.OSVersion, IsGitRepository: e.IsGitRepository,
		GitStatus: e.GitStatus, GitBranch: e.GitBranch, GitMainBranch: e.GitMainBranch, GitUser: e.GitUser,
		GitStatusLines: e.GitStatusLines, RecentCommits: e.RecentCommits}
	if v.Model != nil {
		c.Model = &Model{v.Model.ProviderID, v.Model.ModelID}
	}
	if v.Skills != nil {
		for _, s := range v.Skills.Skills {
			c.Skills = append(c.Skills, Skill{Name: s.Name, QualifiedName: s.QualifiedName, Description: s.Description, WhenToUse: s.WhenToUse, Path: s.Path})
		}
	}
	if i := v.Instructions; i != nil {
		if len(i.Sources) > 0 {
			for _, s := range i.Sources {
				c.Instructions = append(c.Instructions, InstructionSource{Scope: s.Scope, FilePath: s.FilePath, FileName: s.FileName, Content: s.Content, Truncated: s.Truncated})
			}
		} else {
			c.Instructions = []InstructionSource{{Scope: "workspace", FilePath: i.FilePath, FileName: i.FileName, Content: i.Content, Truncated: i.Truncated}}
		}
	}
	if v.OutputStyle != nil {
		c.OutputStyle = &OutputStyle{v.OutputStyle.Name, v.OutputStyle.Prompt}
	}
	if v.GuidanceTool != nil {
		c.GuidanceToolsSet, c.GuidanceToolNames = true, *v.GuidanceTool
	}
	return c
}

func TestBuilderMatchesTypeScriptOnEveryVector(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/vectors/context.json")
	if err != nil {
		t.Fatal(err)
	}
	var vecs []vector
	if err := json.Unmarshal(raw, &vecs); err != nil {
		t.Fatal(err)
	}
	if len(vecs) < 30 {
		t.Fatalf("vector file truncated: %d", len(vecs))
	}
	for _, v := range vecs {
		t.Run(v.Name, func(t *testing.T) {
			if v.Config.MemoryIndex != nil && MemoryNeedsMarkdownLexer(*v.Config.MemoryIndex) {
				t.Skip("needs the marked lexer; not ported")
			}
			got := Build(v.Config.toConfig())
			if v.MergedSystem == nil {
				t.Fatalf("vector without system text")
			}
			if got.MergedSystem != *v.MergedSystem {
				t.Errorf("system prompt differs\n%s", firstDiff(got.MergedSystem, *v.MergedSystem))
			}
			if len(got.MetaUser) != len(v.MetaUser) {
				t.Fatalf("meta-user blocks: go %d, ts %d", len(got.MetaUser), len(v.MetaUser))
			}
			for i := range v.MetaUser {
				if got.MetaUser[i].Source != v.MetaUser[i].Source || got.MetaUser[i].Content != v.MetaUser[i].Content {
					t.Errorf("meta-user %d (%s) differs\n%s", i, v.MetaUser[i].Source, firstDiff(got.MetaUser[i].Content, v.MetaUser[i].Content))
				}
			}
			if len(got.Sections) != len(v.Sections) {
				t.Fatalf("sections: go %d, ts %d", len(got.Sections), len(v.Sections))
			}
			for i, w := range v.Sections {
				g := got.Sections[i]
				if g.Name != w.Name || g.Source != w.Source || string(g.Target) != w.InjectionTarget || string(g.Hint) != w.CacheHint ||
					g.Chars() != w.Chars || g.Tokens() != w.Tokens {
					t.Errorf("section %d: go {%s %s %s %s chars=%d tokens=%d} ts %+v", i, g.Name, g.Source, g.Target, g.Hint, g.Chars(), g.Tokens(), w)
				}
			}
		})
	}
}

func firstDiff(a, b string) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			lo := max(0, i-40)
			return "at byte " + itoa(i) + "\n go: " + quote(a[lo:min(len(a), i+60)]) + "\n ts: " + quote(b[lo:min(len(b), i+60)])
		}
	}
	return "lengths differ: go " + itoa(len(a)) + ", ts " + itoa(len(b))
}

func itoa(n int) string     { b, _ := json.Marshal(n); return string(b) }
func quote(s string) string { b, _ := json.Marshal(s); return string(b) }
