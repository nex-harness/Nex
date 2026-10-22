import { spawn } from "node:child_process";
import { access, chmod, mkdir, realpath } from "node:fs/promises";
import { createHash } from "node:crypto";
import { dirname, join, resolve, sep } from "node:path";

/**
 * The thin `git` boundary of the checkpoint service (spec: docs/next/attachments-rewind.md B).
 *
 * Every call is `spawn("git", [args…])` with an argument array — never a shell — so paths
 * with spaces/quotes and Windows drive letters never need quoting. Paths handed to git use
 * forward slashes (`toGitPath`), and the shadow repo is pinned to
 * `core.autocrlf=false`/`core.fileMode=false` so NTFS mode bits and CRLF conversion cannot
 * make an untouched file look changed.
 */

export type GitFailureKind = "unavailable" | "failed";

export class GitError extends Error {
  constructor(
    readonly kind: GitFailureKind,
    message: string,
    readonly stderr = "",
  ) {
    super(message);
    this.name = "GitError";
  }
}

export interface GitRunOptions {
  input?: string | Buffer;
  /** Defaults to 120 s; checkpoints run inside a tool call, so they must never hang a turn. */
  timeoutMs?: number;
  /** Extra environment (e.g. `GIT_INDEX_FILE` for an isolated `add`). */
  env?: Record<string, string>;
}

export interface GitResult {
  /** Trimmed UTF-8 stdout. */
  stdout: string;
  /** Raw stdout bytes (blob content must not be decoded/normalized). */
  stdoutBuffer: Buffer;
  stderr: string;
}

/** `git` runner bound to one shadow repository and work tree. */
export class CheckpointGit {
  constructor(
    private readonly gitDir: string,
    private readonly workTree: string,
    private readonly executable = "git",
  ) {}

  get repositoryDir(): string {
    return this.gitDir;
  }

  get root(): string {
    return this.workTree;
  }

  /** `git --git-dir=… --work-tree=…` with no shell, never interactive. */
  run(args: string[], options: GitRunOptions = {}): Promise<GitResult> {
    return this.spawnGit(
      [`--git-dir=${toGitPath(this.gitDir)}`, `--work-tree=${toGitPath(this.workTree)}`, ...args],
      this.workTree,
      options,
    );
  }

  /** Binary probe: `git --version` with no repository flags and an existing cwd. */
  probeVersion(): Promise<GitResult> {
    return this.spawnGit(["--version"], process.cwd(), { timeoutMs: 15_000 });
  }

  private spawnGit(full: string[], cwd: string, options: GitRunOptions): Promise<GitResult> {
    const args = full;
    const env: Record<string, string | undefined> = {
      ...process.env,
      GIT_TERMINAL_PROMPT: "0",
      GIT_OPTIONAL_LOCKS: "0",
      LC_ALL: "C",
      ...options.env,
    };
    // A user-level `core.hooksPath`/alias must never widen what a checkpoint does.
    env.GIT_CONFIG_NOSYSTEM = "1";
    return new Promise<GitResult>((resolvePromise, reject) => {
      const child = spawn(this.executable, full, {
        cwd,
        env: env as NodeJS.ProcessEnv,
        shell: false,
        windowsHide: true,
        stdio: ["pipe", "pipe", "pipe"],
      });
      const stdout: Buffer[] = [];
      const stderr: Buffer[] = [];
      let settled = false;
      const timer = setTimeout(() => {
        if (settled) return;
        settled = true;
        child.kill("SIGKILL");
        reject(new GitError("failed", `git ${args[0] ?? ""} timed out`));
      }, options.timeoutMs ?? 120_000);
      child.stdout.on("data", (chunk: Buffer) => stdout.push(chunk));
      child.stderr.on("data", (chunk: Buffer) => stderr.push(chunk));
      child.on("error", (error: NodeJS.ErrnoException) => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        reject(
          error.code === "ENOENT"
            ? new GitError("unavailable", `git executable not found (${error.message})`)
            : new GitError("failed", error.message),
        );
      });
      child.on("close", (code) => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        const outBuffer = Buffer.concat(stdout);
        const errText = Buffer.concat(stderr).toString("utf8");
        if (code !== 0) {
          reject(
            new GitError(
              "failed",
              `git ${args.join(" ")} exited with ${code}: ${errText.trim().slice(0, 400)}`,
              errText,
            ),
          );
          return;
        }
        resolvePromise({
          stdout: outBuffer.toString("utf8").replace(/\r?\n$/u, ""),
          stdoutBuffer: outBuffer,
          stderr: errText,
        });
      });
      child.stdin.on("error", () => {
        // EPIPE when git exits early (e.g. `check-ignore` without input); the exit code
        // decides the outcome, not the stdin writer.
      });
      if (options.input !== undefined) child.stdin.end(options.input);
      else child.stdin.end();
    });
  }
}

