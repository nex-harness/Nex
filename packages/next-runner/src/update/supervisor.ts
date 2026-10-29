/**
 * How a restarted runner comes back (docs/next/runner-update.md decision 5).
 *
 * A self-update ends by exiting with code 75. Whether the supervisor restarts it
 * depends on how the runner was started:
 * - systemd (`Restart=always`) and launchd (`KeepAlive`) restart on any exit.
 * - The installer's wrapper loop restarts only on 75.
 * - Older installs (Windows `start-runner.cmd`, `setsid` fallback) have no
 *   supervisor at all, so the runner spawns a detached copy of itself first.
 *
 * The server's installer will set `NEX_RUNNER_SUPERVISOR` in the env file going
 * forward. Existing installs are detected from the environment the supervisor
 * leaves behind, which is why the heuristics below exist.
 */

export type SupervisorMode = "managed" | "respawn";

/**
 * `managed` = exit 75 and let the supervisor/wrapper restart us.
 * `respawn` = no supervisor, so spawn a detached copy before exiting.
 */
export function resolveSupervisorMode(
  env: NodeJS.ProcessEnv,
  platform: NodeJS.Platform = process.platform,
): SupervisorMode {
  const declared = env["NEX_RUNNER_SUPERVISOR"]?.trim().toLowerCase();
  if (declared === "systemd" || declared === "launchd" || declared === "loop") return "managed";
  // 显式 none（或未设置）走下面的平台探测；只有检测不到任何托管者时才自举。
  if (declared === "none") return "respawn";
  if (declared) {
    // 未知值按“没有托管者”处理：宁可多一次 detached 重启，也不要退出后不再回来。
    return "respawn";
  }
  if (platform === "darwin") {
    // launchd 给作业注入 XPC_SERVICE_NAME；只认本 runner 的 label，
    // 避免把用户 shell 里其他 launchd 上下文误判成 KeepAlive。
    const xpc = env["XPC_SERVICE_NAME"];
    if (typeof xpc === "string" && xpc.includes("cn.nex.runner")) return "managed";
    return "respawn";
  }
  if (platform === "linux") {
    // systemd 服务进程一定有 INVOCATION_ID；setsid/nohup 启动的没有。
    return env["INVOCATION_ID"] ? "managed" : "respawn";
  }
  // win32（旧 schtasks 包装）与其他平台：没有可依赖的托管者。
  return "respawn";
}

export interface SpawnedChild {
  unref?: () => void;
}

export type SpawnFn = (
  command: string,
  args: readonly string[],
  options: {
    detached: true;
    stdio: "ignore";
    cwd: string;
    env: NodeJS.ProcessEnv;
  },
) => SpawnedChild;

export interface RespawnOptions {
  execPath?: string;
  argv?: readonly string[];
  cwd?: string;
  env?: NodeJS.ProcessEnv;
  spawn: SpawnFn;
}

/**
 * Spawns a detached copy of this process with the same argv/cwd/env and unrefs it,
 * so the caller can exit without killing the successor. `argv` follows Node's
 * convention: `process.argv` starts with the executable path.
 */
export function respawnDetached(options: RespawnOptions): void {
  const argv = options.argv ?? process.argv;
  const child = options.spawn(options.execPath ?? process.execPath, argv.slice(1), {
    detached: true,
    stdio: "ignore",
    cwd: options.cwd ?? process.cwd(),
    env: options.env ?? process.env,
  });
  child.unref?.();
}
