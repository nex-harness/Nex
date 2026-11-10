import { watch, type FSWatcher } from "node:fs";
import { createHash } from "node:crypto";
import { mkdir, readFile } from "node:fs/promises";
import { basename, dirname, resolve } from "node:path";
import type { ProviderConfigLayerSnapshot, ProviderSource } from "@nex/provider";
import { atomicWritePrivateTextFile, withFileLock } from "@nex/shared/node";
import {
  decodeNexBuiltinRelease,
  encodeNexBuiltinRelease,
  serializeNexBuiltinRelease,
  type NexBuiltinRelease,
} from "./nex-builtin-release.js";

export interface NodeNexBuiltinProviderConfigSourceOptions {
  readonly bundledFilePath: string;
  readonly activeFilePath?: string;
  readonly watch?: boolean;
}

export type ApplyNexBuiltinReleaseResult = "updated" | "unchanged" | "stale";

/** Bundled、Active/LKG 与 Remote 共用同一 Release，并最终发布为现有 Config Snapshot。 */
export class NodeNexBuiltinProviderConfigSource implements ProviderSource<ProviderConfigLayerSnapshot> {
  readonly #bundledFilePath: string;
  readonly #activeFilePath: string;
  readonly #sourceKey: string;
  readonly #watchEnabled: boolean;
  readonly #listeners = new Set<(reason: string) => void>();
  #watcher: FSWatcher | null = null;
  #observedSignature: string | null = null;
  #watchRefresh = Promise.resolve();
  #disposed = false;

  constructor(options: NodeNexBuiltinProviderConfigSourceOptions) {
    const bundledFilePath = options.bundledFilePath.trim();
    if (!bundledFilePath) throw new Error("Nex Built-in bundledFilePath 不能为空");
    this.#bundledFilePath = bundledFilePath;
    this.#activeFilePath = options.activeFilePath?.trim() || bundledFilePath;
    // 旧标识只有发布序号，不同 Endpoint 同序号会让 Registry 误复用上一来源。
    // Active 路径已含规范化 Endpoint 隔离范围；Worker 收到同一路径，不另拼账号事实。
    this.#sourceKey = createHash("sha256").update(resolve(this.#activeFilePath)).digest("hex");
    this.#watchEnabled = options.watch !== false;
  }

  get activeFilePath(): string {
    return this.#activeFilePath;
  }

