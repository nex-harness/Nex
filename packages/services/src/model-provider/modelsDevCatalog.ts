import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import type { ModelConfigObject } from "@nex/provider";
import type { ModelInputFormatData } from "@nex/shared/model-config";
import { getAppConfigDir } from "../paths.js";
import type { ModelInfoLookupResult } from "./providerFacadeServices.js";

/**
 * models.dev 公共模型目录客户端（仅 Node 进程可用）。
 *
 * 职责：拉取并缓存 https://models.dev/api.json，把命中的模型条目映射为
 * 稀疏 ModelConfig（contextWindow / maxOutputTokens / 推理档位 / 输入输出格式 /
 * 工具调用 / JSON Schema 输出），供模型编辑器"获取模型信息"使用。
 *
 * 用户侧模型 ID 可能带命名空间前缀（如 "cli/gpt-5.6-sol"），查找时同时匹配
 * 完整 ID 与去掉前缀后的模型名，大小写不敏感；跨供应商的同名条目聚合后再使用。
 */

const MODELS_DEV_API_URL = "https://models.dev/api.json";
const REFRESH_INTERVAL_MS = 24 * 60 * 60 * 1000;
const FETCH_TIMEOUT_MS = 10_000;

interface ModelsDevModel {
  readonly modalities?: { readonly input?: readonly string[] };
  readonly limit?: { readonly context?: number; readonly output?: number };
  readonly tool_call?: boolean;
  readonly structured_output?: boolean;
  readonly reasoning_options?: readonly {
    readonly type?: string;
    readonly values?: readonly string[];
  }[];
}

interface ModelsDevProvider {
  readonly models?: Record<string, ModelsDevModel>;
}

export type ModelsDevCatalog = Record<string, ModelsDevProvider>;

export interface ModelsDevMatch {
  readonly providerId: string;
  readonly model: ModelsDevModel;
}

interface CatalogCache {
  readonly data: ModelsDevCatalog;
  readonly fetchedAt: number;
}

let memoryCache: CatalogCache | null = null;
let inflight: Promise<ModelsDevCatalog | null> | null = null;

function cacheFilePath(): string {
  return join(getAppConfigDir(), "runtime", "models-dev", "api.json");
}

function isCatalog(value: unknown): value is ModelsDevCatalog {
  return typeof value === "object" && value !== null && Object.keys(value).length > 0;
}

async function readDiskCache(): Promise<CatalogCache | null> {
  try {
    const parsed = JSON.parse(await readFile(cacheFilePath(), "utf8")) as Partial<CatalogCache>;
    if (!isCatalog(parsed?.data)) return null;
    return {
      data: parsed.data,
      fetchedAt: typeof parsed.fetchedAt === "number" ? parsed.fetchedAt : 0,
    };
  } catch {
    // 文件不存在或内容损坏都等价于"无缓存"。
    return null;
  }
}

async function writeDiskCache(cache: CatalogCache): Promise<void> {
  const filePath = cacheFilePath();
  const tempPath = `${filePath}.${process.pid}.tmp`;
  try {
    await mkdir(dirname(filePath), { recursive: true });
    // 先写临时文件再 rename，避免多进程并发读到半截 JSON。
    await writeFile(tempPath, JSON.stringify(cache), "utf8");
    await rename(tempPath, filePath);
  } catch {
    // 缓存写失败只影响下次拉取速度，不阻塞目录使用。
  }
}

async function fetchCatalog(): Promise<ModelsDevCatalog> {
  const response = await fetch(MODELS_DEV_API_URL, {
    signal: AbortSignal.timeout(FETCH_TIMEOUT_MS),
    headers: { accept: "application/json" },
  });
  if (!response.ok) throw new Error(`models.dev API responded with HTTP ${response.status}`);
  const data: unknown = await response.json();
  if (!isCatalog(data)) throw new Error("models.dev API returned an empty catalog");
  return data;
}

function isFresh(cache: CatalogCache): boolean {
  return Date.now() - cache.fetchedAt < REFRESH_INTERVAL_MS;
}

async function loadCatalog(): Promise<ModelsDevCatalog | null> {
  const disk = await readDiskCache();
  if (disk && isFresh(disk)) {
    memoryCache = disk;
    return disk.data;
  }
  try {
    const data = await fetchCatalog();
    memoryCache = { data, fetchedAt: Date.now() };
    await writeDiskCache(memoryCache);
    return data;
  } catch {
    // 离线时退回过期的磁盘数据；不写入内存缓存，下次调用仍会重试网络。
    return disk?.data ?? null;
  }
}

/** 加载目录：内存 → 磁盘（未过期）→ 网络；网络失败时退回过期数据，全都没有时返回 null。 */
function loadModelsDevCatalog(): Promise<ModelsDevCatalog | null> {
  if (memoryCache && isFresh(memoryCache)) return Promise.resolve(memoryCache.data);
  inflight ??= loadCatalog().finally(() => {
    inflight = null;
  });
  return inflight;
}

/**
 * 命名空间感知查找，返回所有命中条目（聚合商与官方同名条目并存很常见）：
 * 同时匹配完整 ID 与逐层去掉命名空间前缀后的各个后缀，大小写不敏感。
 */
