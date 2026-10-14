// provider.go ports packages/services/src/git/providers/ (step 3.3): the git
// binary discovery (gitEnvironmentProvider.ts) and the command runner
// (gitCommandProvider.ts), both built on the Phase-1 exec adapter.
//
// 与 TS 一致的关键语义：
//   - 每个命令都是 argv（不经过 shell），env 统一来自 GetGitCommandEnv；
//   - 输出有硬上限，超限即截断并杀进程（outputTruncated）；
//   - 超时先 SIGTERM、再 SIGKILL 整棵进程树；进程退出码与截断状态都随结果返回；
//   - spawn 失败（cwd 被删等）降级为 exitCode=-2 的失败结果而不是 panic，上层
//     按既有 ensureGitCommandSucceeded 语义处理 —— fire-and-forget 调用链不会
//     因此泄漏进程级异常。
package git

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/tylinux/nex/go/internal/adapters/exec"
)

// GitCommandExecutionOptions ports GitCommandExecutionOptions.
type GitCommandExecutionOptions struct {
	Cwd            string
	Args           []string
	TimeoutMs      int64
	MaxOutputBytes int64
	// Env layers per-invocation variables (GIT_INDEX_FILE for temp-index
	// commits/checkpoints) over the scrubbed base env.
	Env map[string]string
}

// GitCommandExecutionResult ports GitCommandExecutionResult. ExitCode is nil
// when the process could not be run or its status could not be observed
// (timeout + orphaned, or spawn failure); timedOut/outputTruncated mirror the
// TS flags.
type GitCommandExecutionResult struct {
	BinaryPath      string
	Cwd             string
	Args            []string
	Stdout          string
	Stderr          string
	ExitCode        *int
	Signal          string
	DurationMs      int64
	TimedOut        bool
	TimeoutMs       int64
	ForceKillDone   bool
	OutputTruncated bool
}

// ExitCodeOr reports the exit code, or fallback when it was never observed.
func (r *GitCommandExecutionResult) ExitCodeOr(fallback int) int {
	if r.ExitCode == nil {
		return fallback
	}
	return *r.ExitCode
}

// GitCommandProvider ports the TS interface of the same name.
type GitCommandProvider interface {
	ResolveGitBinary(ctx context.Context) (string, error)
	Run(ctx context.Context, options GitCommandExecutionOptions) (GitCommandExecutionResult, error)
}

// CommandProviderOptions configures NewCommandProvider.
type CommandProviderOptions struct {
	// Environ is the process environment source (nil -> os.Environ, read once).
	Environ []string
	// Getenv resolves one variable for binary discovery (nil -> os.Getenv).
	Getenv func(string) string
	// IsWindows selects the null-device path and kill strategy (nil -> runtime).
	IsWindows *bool
	// DiscoveryTimeoutMs bounds one `git --version` probe (0 -> 3s).
	DiscoveryTimeoutMs int64
}

// CommandProvider is the production GitCommandProvider.
type CommandProvider struct {
	environ     []string
	getenv      func(string) string
	isWindows   bool
	discoveryMs int64

	resolveOnce sync.Once
	binary      string
	resolveErr  error
}

// NewCommandProvider builds the provider; env is captured once, matching the
// TS module-level snapshot behaviour.
func NewCommandProvider(opts CommandProviderOptions) *CommandProvider {
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	if opts.Environ == nil {
		opts.Environ = os.Environ()
	}
	isWindows := IsWindowsOS()
	if opts.IsWindows != nil {
		isWindows = *opts.IsWindows
	}
	if opts.DiscoveryTimeoutMs <= 0 {
		opts.DiscoveryTimeoutMs = DefaultGitDiscoveryTimeoutMs
	}
	return &CommandProvider{environ: opts.Environ, getenv: opts.Getenv, isWindows: isWindows, discoveryMs: opts.DiscoveryTimeoutMs}
}

// baseEnv returns the scrubbed command environment (computed lazily, cached).
func (p *CommandProvider) baseEnv() []string {
	return GetGitCommandEnv(p.environ)
}

