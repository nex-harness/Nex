// service.go: the ISettingsSyncService surface — detect, importSelected,
// getFirstRunPromptState, markFirstRunPromptHandled, and the Claude
// AGENTS.md migration pair. The discovery/import builders live in
// import_skills.go / import_commands.go / import_plugins.go /
// import_mcp.go / import_claude_agents.go.
package settingssync

import (
	"context"
)

// SettingService is the ISettingService dependency (first-run prompt state).
type SettingService interface {
	Get(ctx context.Context) (settings map[string]any, err error)
	Update(ctx context.Context, patch map[string]any) error
}

// Options configures NewService.
type Options struct {
	// SettingService supplies settingsSyncFirstRunPromptHandled.
	SettingService SettingService
	// Home is the user home (nil → resolveUserHomeDir: HOME > USERPROFILE).
	Home string
	// Env supplies env reads (tests); nil reads the process env.
	Env func() map[string]string
}

// Service is ISettingsSyncService.
type Service struct {
	setting SettingService
	home    string
	env     func() map[string]string
}

// NewService builds the service (createSettingsSyncService).
func NewService(opts Options) *Service {
	env := opts.Env
	if env == nil {
		env = defaultEnvReader
	}
	home := opts.Home
	if home == "" {
		for _, key := range []string{"HOME", "USERPROFILE"} {
			if v := trimSpaceOf(env()[key]); v != "" {
				home = v
				break
			}
		}
		if home == "" {
			home = userHomeDirOrEmpty()
		}
	}
	return &Service{setting: opts.SettingService, home: home, env: env}
}

func (s *Service) layoutFor(workspacePath string) *layout {
	return &layout{home: s.home, workspacePath: workspacePath}
}

// DetectRequest is the detect() request.
type DetectRequest struct {
	WorkspaceRef
	Categories []string
	Intent     string // "firstRun" | "manualImport"
}

// shouldScan* ports the shouldScan* family: ONLY manualImport with the
// matching category scans.
func shouldScan(request DetectRequest, category string) bool {
	return request.Intent == "manualImport" && containsString(request.Categories, category)
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

// Detect ports detect().
func (s *Service) Detect(ctx context.Context, request DetectRequest) (*DiscoveryResult, error) {
	l := s.layoutFor(request.WorkspacePath)
	switch {
	case shouldScan(request, CategorySkills):
		return s.buildSkillsDiscovery(l), nil
	case shouldScan(request, CategoryCommands):
		return s.buildCommandsDiscovery(l), nil
	case shouldScan(request, CategoryPlugins):
		return s.buildPluginsDiscovery(l), nil
	case shouldScan(request, CategoryMcpServers):
		return s.buildMcpDiscovery(l), nil
	}
	return &DiscoveryResult{Agents: []AgentSummary{}}, nil
}

// GetFirstRunPromptState ports getFirstRunPromptState.
func (s *Service) GetFirstRunPromptState(ctx context.Context) (FirstRunPromptState, error) {
	settings, err := s.setting.Get(ctx)
	if err != nil {
		return FirstRunPromptState{}, err
	}
	return FirstRunPromptState{Handled: settings["settingsSyncFirstRunPromptHandled"] == true}, nil
}

// MarkFirstRunPromptHandled ports markFirstRunPromptHandled: 这里记录的是
// "首启导入提示是否已经被消费"，不是"导入是否成功"。
func (s *Service) MarkFirstRunPromptHandled(ctx context.Context) error {
	return s.setting.Update(ctx, map[string]any{"settingsSyncFirstRunPromptHandled": true})
}

// FirstRunPromptState is SettingsSyncFirstRunPromptState.
type FirstRunPromptState struct {
	Handled bool `json:"handled"`
}
