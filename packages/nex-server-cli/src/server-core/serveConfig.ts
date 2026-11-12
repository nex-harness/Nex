import { randomUUID } from "node:crypto";
import { chmod, mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import { dirname, isAbsolute } from "node:path";
import { z } from "zod";
import type { ServerLayout } from "../runtime/paths.js";

// serve 的监听/托管参数。OS service 与 restart 后的 Core 不继承注册时的 shell 环境，
// 所以参数落盘，Core 启动时读取；环境变量覆盖文件。规则见 docs/specs/server-web-login.md。

export const SERVE_HOST_ENV = "NEX_SERVER_LISTEN_HOST";
export const SERVE_PORT_ENV = "NEX_SERVER_LISTEN_PORT";
export const SERVE_WEB_ROOT_ENV = "NEX_SERVER_WEB_ROOT";

const serveConfigSchema = z.object({
  version: z.literal(1),
  host: z.string().trim().min(1).optional(),
  port: z.number().int().min(0).max(65535).optional(),
  webRoot: z
    .string()
    .min(1)
    .refine((value) => isAbsolute(value), "webRoot must be an absolute path")
    .optional(),
});

export type ServeConfig = Omit<z.infer<typeof serveConfigSchema>, "version">;

export function isLoopbackHost(host: string): boolean {
  const normalized = host.trim().toLowerCase();
  return normalized === "127.0.0.1" || normalized === "::1" || normalized === "localhost";
}

export function parsePortValue(raw: string, source: string): number {
  const value = Number(raw);
  if (!/^\d+$/.test(raw.trim()) || !Number.isInteger(value) || value < 0 || value > 65535) {
    throw new Error(`${source} must be an integer between 0 and 65535`);
  }
  return value;
}

export async function readServeConfig(layout: ServerLayout): Promise<ServeConfig> {
  let raw: string;
  try {
    raw = await readFile(layout.serveConfigFile, "utf8");
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return {};
    throw error;
  }
  // 损坏的配置直接失败：静默忽略会让一个本应只监听回环的 Server 之外的设置悄悄失效，
  // 或让用户以为已经开启的 Web 托管不存在。
  const parsed = serveConfigSchema.safeParse(JSON.parse(raw));
  if (!parsed.success) {
    throw new Error(`Invalid ${layout.serveConfigFile}: ${parsed.error.issues[0]?.message}`);
  }
  const { version: _version, ...config } = parsed.data;
  return config;
}

export async function writeServeConfig(layout: ServerLayout, config: ServeConfig): Promise<void> {
  await mkdir(dirname(layout.serveConfigFile), { recursive: true, mode: 0o700 });
  const temporary = `${layout.serveConfigFile}.${process.pid}.${randomUUID()}.tmp`;
  try {
    await writeFile(temporary, `${JSON.stringify({ version: 1, ...config }, null, 2)}\n`, {
      encoding: "utf8",
      mode: 0o600,
    });
    await rename(temporary, layout.serveConfigFile);
    if (process.platform !== "win32") await chmod(layout.serveConfigFile, 0o600);
  } finally {
    await rm(temporary, { force: true }).catch(() => undefined);
  }
}

export interface ResolvedServeOptions {
  host: string | undefined;
  port: number | undefined;
  webRoot: string | undefined;
  /** 非回环监听或托管 Web UI 或显式提供 web token：启用浏览器登录。 */
  webLoginEnabled: boolean;
}

/** 环境变量 > serve-config.json。 */
export async function resolveServeOptions(
  layout: ServerLayout,
  env: NodeJS.ProcessEnv = process.env,
): Promise<ResolvedServeOptions> {
  const file = await readServeConfig(layout);
  const host = env[SERVE_HOST_ENV]?.trim() || file.host;
  const portEnv = env[SERVE_PORT_ENV]?.trim();
  const port = portEnv ? parsePortValue(portEnv, SERVE_PORT_ENV) : file.port;
  const webRoot = env[SERVE_WEB_ROOT_ENV]?.trim() || file.webRoot;
  if (webRoot && !isAbsolute(webRoot)) throw new Error(`${SERVE_WEB_ROOT_ENV} must be absolute`);
  const webLoginEnabled =
    Boolean(webRoot) ||
    (host !== undefined && !isLoopbackHost(host)) ||
    Boolean(env["NEX_SERVER_WEB_TOKEN"]?.trim());
  return { host, port, webRoot, webLoginEnabled };
}

export interface ServeFlagResult {
  /** 命令行显式给出的字段；没有任何监听/托管参数时为 undefined。 */
  patch: ServeConfig | undefined;
  clear: boolean;
  /** 去掉本组参数后剩余的 argv。 */
  rest: string[];
}

/** 解析 `serve` 的 --host / --port / --web-root / --clear-serve-config；其余参数原样保留。 */
export function parseServeFlags(args: readonly string[]): ServeFlagResult {
  const patch: ServeConfig = {};
  const rest: string[] = [];
  let clear = false;
  let touched = false;
  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index] as string;
    const takeValue = (): string => {
      const value = args[index + 1];
      if (value === undefined || value.startsWith("--")) throw new Error(`${arg} requires a value`);
      index += 1;
      return value;
    };
    if (arg === "--host") {
      patch.host = takeValue().trim();
      touched = true;
    } else if (arg === "--port") {
      patch.port = parsePortValue(takeValue(), "--port");
      touched = true;
    } else if (arg === "--web-root") {
      const value = takeValue();
      if (!isAbsolute(value)) throw new Error("--web-root must be an absolute path");
      patch.webRoot = value;
      touched = true;
    } else if (arg === "--clear-serve-config") {
      clear = true;
    } else {
      rest.push(arg);
    }
  }
  if (clear && touched) {
    throw new Error("--clear-serve-config cannot be combined with --host/--port/--web-root");
  }
  return { patch: touched ? patch : undefined, clear, rest };
}

export async function clearServeConfig(layout: ServerLayout): Promise<void> {
  await rm(layout.serveConfigFile, { force: true });
}