export function findModelsDevModels(catalog: ModelsDevCatalog, modelId: string): ModelsDevMatch[] {
  const normalized = modelId.trim().toLowerCase();
  if (!normalized) return [];
  // 命名空间可能有多层（"cst/claude/claude-sonnet-5-5"），目录键本身也可能带斜杠
  // （"anthropic/claude-..."），所以逐层剥掉前缀，把每个后缀都作为候选。
  const candidates = new Set([normalized]);
  let separatorIndex = normalized.indexOf("/");
  while (separatorIndex >= 0) {
    const suffix = normalized.slice(separatorIndex + 1);
    if (suffix) candidates.add(suffix);
    separatorIndex = normalized.indexOf("/", separatorIndex + 1);
  }
  const matches: ModelsDevMatch[] = [];
  for (const [providerId, provider] of Object.entries(catalog)) {
    for (const [modelKey, model] of Object.entries(provider.models ?? {})) {
      if (candidates.has(modelKey.trim().toLowerCase())) matches.push({ providerId, model });
    }
  }
  return matches;
}

const INPUT_MODALITY_KEYS = {
  text: "supportsText",
  image: "supportsImage",
  video: "supportsVideo",
  audio: "supportsAudio",
  pdf: "supportsPdf",
} as const satisfies Record<string, keyof ModelInputFormatData>;

/**
 * 众数聚合。平票时数值取最大（聚合商常把上限缩水，最大值更接近官方），
 * 布尔取 true，其余保持首次出现的顺序。
 */
function majority<T extends number | boolean>(values: readonly (T | undefined)[]): T | undefined;
function majority<T>(
  values: readonly (T | undefined)[],
  keyOf: (value: T) => string,
): T | undefined;
function majority<T>(
  values: readonly (T | undefined)[],
  keyOf: (value: T) => string = String,
): T | undefined {
  const counts = new Map<string, { value: T; count: number }>();
  for (const value of values) {
    if (value === undefined) continue;
    const key = keyOf(value);
    const entry = counts.get(key);
    if (entry) entry.count += 1;
    else counts.set(key, { value, count: 1 });
  }
  let best: { value: T; count: number } | undefined;
  for (const entry of counts.values()) {
    if (
      !best ||
      entry.count > best.count ||
      (entry.count === best.count && prefers(entry.value, best.value))
    )
      best = entry;
  }
  return best?.value;
}

function prefers(candidate: unknown, current: unknown): boolean {
  if (typeof candidate === "number" && typeof current === "number") return candidate > current;
  return candidate === true && current !== true;
}

function positiveInteger(value: unknown): number | undefined {
  return typeof value === "number" && Number.isInteger(value) && value > 0 ? value : undefined;
}

function booleanValue(value: unknown): boolean | undefined {
  return typeof value === "boolean" ? value : undefined;
}

/** effort 类推理档位；空数组或含空串/重复项的数据不满足 reasoningLevel.values 约束，直接丢弃。 */
function effortLevels(model: ModelsDevModel): readonly string[] | undefined {
  const values = model.reasoning_options?.find((option) => option.type === "effort")?.values;
  if (!Array.isArray(values) || values.length === 0) return undefined;
  if (values.some((value) => typeof value !== "string" || !value.trim())) return undefined;
  return new Set(values).size === values.length ? values : undefined;
}

/** 跨供应商聚合同一模型的条目，产出单一稀疏 ModelConfig。 */
export function modelConfigFromMatches(matches: readonly ModelsDevMatch[]): ModelConfigObject {
  const models = matches.map((match) => match.model);
  const contextWindow = majority(models.map((m) => positiveInteger(m.limit?.context)));
  const maxOutputTokens = majority(models.map((m) => positiveInteger(m.limit?.output)));
  const supportsToolCall = majority(models.map((m) => booleanValue(m.tool_call)));
  const supportsJsonSchemaOutput = majority(models.map((m) => booleanValue(m.structured_output)));
  const reasoningLevels = majority(models.map(effortLevels), (values) => values.join("\u0000"));

  // 输入模态：任一条目声明支持即视为支持；有模态数据时未声明的类型按不支持。
  const modalityLists = models
    .map((m) => m.modalities?.input)
    .filter((input): input is readonly string[] => Array.isArray(input));
  let inputFormat: ModelInputFormatData | undefined;
  if (modalityLists.length > 0) {
    const declared = new Set(modalityLists.flat());
    inputFormat = Object.fromEntries(
      Object.entries(INPUT_MODALITY_KEYS).map(([modality, key]) => [key, declared.has(modality)]),
    ) as unknown as ModelInputFormatData;
  }

  const properties = {
    ...(contextWindow !== undefined ? { contextWindow } : {}),
    ...(supportsToolCall !== undefined ? { supportsToolCall } : {}),
    ...(supportsJsonSchemaOutput !== undefined ? { supportsJsonSchemaOutput } : {}),
    ...(inputFormat ? { inputFormat, outputFormat: { supportsText: true } } : {}),
  };
  const optionSpecs = {
    ...(maxOutputTokens !== undefined ? { maxOutputTokens: { max: maxOutputTokens } } : {}),
    ...(reasoningLevels !== undefined ? { reasoningLevel: { values: [...reasoningLevels] } } : {}),
  };
  return {
    ...(Object.keys(properties).length > 0 ? { properties } : {}),
    ...(Object.keys(optionSpecs).length > 0 ? { optionSpecs } : {}),
  };
}

/**
 * "获取模型信息"入口：按模型 ID 查询 models.dev 并聚合成稀疏配置。
 * 目录不可用（离线且无缓存）时抛错，让调用方区分"未收录"与"查询失败"。
 */
export async function lookupModelsDevModelInfo(modelId: string): Promise<ModelInfoLookupResult> {
  const catalog = await loadModelsDevCatalog();
  if (!catalog) throw new Error("models.dev catalog is unavailable");
  const matches = findModelsDevModels(catalog, modelId);
  if (matches.length === 0) return { found: false, config: {} };
  return {
    found: true,
    providerId: matches[0]!.providerId,
    config: modelConfigFromMatches(matches),
  };
}