// ResolveGitBinary ports resolveGitBinary: probe each candidate once with
// `git --version`, cache the outcome. An empty binary path is NOT an error —
// callers treat it as "git unavailable" exactly like the TS's null.
func (p *CommandProvider) ResolveGitBinary(ctx context.Context) (string, error) {
	p.resolveOnce.Do(func() {
		env := p.baseEnv()
		for _, candidate := range GetGitBinaryCandidates(p.getenv, p.isWindows) {
			if p.probeCandidate(ctx, candidate, env) {
				p.binary = candidate
				return
			}
		}
	})
	return p.binary, p.resolveErr
}

// probeCandidate runs `<candidate> --version` with the discovery timeout.
func (p *CommandProvider) probeCandidate(ctx context.Context, candidate string, env []string) bool {
	probeCtx, cancel := context.WithTimeout(ctx, time.Duration(p.discoveryMs)*time.Millisecond)
	defer cancel()
	proc, err := exec.Start(probeCtx, exec.Options{
		Args:         []string{candidate, "--version"},
		Env:          env,
		MaxTailBytes: -1,
	})
	if err != nil {
		return false
	}
	result := proc.Wait()
	return result.ExitCode == 0 && !result.Killed()
}

// Run ports GitCommandProvider.run: spawn via the exec adapter, cap both output
// streams, surface timeout/truncation. The error return is non-nil only for a
// programmer-level failure (no args / git unavailable); command failures are
// expressed in the result, like the TS.
func (p *CommandProvider) Run(ctx context.Context, command GitCommandExecutionOptions) (GitCommandExecutionResult, error) {
	if len(command.Args) == 0 {
		return GitCommandExecutionResult{}, errors.New("git: command args must not be empty")
	}
	binary, err := p.ResolveGitBinary(ctx)
	if err != nil {
		return GitCommandExecutionResult{}, err
	}
	if binary == "" {
		return GitCommandExecutionResult{}, errors.New("Git binary is not available")
	}

	timeoutMs := command.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = DefaultGitCommandTimeoutMs
	}
	maxOutputBytes := command.MaxOutputBytes
	if maxOutputBytes <= 0 {
		maxOutputBytes = DefaultGitOutputBytes
	}

	env := p.baseEnv()
	if len(command.Env) > 0 {
		env = append(env, envPairs(command.Env)...)
	}

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	startedAt := time.Now()
	proc, err := exec.Start(runCtx, exec.Options{
		Args:           append([]string{binary}, command.Args...),
		Dir:            command.Cwd,
		Env:            env,
		MaxOutputBytes: maxOutputBytes,
		MaxTailBytes:   -1,
	})
	if err != nil {
		// spawn 失败（cwd 被并发删除 / git 瞬时缺失）降级为 -2 失败结果，
		// 与 TS 的 child.once("error") 分支一致。
		exit := -2
		return GitCommandExecutionResult{
			BinaryPath: binary,
			Cwd:        command.Cwd,
			Args:       command.Args,
			Stderr:     err.Error(),
			ExitCode:   &exit,
			DurationMs: time.Since(startedAt).Milliseconds(),
			TimeoutMs:  timeoutMs,
		}, nil
	}
	result := proc.Wait()

	out := GitCommandExecutionResult{
		BinaryPath:      binary,
		Cwd:             command.Cwd,
		Args:            command.Args,
		Stdout:          result.Stdout.Text,
		Stderr:          result.Stderr.Text,
		Signal:          result.Signal,
		DurationMs:      time.Since(startedAt).Milliseconds(),
		TimedOut:        result.Killed() && runCtx.Err() != nil,
		TimeoutMs:       timeoutMs,
		OutputTruncated: result.Stdout.Truncated || result.Stderr.Truncated,
	}
	if result.ExitCode >= 0 {
		code := result.ExitCode
		out.ExitCode = &code
	}
	if out.OutputTruncated && out.ExitCode == nil {
		// 截断时 TS 会 kill 子进程；exec adapter 已通过 ctx 完成清理。
		zero := 0
		out.ExitCode = &zero
	}
	return out, nil
}

// envPairs renders a map as KEY=VALUE entries.
func envPairs(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for key, value := range env {
		out = append(out, key+"="+value)
	}
	return out
}

var _ GitCommandProvider = (*CommandProvider)(nil)