/** git wants forward slashes even on Windows. */
export function toGitPath(path: string): string {
  return path.replaceAll("\\", "/");
}

/** One shadow repository per workspace: `sha256(realpath(ws))[0:16]` (spec B). */
export function shadowRepoDir(dataDir: string, workspaceRealPath: string): string {
  const hash = createHash("sha256").update(workspaceRealPath).digest("hex").slice(0, 16);
  return join(dataDir, "checkpoints", `${hash}.git`);
}

export interface EnsureRepoOptions {
  workspaceRealPath: string;
  dataDir: string;
  gitExecutable?: string;
}

/**
 * Creates (once) and validates the shadow repository. Returns the runner bound to it.
 *
 * Layout: `--git-dir=<data>/checkpoints/<hash>.git --work-tree=<workspace>`; the workspace
 * is never modified (no `.git` inside it), so a workspace that has its own repository is
 * unaffected. `core.worktree` is set so plain `git` invocations inside the shadow dir agree.
 */
export async function ensureShadowRepo(options: EnsureRepoOptions): Promise<CheckpointGit> {
  const gitDir = shadowRepoDir(options.dataDir, options.workspaceRealPath);
  const git = new CheckpointGit(gitDir, options.workspaceRealPath, options.gitExecutable);
  await mkdir(dirname(gitDir), { recursive: true });
  const hasHead = await access(join(gitDir, "HEAD")).then(
    () => true,
    () => false,
  );
  if (!hasHead) {
    // A normal (non-bare) repository whose work tree is the workspace only for OUR calls:
    // `--work-tree` is passed on every invocation, so nothing is written into the workspace
    // and `core.worktree` is deliberately left unset (a bare init cannot record it).
    await git.run(["init", "--quiet", toGitPath(gitDir)], { timeoutMs: 30_000 });
    await writeRepoConfig(git);
  }
  return git;
}

/**
 * Windows/desktop-safe repository settings. `fileMode=false` keeps chmod differences from
 * showing as modifications; `autocrlf=false` keeps the stored bytes identical to the work
 * tree so hashes and restores round-trip; `longpaths=true` lifts the 260-char limit.
 * `core.bare` stays false while `core.worktree` remains unset: every runner call supplies
 * `--work-tree`, and an explicit value would leak the absolute path into a movable repo.
 */
async function writeRepoConfig(git: CheckpointGit): Promise<void> {
  const settings: Array<[string, string]> = [
    ["core.autocrlf", "false"],
    ["core.safecrlf", "false"],
    ["core.fileMode", "false"],
    ["core.longpaths", "true"],
    ["core.fsmonitor", "false"],
    ["gc.auto", "0"],
    ["gc.autoDetach", "false"],
    ["maintenance.auto", "false"],
  ];
  for (const [key, value] of settings) {
    await git.run(["config", "--local", key, value], { timeoutMs: 20_000 }).catch(() => undefined);
  }
  await chmod(git.repositoryDir, 0o700).catch(() => undefined);
}

/** True when `path` is inside `root` (both real paths, case-insensitive on Windows). */
export function isInside(root: string, path: string): boolean {
  const base = process.platform === "win32" ? root.toLowerCase() : root;
  const candidate = process.platform === "win32" ? path.toLowerCase() : path;
  return candidate === base || candidate.startsWith(base.endsWith(sep) ? base : `${base}${sep}`);
}

/**
 * Canonicalizes the workspace root once (macOS `/var` → `/private/var`, Windows 8.3 names).
 * Callers keep the canonical form; paths that already start with it need no realpath.
 */
export async function canonicalRoot(root: string): Promise<string> {
  try {
    return await realpath(root);
  } catch {
    return resolve(root);
  }
}

/**
 * Workspace-relative path from an absolute (possibly symlinked) path, or undefined.
 * Symlinks are resolved on the deepest existing ancestor, so a link pointing outside the
 * workspace never yields a relative path.
 */
export async function relativeInside(
  root: string,
  path: string,
  canonical = root,
): Promise<string | undefined> {
  const absolute = resolve(root, path);
  let real: string | undefined;
  try {
    real = await realpath(absolute);
  } catch {
    // A not-yet-created file (write/apply) has no realpath; canonicalize its parent.
    const parent = dirname(absolute);
    try {
      real = join(await realpath(parent), absolute.slice(parent.length + 1));
    } catch {
      real = undefined;
    }
  }
  const candidates = real ? [real, absolute] : [absolute];
  for (const candidate of candidates) {
    if (!isInside(canonical, candidate)) continue;
    return toGitPath(candidate.slice(canonical.length + (canonical.endsWith(sep) ? 0 : 1)));
  }
  return undefined;
}