  async read(): Promise<ProviderConfigLayerSnapshot> {
    this.#assertNotDisposed();
    let release: NexBuiltinRelease;
    try {
      await this.#ensureWatcher();
      release = await withFileLock(this.#activeFilePath, () => this.#readAndMaterializeLocked());
    } catch {
      // Active 只是可丢弃缓存，目录锁、监听或原子物化失败不能阻断
      // Bundled 基线。缓存边界不可用时绕过 Active；Bundled 自身无效仍会在这里抛错。
      release = selectReleaseCandidate(
        await readBundledReleaseCandidate(this.#bundledFilePath),
        null,
      );
    }
    this.#observedSignature ??= signatureOf(release);
    return snapshotFromRelease(release, this.#sourceKey);
  }

  async applyRemoteRelease(release: NexBuiltinRelease): Promise<ApplyNexBuiltinReleaseResult> {
    this.#assertNotDisposed();
    await this.#ensureWatcher();
    const result = await withFileLock(this.#activeFilePath, async () => {
      this.#assertNotDisposed();
      const current = await this.#readAndMaterializeLocked();
      this.#assertNotDisposed();
      if (release.revision < current.revision) return "stale" as const;
      if (release.revision === current.revision) {
        if (serializeNexBuiltinRelease(release) === serializeNexBuiltinRelease(current)) {
          return "unchanged" as const;
        }
        throw new Error(`Nex Built-in 相同 revision ${release.revision} 对应不同内容`);
      }
      await this.#writeActiveLocked(release);
      this.#observedSignature = signatureOf(release);
      return "updated" as const;
    });
    if (result === "updated" && !this.#disposed) this.#emit("remote-updated");
    return result;
  }

  onDidChange(listener: (reason: string) => void): () => void {
    this.#assertNotDisposed();
    this.#listeners.add(listener);
    return () => this.#listeners.delete(listener);
  }

  dispose(): void {
    if (this.#disposed) return;
    this.#disposed = true;
    this.#watcher?.close();
    this.#watcher = null;
    this.#listeners.clear();
  }

  async #readAndMaterializeLocked(): Promise<NexBuiltinRelease> {
    const [bundled, active] = await Promise.all([
      readBundledReleaseCandidate(this.#bundledFilePath),
      this.#activeFilePath === this.#bundledFilePath
        ? Promise.resolve(null)
        : readReleaseCandidate(this.#activeFilePath),
    ]);
    const selected = selectReleaseCandidate(bundled, active);
    if (this.#activeFilePath !== this.#bundledFilePath) {
      const activeSignature = active?.release ? signatureOf(active.release) : null;
      if (activeSignature !== signatureOf(selected)) await this.#writeActiveLocked(selected);
    }
    return selected;
  }

  async #writeActiveLocked(release: NexBuiltinRelease): Promise<void> {
    this.#assertNotDisposed();
    await atomicWritePrivateTextFile(
      this.#activeFilePath,
      JSON.stringify(encodeNexBuiltinRelease(release), null, 2),
    );
  }

  async #ensureWatcher(): Promise<void> {
    await mkdir(dirname(this.#activeFilePath), { recursive: true });
    if (!this.#watchEnabled || this.#watcher || this.#disposed) return;
    const target = basename(this.#activeFilePath);
    this.#watcher = watch(dirname(this.#activeFilePath), (_eventType, fileName) => {
      if (fileName === null || fileName.toString() === target) this.#scheduleWatchRefresh();
    });
    this.#watcher.on("error", () => this.#emit("watch-error"));
  }

  #scheduleWatchRefresh(): void {
    this.#watchRefresh = this.#watchRefresh.then(async () => {
      if (this.#disposed) return;
      try {
        const release = await withFileLock(this.#activeFilePath, () =>
          this.#readAndMaterializeLocked(),
        );
        const signature = signatureOf(release);
        if (this.#disposed || signature === this.#observedSignature) return;
        this.#observedSignature = signature;
        this.#emit("file-changed");
      } catch {
        this.#emit("watch-error");
      }
    });
  }

  #emit(reason: string): void {
    if (this.#disposed) return;
    for (const listener of this.#listeners) listener(reason);
  }

  #assertNotDisposed(): void {
    if (this.#disposed) throw new Error("NodeNexBuiltinProviderConfigSource 已 dispose");
  }
}

export function createNodeNexBuiltinProviderConfigSource(
  options: NodeNexBuiltinProviderConfigSourceOptions,
): NodeNexBuiltinProviderConfigSource {
  return new NodeNexBuiltinProviderConfigSource(options);
}

interface ReleaseCandidate {
  readonly release?: NexBuiltinRelease;
  readonly error?: unknown;
}

/**
 * Bug 原因：远端 Core 只在启动时把构建内嵌的 Bundled 物化到 `runtime/provider/bundled`，
 * 运行中该目录与 Endpoint Active 一起被删除后，每次读取都拿到两个 ENOENT，Provisioning
 * 同步持续失败到 Core 重启。Bundled 在进程生命周期内不可变（升级以退出旧进程为前提），
 * 因此按路径记住本进程最近一次成功解析的 Bundled，仅在文件缺失时作为基线；随后照常由
 * 选中的候选重建 Active。不回写 Bundled 路径：Desktop 的 Bundled 位于只读安装资源内。
 * 用模块级缓存而非实例字段，是因为 Endpoint 切换会新建 Source 实例，也要能读到同一基线。
 * Worker 把 Host 的 Active 当作 Bundled 读取时，这里等价于最近一次成功读取的 LKG。
 */
const lastValidBundledReleaseByPath = new Map<string, NexBuiltinRelease>();

async function readBundledReleaseCandidate(filePath: string): Promise<ReleaseCandidate> {
  const key = resolve(filePath);
  const candidate = await readReleaseCandidate(filePath);
  if (candidate.release) {
    lastValidBundledReleaseByPath.set(key, candidate.release);
    return candidate;
  }
  const baseline = isFileNotFound(candidate.error)
    ? lastValidBundledReleaseByPath.get(key)
    : undefined;
  return baseline ? { release: baseline } : candidate;
}

async function readReleaseCandidate(filePath: string): Promise<ReleaseCandidate> {
  try {
    return { release: decodeNexBuiltinRelease(JSON.parse(await readFile(filePath, "utf8"))) };
  } catch (error) {
    // Bug 原因：ENOENT 曾直接返回 null，两份文件都缺失时 AggregateError 的错误列表为空，
    // 日志只剩“均不可用”而没有路径。缺失同样是无效候选，保留原始错误供汇总报告。
    return { error };
  }
}

function selectReleaseCandidate(
  bundled: ReleaseCandidate | null,
  active: ReleaseCandidate | null,
): NexBuiltinRelease {
  if (
    bundled?.release &&
    active?.release &&
    bundled.release.revision === active.release.revision &&
    serializeNexBuiltinRelease(bundled.release) !== serializeNexBuiltinRelease(active.release)
  ) {
    // 同 revision 冲突属于 Active 缓存失效，不能反向使可信 Bundled 无法启动。
    // 返回 Bundled 后调用方会在能够写入时原子替换 Active。
    return bundled.release;
  }
  const valid = [bundled?.release, active?.release].filter(
    (candidate): candidate is NexBuiltinRelease => candidate !== undefined,
  );
  if (valid.length === 0) {
    throw new AggregateError(
      [bundled?.error, active?.error].filter((error) => error !== undefined),
      "Bundled 与 Active Nex Built-in Release 均不可用",
    );
  }
  return valid.reduce((newest, candidate) =>
    candidate.revision > newest.revision ? candidate : newest,
  );
}

function snapshotFromRelease(
  release: NexBuiltinRelease,
  sourceKey: string,
): ProviderConfigLayerSnapshot {
  return Object.freeze({
    revision: `nex-builtin:${release.revision}:${sourceKey}`,
    providers: release.config.providers,
    providerTemplates: release.config.providerTemplates,
    models: release.config.modelConfigRules,
  });
}

function signatureOf(release: NexBuiltinRelease): string {
  return `${release.revision}:${serializeNexBuiltinRelease(release)}`;
}

function isFileNotFound(error: unknown): boolean {
  return (
    typeof error === "object" &&
    error !== null &&
    "code" in error &&
    (error as { code?: unknown }).code === "ENOENT"
  );
}
