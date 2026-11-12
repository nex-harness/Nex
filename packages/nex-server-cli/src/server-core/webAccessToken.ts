import { randomBytes, randomUUID } from "node:crypto";
import { chmod, mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import { dirname } from "node:path";
import type { ServerLayout } from "../runtime/paths.js";

// web access token 与 ingress token 是两个凭据：前者给浏览器登录（长期、用户可见），
// 后者每次启动轮换、只给桌面/SSH/反代这类程序化入口。规则见 docs/specs/server-web-login.md。

export const WEB_ACCESS_TOKEN_ENV = "NEX_SERVER_WEB_TOKEN";
export const MIN_ENV_WEB_ACCESS_TOKEN_LENGTH = 12;

export type WebAccessTokenSource = "env" | "file";

export interface ResolvedWebAccessToken {
  token: string;
  source: WebAccessTokenSource;
}

async function writeTokenFile(layout: ServerLayout, token: string): Promise<void> {
  await mkdir(dirname(layout.webAccessTokenFile), { recursive: true, mode: 0o700 });
  const temporary = `${layout.webAccessTokenFile}.${process.pid}.${randomUUID()}.tmp`;
  try {
    await writeFile(temporary, `${token}\n`, { encoding: "utf8", mode: 0o600 });
    await rename(temporary, layout.webAccessTokenFile);
    if (process.platform !== "win32") await chmod(layout.webAccessTokenFile, 0o600);
  } finally {
    await rm(temporary, { force: true }).catch(() => undefined);
  }
}

async function readTokenFile(layout: ServerLayout): Promise<string | undefined> {
  try {
    return (await readFile(layout.webAccessTokenFile, "utf8")).trim() || undefined;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return undefined;
    throw error;
  }
}

function generateToken(): string {
  return randomBytes(32).toString("base64url");
}

/** 文件里的 token；不存在则生成并落盘。env 覆盖不在这里处理。 */
export async function readOrCreateWebAccessTokenFile(layout: ServerLayout): Promise<string> {
  const existing = await readTokenFile(layout);
  if (existing) return existing;
  const token = generateToken();
  await writeTokenFile(layout, token);
  return token;
}

/** 重新生成文件里的 token。运行中的 Core 仍持有旧值，需要 restart 生效。 */
export async function rotateWebAccessTokenFile(layout: ServerLayout): Promise<string> {
  const token = generateToken();
  await writeTokenFile(layout, token);
  return token;
}

/** Core 启动时解析登录凭据：env 优先（必须足够长），否则读取/创建文件。 */
export async function resolveWebAccessToken(
  layout: ServerLayout,
  env: NodeJS.ProcessEnv = process.env,
): Promise<ResolvedWebAccessToken> {
  const fromEnv = env[WEB_ACCESS_TOKEN_ENV]?.trim();
  if (fromEnv) {
    if (fromEnv.length < MIN_ENV_WEB_ACCESS_TOKEN_LENGTH) {
      throw new Error(
        `${WEB_ACCESS_TOKEN_ENV} must be at least ${MIN_ENV_WEB_ACCESS_TOKEN_LENGTH} characters`,
      );
    }
    return { token: fromEnv, source: "env" };
  }
  return { token: await readOrCreateWebAccessTokenFile(layout), source: "file" };
}
